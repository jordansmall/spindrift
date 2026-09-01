// Package settle drives a Dispatch from Box-exit to its terminal lifecycle
// state: interpreting the Outcome line, watching CI, self-heal fix passes,
// the merge or push-only landing under MERGE_MODE and the Merge guard,
// merged-verification (tripwire), and usage-comment posting. The seam is
// Settler + Settle (the prod adapter) + Fake.
package settle

import (
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/terminate"
)

// Config carries the subset of launcher config a Settle needs.
type Config struct {
	// MergeMode controls post-green behavior: "immediate" merges the PR,
	// "manual" leaves it open, "auto" enqueues GitHub's native auto-merge.
	MergeMode string

	// MergeGuardPaths is a comma-separated list of globs matched against
	// every changed path in the PR; a hit downgrades the merge to manual
	// regardless of MergeMode. Empty disables the guard.
	MergeGuardPaths string

	// CompleteLabel is what verifyMerged checks for on the tripwire path.
	CompleteLabel string

	// Merge gate polling knobs.
	MergePollInterval int
	MergePollTimeout  int
	MaxFixAttempts    int
	MaxRebaseAttempts int

	// Policy is the transient-retry tuning shared with dispatch's exit-retry
	// path. Policy.Max caps merge-transient retries, not MaxRebaseAttempts,
	// which is a merge-conflict budget.
	Policy retry.Policy

	// Clock is the rebase-push backoff's sleep seam, defaulting to
	// dispatch.RealClock() when its Sleep field is nil.
	Clock dispatch.Clock

	// MaxBudgetTokens and MaxBudgetUSD cap cumulative usage — summed across
	// every attempt log dispatched so far — before selfHealGate launches
	// another fix pass. Reaching or exceeding either cap stops the run with a
	// distinct budget-exhausted status, never merging partial work. Zero
	// means "no cap" for that dimension; the two knobs are independent.
	MaxBudgetTokens int
	MaxBudgetUSD    float64

	// PreflightStaleBase opts into ADR 0026's proactive stale-base rebase:
	// mergeImmediate rebases a green PR that is behind its base and re-waits
	// for CI before merging. When false (the default), a green-but-behind PR
	// merges as-is (ADR 0028).
	PreflightStaleBase bool

	// OutboxDir resolves an issue number to its Box's writable outbox
	// directory (CODE_FORGE=local, ADR 0033) — where the Code Forge's
	// optional BundleRelay hook reads the code-out bundle from before Merge.
	// Nil for every non-local construction site.
	OutboxDir func(num string) string

	// CodeForgeForIssue resolves num's own CodeForge instance for the
	// parent-sensitive landing calls (RelayBundle and its capability probes,
	// Merge, and the reactive Rebase retry in mergeImmediate; LandingRef in
	// landPushOnly). Under CODE_FORGE=local (ADR 0033) each dispatched issue
	// may key its Integration branch off a different parent, so those calls
	// must land through its own resolved instance, not the single cf New()
	// received; parent-agnostic operations still use New's cf. Nil defaults
	// to always returning that cf — every non-local site has exactly one.
	CodeForgeForIssue func(num string) forge.CodeForge

	// ReadOnly mirrors BOX_FORGE_AND_ISSUE_ACCESS=read-only: the Box holds no
	// in-box write token, so its blocked-note comment travels via the outcome
	// note= field regardless of what the tracker implements.
	ReadOnly bool

	// BaseBranch is the target branch a host-mediated draft PR is opened
	// against: under read-only access the Box never runs `gh pr create`
	// itself, so hostMediateDraftPR supplies the base it would have passed as
	// --base. Unused elsewhere — every read-write PR is opened in-box.
	BaseBranch string

	// Capabilities is this run's resolved backend capabilities, threaded down
	// rather than probed from cf/it by New.
	Capabilities forge.Capabilities
}

// Settler is the narrow "settle a dispatch result" surface every generic
// caller depends on, as opposed to the work-only adopt/relay surface below.
// Tests can inject a Fake instead of a real Settle.
type Settler interface {
	// Settle interprets result and drives num to its terminal label: CI-watch,
	// self-heal fix passes via d, merge modes, the Merge guard,
	// merged-verification, and the usage comment. gen is the
	// terminate.Registry generation this call's own dispatch was launched
	// under — every termination checkpoint checks against gen specifically, so
	// a re-pick's later generation can never be mistaken for this one. Callers
	// with no Registry pass the zero value, which never matches a real mark.
	Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result)

	// Fail records a Box that ran and exited non-zero. It runs no merge-gate
	// machinery — the caller already transitioned the tracker issue to Failed
	// — and exists solely so a wrapper (the Console's queueSettler) can react
	// to a natural Box failure the same way it reacts to a settle.
	Fail(num string, gen uint64, result dispatch.Result)
}

