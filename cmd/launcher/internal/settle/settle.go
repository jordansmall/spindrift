// Package settle drives a Dispatch from Box-exit to its terminal lifecycle
// state (issue #442): interpreting the Outcome line, watching CI, self-heal
// fix passes, the merge or push-only landing under MERGE_MODE and the Merge
// guard, merged-verification (tripwire), and usage-comment posting. The seam
// is Settler + Settle (the prod adapter, constructed once with its config and
// the forge client) + Fake.
package settle

import (
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
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

	// CompleteLabel is the label name verifyMerged checks for on the tripwire
	// path.
	CompleteLabel string

	// Merge gate polling knobs.
	MergePollInterval int
	MergePollTimeout  int
	MaxFixAttempts    int
	MaxRebaseAttempts int

	// PreflightStaleBase opts into ADR 0026's proactive stale-base rebase:
	// when true, mergeImmediate rebases a green PR that is behind its base
	// (no textual conflict) and re-waits for CI before merging. When false
	// (the default), a green-but-behind PR merges as-is (ADR 0028).
	PreflightStaleBase bool

	// OutboxDir resolves an issue number to its Box's writable outbox
	// directory (CODE_FORGE=local, ADR 0033) — the host path the Code
	// Forge's optional BundleRelay hook reads the code-out bundle from
	// before Merge. Nil for every non-local construction site; consulted
	// only when the Code Forge implements forge.BundleRelay.
	OutboxDir func(num string) string

	// CodeForgeForIssue resolves num's own CodeForge instance for the
	// parent-sensitive landing calls (RelayBundle and its BundleRelay/
	// LandingRef capability probes, Merge, and the reactive Rebase retry,
	// all inside mergeImmediate; LandingRef in landPushOnly) — CODE_FORGE
	// =local, ADR 0033/issue #1734: each dispatched issue may key its
	// Integration branch off a different parent, so those calls must land
	// through ITS OWN resolved instance, not the single cf New() received.
	// Every parent-agnostic operation (AgentBranch, BranchExists, and the
	// PRForge capability resolved once at New() into s.pr) still uses New's
	// cf unchanged. Defaults to always returning that cf when nil (every
	// non-local construction site, and every pre-#1734 test) -- there is
	// exactly one instance to use there.
	CodeForgeForIssue func(num string) forge.CodeForge
}

// Settler is the seam callers depend on so tests can inject a Fake instead of
// a real Settle.
type Settler interface {
	// Settle interprets result (a Dispatcher's Run outcome) and drives num to
	// its terminal label: CI-watch, self-heal fix passes via d, merge modes,
	// the Merge guard, merged-verification, and the usage comment. gen is the
	// terminate.Registry generation (waves.Issue.Generation, issue #743) this
	// call's own dispatch was launched under — every termination checkpoint
	// this call makes checks against gen specifically, so a re-pick's later
	// generation can never be mistaken for this one (or vice versa). Callers
	// with no Registry (every headless dispatch path) pass the zero value,
	// which never matches a real mark.
	Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result)

	// SettleAdopted runs the same merge gate as Settle, for an
	// already-discovered open non-draft PR with no outcome line (the
	// reconcile/recover entry point). gen is as in Settle.
	SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string)

	// Fail records a Box that ran and exited non-zero (result.Success ==
	// false). Unlike Settle, it runs no merge-gate machinery — the caller
	// already transitioned the tracker issue to Failed itself — this hook
	// exists solely so a wrapper (e.g. the Console's queueSettler) can react
	// to a natural Box failure the same way it reacts to a settle (issue
	// #705). gen is as in Settle.
	Fail(num string, gen uint64, result dispatch.Result)
}

// Settle is the prod adapter: constructed once per top-level dispatch entry
// point with its config, IssueTracker, and CodeForge, then reused across
// every issue in that invocation. Safe for concurrent use across dispatchWave
// goroutines because it holds no mutable state of its own beyond the
// (concurrency-safe) it/cf.
type Settle struct {
	cfg Config
	it  forge.IssueTracker
	cf  forge.CodeForge
	// pr is cf's PRForge surface, resolved once at construction via a type
	// assertion — nil for the push-only git adapter. Callers branch on pr ==
	// nil instead of a removed PushOnly() flag.
	pr forge.PRForge
	// landing is the IssueTracker's optional LandingRecorder surface (ADR
	// 0029), resolved once at construction via a type assertion — nil for
	// github/jira, which don't implement it.
	landing forge.LandingRecorder
	// term is checked at every CI-watch/fix-pass/merge-gate loop checkpoint
	// so a Terminate (ADR 0024, issue #649) landing mid-settle is noticed and
	// abandoned instead of corrupting the issue's state after Terminate
	// already reclaimed it. Nil (every construction site but the Console's)
	// means "never terminated" — terminate.Registry is nil-safe.
	term *terminate.Registry
	// cfForNum resolves num's own CodeForge instance for the parent-sensitive
	// landing calls (Config.CodeForgeForIssue, issue #1734) — defaults to
	// always returning cf when Config.CodeForgeForIssue is nil.
	cfForNum func(num string) forge.CodeForge
}

// SetTerminated wires reg as this Settle's termination registry — called
// once by the Console's launcher wiring (issue #649). A Settle with no
// registry set (every headless dispatch path) behaves exactly as before.
func (s *Settle) SetTerminated(reg *terminate.Registry) {
	s.term = reg
}

// terminated reports whether num was marked terminated at generation gen
// specifically (issue #743) — not merely whether some earlier or later
// generation of num was.
func (s *Settle) terminated(num string, gen uint64) bool {
	return s.term.Marked(num, gen)
}

// Fail is a no-op: every headless dispatch path already transitions the
// tracker issue to Failed itself before calling this, and Settle has no
// queue or other UI-facing state of its own to react with. It exists only
// to satisfy Settler so a wrapper (the Console's queueSettler) has a hook.
func (s *Settle) Fail(num string, gen uint64, result dispatch.Result) {}

var _ Settler = (*Settle)(nil)

// New constructs a Settle. pr is resolved from cf once via a type assertion
// (nil when cf is push-only, e.g. the git adapter).
func New(cfg Config, it forge.IssueTracker, cf forge.CodeForge) *Settle {
	pr, _ := cf.(forge.PRForge)
	landing, _ := it.(forge.LandingRecorder)
	cfForNum := cfg.CodeForgeForIssue
	if cfForNum == nil {
		cfForNum = func(string) forge.CodeForge { return cf }
	}
	return &Settle{cfg: cfg, it: it, cf: cf, pr: pr, landing: landing, cfForNum: cfForNum}
}
