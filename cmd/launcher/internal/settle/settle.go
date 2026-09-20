// Package settle drives a Dispatch from Box-exit to its terminal lifecycle
// state (issue #442): the Outcome line, CI watching, self-heal fix passes, the
// merge or push-only landing, merged-verification, and the usage comment.
package settle

import (
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/terminate"
)

// Config carries the subset of launcher config a Settle needs.
type Config struct {
	// MergeMode is "immediate" (merge the PR), "manual" (leave it open), or
	// "auto" (enqueue GitHub's native auto-merge).
	MergeMode string

	// MergeGuardPaths is a comma-separated list of globs. A hit on any changed
	// path in the PR downgrades the merge to manual whatever MergeMode says;
	// empty disables the guard.
	MergeGuardPaths string

	// CompleteLabel is the label verifyMerged checks for on the tripwire path.
	CompleteLabel string

	MergePollInterval int
	MergePollTimeout  int
	MaxFixAttempts    int
	MaxRebaseAttempts int

	// Policy tunes transient retries (issue #2928); the rebase-push loops reuse
	// dispatch's exit-retry policy (issue #2095). Policy.Max caps
	// merge-transient retries (issue #2325); MaxRebaseAttempts is a separate
	// merge-conflict budget.
	Policy retry.Policy

	// Clock is the sleep seam the rebase-push backoff sleeps through (issue
	// #2095). Defaults to dispatch.RealClock() when its Sleep field is nil.
	Clock dispatch.Clock

	// MaxBudgetTokens and MaxBudgetUSD cap cumulative usage (issues #2001,
	// #2575): selfHealGate sums every attempt dispatched so far and checks both
	// caps before launching another fix pass. Reaching either one stops the run
	// with a budget-exhausted status and never merges partial work. Zero
	// disables that dimension.
	MaxBudgetTokens int
	MaxBudgetUSD    float64

	// PreflightStaleBase opts into ADR 0026: mergeImmediate rebases a green PR
	// that is behind its base and re-waits for CI before merging. When false,
	// a green-but-behind PR merges as-is (ADR 0028).
	PreflightStaleBase bool

	// OutboxDir resolves an issue number to its Box's writable outbox directory
	// (CODE_FORGE=local, ADR 0033), which the Code Forge's BundleRelay hook
	// reads the code-out bundle from before Merge. Nil at every non-local
	// construction site, and read only when the Code Forge implements
	// forge.BundleRelay.
	OutboxDir func(num string) string

	// CodeForgeForIssue resolves num's own CodeForge for the parent-sensitive
	// landing calls (ADR 0033, issue #1734): under CODE_FORGE=local each issue
	// may key its Integration branch off a different parent, so RelayBundle,
	// Merge, Rebase, and LandingRef must land through that instance. Nil
	// returns New's cf, the only instance elsewhere.
	CodeForgeForIssue func(num string) forge.CodeForge

	// ReadOnly mirrors BOX_FORGE_AND_ISSUE_ACCESS=read-only (issue #1917): the
	// Box holds no write token, so its blocked-note comment travels via the
	// outcome note= field whatever the tracker implements.
	ReadOnly bool

	// BaseBranch is the branch hostMediateDraftPR opens against (issue #1919),
	// since a read-only Box never runs the PR-create itself. Unused outside
	// that path.
	BaseBranch string

	// Capabilities is this run's resolved backend capabilities. The caller
	// resolves them via forge.ResolveCapabilities so New probes nothing itself
	// (issue #2945).
	Capabilities forge.Capabilities
}

// Settler is the "settle a dispatch result" interface every generic caller
// depends on: the waves engine, the Console's queue hook, and newSettle.
type Settler interface {
	// Settle interprets result and drives num to its terminal label. gen is the
	// terminate.Registry generation (issue #743) this call's own dispatch was
	// launched under, so a re-pick's later generation is never mistaken for it.
	// Callers with no Registry pass the zero value, which matches no real mark.
	Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result)

	// Fail records a Box that ran and exited non-zero. The caller already
	// transitioned the issue to Failed, so this runs no merge-gate machinery;
	// it exists so the Console's queueSettler can react to a Box failure the
	// same way it reacts to a settle (issue #705).
	Fail(num string, gen uint64, result dispatch.Result)
}