// WorkSettler is the work-only adopt/relay surface, consumed only by
// recover's adopt path — research never touches the Code Forge.
type WorkSettler interface {
	// SettleAdopted runs the same merge gate as Settle, for an
	// already-discovered open PR (draft or not) with no outcome line.
	SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string)

	// SettleRelayedBranch is recover's adopt-a-relayed-branch entry point. sit
	// is the caller's already-computed Situation, whose open-PR fact is a hard
	// precondition: this returns false immediately when sit.OpenPRFound is
	// true, since that shape is SettleAdopted's job. Otherwise it consults
	// result's self-report evidence and, if it is a genuine success, relays
	// the branch, opens a PR, and runs the same merge gate as Settle. Returns
	// false when there is no relayable evidence, leaving the caller's own "no
	// open PR" handling unchanged.
	SettleRelayedBranch(d dispatch.Dispatcher, num string, gen uint64, sit Situation, result dispatch.Result) bool

	// SituationFor computes num's shared adoption-evidence Situation so a
	// caller outside this package can thread the same value into
	// SettleRelayedBranch without duplicating situationFor's logic.
	// openPRFound is the caller's own resolved fact, passed through unchanged.
	SituationFor(num string, openPRFound bool, result dispatch.Result) Situation
}

// Settle is the prod adapter: constructed once per top-level dispatch entry
// point and reused across every issue in that invocation. Safe for concurrent
// use across dispatchWave goroutines because it holds no mutable state of its
// own beyond the (concurrency-safe) it/cf.
type Settle struct {
	cfg Config
	it  forge.IssueTracker
	cf  forge.CodeForge
	// pr is cf's PRForge surface — nil for the push-only git adapter, which
	// callers branch on.
	pr forge.PRForge
	// landing is the IssueTracker's optional LandingRecorder surface (ADR
	// 0029) — nil for github/jira, which don't implement it.
	landing forge.LandingRecorder
	// readOnly mirrors Config.ReadOnly — see postBlockedNoteComment.
	readOnly bool
	// term is checked at every CI-watch/fix-pass/merge-gate loop checkpoint so
	// a Terminate (ADR 0024) landing mid-settle is noticed and abandoned
	// instead of corrupting the issue's state after Terminate already
	// reclaimed it. Nil means "never terminated" — the registry is nil-safe.
	term *terminate.Registry
	// cfForNum is Config.CodeForgeForIssue, defaulting to always returning cf.
	cfForNum func(num string) forge.CodeForge
	// clock backs the rebase-push backoff sleep, defaulting to
	// dispatch.RealClock().
	clock dispatch.Clock
}

// SetTerminated wires reg as this Settle's termination registry — called once
// by the Console's launcher wiring. Every headless dispatch path leaves it
// unset.
func (s *Settle) SetTerminated(reg *terminate.Registry) {
	s.term = reg
}

// terminated reports whether num was marked terminated at generation gen
// specifically — not merely whether some other generation of num was.
func (s *Settle) terminated(num string, gen uint64) bool {
	return s.term.Marked(num, gen)
}

// Fail is a no-op: the caller already transitioned the tracker issue to
// Failed, and Settle has no UI-facing state to react with. It exists only so
// a wrapper has a hook.
func (s *Settle) Fail(num string, gen uint64, result dispatch.Result) {}

var _ Settler = (*Settle)(nil)
var _ WorkSettler = (*Settle)(nil)

// New constructs a Settle. pr and landing come from cfg.Capabilities, which
// the caller resolves via forge.ResolveCapabilities.
func New(cfg Config, it forge.IssueTracker, cf forge.CodeForge) *Settle {
	pr := cfg.Capabilities.PRForge
	landing := cfg.Capabilities.LandingRecorder
	cfForNum := cfg.CodeForgeForIssue
	if cfForNum == nil {
		cfForNum = func(string) forge.CodeForge { return cf }
	}
	clock := cfg.Clock
	if clock.Sleep == nil {
		clock = dispatch.RealClock()
	}
	return &Settle{cfg: cfg, it: it, cf: cf, pr: pr, landing: landing, readOnly: cfg.ReadOnly, cfForNum: cfForNum, clock: clock}
}