// WorkSettler is the work-only adopt/relay interface, used only by recover's
// adopt path. Research never touches the Code Forge, so it never needs this.
type WorkSettler interface {
	// SettleAdopted runs the same merge gate as Settle for an already-open PR
	// (draft or not) with no outcome line, the reconcile/recover entry point.
	SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string)

	// SettleRelayedBranch adopts a relayed branch (issue #2225). It returns
	// false when sit.OpenPRFound is true, since that shape is SettleAdopted's
	// job, and false when result carries no relayable success evidence, leaving
	// the caller's own "no open PR" handling unchanged. Otherwise it relays the
	// branch, opens a PR, and runs the same merge gate.
	SettleRelayedBranch(d dispatch.Dispatcher, num string, gen uint64, sit Situation, result dispatch.Result) bool

	// SituationFor computes num's adoption-evidence Situation (issue #2501) so
	// main.go's recoverByNumber can thread the same value into
	// SettleRelayedBranch. openPRFound is the caller's own resolved fact.
	SituationFor(num string, openPRFound bool, result dispatch.Result) Situation
}

// Settle is the prod adapter, constructed once per top-level dispatch entry
// point and reused across every issue in that invocation. Safe for concurrent
// use by dispatchWave goroutines: it holds no mutable state beyond the
// concurrency-safe it/cf.
type Settle struct {
	cfg Config
	it  forge.IssueTracker
	cf  forge.CodeForge
	// pr is nil for the push-only git adapter; callers branch on pr == nil.
	pr forge.PRForge
	// landing is nil for github/jira, which do not implement it (ADR 0029).
	landing forge.LandingRecorder
	// landingPass is nil for every tracker but local (issue #2983).
	landingPass forge.LandingPassRecorder
	// readOnly mirrors Config.ReadOnly (issue #1917). See postBlockedNoteComment.
	readOnly bool
	// Every CI-watch, fix-pass, and merge-gate checkpoint checks term, so a
	// Terminate that lands mid-settle (ADR 0024, issue #649) abandons the settle
	// instead of corrupting issue state Terminate already reclaimed.
	// New builds it and nothing ever writes it again, so the settle goroutines
	// reading it concurrently need no lock and no later caller can displace it
	// mid-run (issue #3522).
	term *terminate.Registry
	// cfForNum defaults to returning cf when Config.CodeForgeForIssue is nil
	// (issue #1734).
	cfForNum func(num string) forge.CodeForge
	// clock defaults to dispatch.RealClock() when Config.Clock is unset.
	clock dispatch.Clock
}

// Registrar is the "settler that owns a termination registry" seam: a caller
// holding a Settler or WorkSettler asks for the registry it must mark through
// without asserting a concrete type. Settler and WorkSettler stay narrower on
// purpose, since settle.Fake and ResearchSettle own no registry.
type Registrar interface {
	Registry() *terminate.Registry
}

// Registry returns this Settle's own termination registry. Every caller that
// marks a termination -- the Console's Terminate, the shutdown gate's abort --
// and every settle checkpoint that reads one must reach this registry and no
// other: a mark written into a registry the settler does not read lets an
// already-reclaimed issue settle on to agent-complete anyway, merging its PR
// out from under the reclaim (issues #649, #743, #3522).
func (s *Settle) Registry() *terminate.Registry { return s.term }

// terminated reports whether num was marked terminated at generation gen
// specifically (issue #743), not whether some other generation of num was.
func (s *Settle) terminated(num string, gen uint64) bool {
	return s.term.Marked(num, gen)
}

// Fail is a no-op: the caller already transitioned the issue to Failed. It
// exists only to satisfy Settler so the Console's queueSettler has a hook.
func (s *Settle) Fail(num string, gen uint64, result dispatch.Result) {}

var _ Settler = (*Settle)(nil)
var _ WorkSettler = (*Settle)(nil)
var _ Registrar = (*Settle)(nil)

// New constructs a Settle, reading pr and landing from cfg.Capabilities rather
// than re-deriving them here (issue #2945).
func New(cfg Config, it forge.IssueTracker, cf forge.CodeForge) *Settle {
	pr := cfg.Capabilities.PRForge
	landing := cfg.Capabilities.LandingRecorder
	landingPass := cfg.Capabilities.LandingPassRecorder
	cfForNum := cfg.CodeForgeForIssue
	if cfForNum == nil {
		cfForNum = func(string) forge.CodeForge { return cf }
	}
	clock := cfg.Clock
	if clock.Sleep == nil {
		clock = dispatch.RealClock()
	}
	return &Settle{cfg: cfg, it: it, cf: cf, pr: pr, landing: landing, landingPass: landingPass, readOnly: cfg.ReadOnly, cfForNum: cfForNum, clock: clock, term: terminate.NewRegistry()}
}
