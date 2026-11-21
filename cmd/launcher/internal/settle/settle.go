// Package settle drives a Dispatch from Box-exit to its terminal lifecycle
// state (issue #442): the Outcome line, CI watching, self-heal fix passes, the
// merge or push-only landing, merged-verification, and the usage comment.
package settle

import (
	"sync"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/terminate"
)

// Config carries the subset of launcher config a Settle needs.
type Config struct {
	// BranchPrefix is BRANCH_PREFIX, the input to forge.AgentBranchName.
	BranchPrefix string

	// MergeMode is "immediate" (merge the PR), "manual" (leave it open), or
	// "auto" (enqueue the forge's native auto-merge).
	MergeMode string

	// MergeGuardPaths is a comma-separated list of globs. A hit on any changed
	// path in the PR downgrades the merge to manual whatever MergeMode says;
	// empty disables the guard.
	MergeGuardPaths string

	// CompleteLabel is the label verifyMerged checks for on the tripwire path.
	CompleteLabel string

	// DispatchLabel is the configured work label an operator re-applies to retry
	// an issue; the unreviewed comment names it. Empty falls back to the default.
	DispatchLabel string

	// FindingLabel is the label review findings are filed under, read from
	// the dispatched kind's row.
	FindingLabel string

	MergePollInterval int
	MergePollTimeout  int
	MaxFixAttempts    int
	MaxRebaseAttempts int

	// Policy tunes transient retries (issue #2928), the rebase-push loops'
	// included (issue #2095); Policy.Max caps merge-transient and verifyMerged
	// read retries (issue #2325), apart from the MaxRebaseAttempts
	// merge-conflict budget.
	Policy retry.Policy

	// Clock is the sleep seam the rebase-push backoff sleeps through (issue
	// #2095). Defaults to dispatch.RealClock() when its Sleep field is nil.
	Clock dispatch.Clock

	// MaxBudgetTokens and MaxBudgetUSD cap cumulative usage across every
	// attempt, checked before each fix pass (issues #2001, #2575); reaching
	// either stops the run budget-exhausted, never merging partial work. Zero
	// disables that dimension.
	MaxBudgetTokens int
	MaxBudgetUSD    float64

	// PreflightStaleBase opts into ADR 0026: mergeImmediate rebases a green PR
	// that is behind its base and re-waits for CI before merging. When false,
	// a green-but-behind PR merges as-is (ADR 0028).
	PreflightStaleBase bool

	// OutboxDir resolves an issue number to its Box's outbox directory (ADR
	// 0033), where forge.BundleRelay reads the code-out bundle and from which a
	// merged landing removes it (issue #4653). Nil disables both.
	OutboxDir func(num string) string

	// LogPath resolves an issue number to its Dispatch's primary Pass log,
	// which flushSettled appends the dispatch_settled op to. Nil disables the
	// append.
	LogPath func(num string) string

	// CodeForgeForIssue resolves num's own CodeForge for the parent-sensitive
	// landing calls (ADR 0033, issue #1734): under CODE_FORGE=local each issue
	// may key its Integration branch off a different parent, so RelayBundle,
	// Merge, Rebase, and LandingRef must land through that instance. Nil
	// returns New's cf.
	CodeForgeForIssue func(num string) forge.CodeForge

	// BaseBranch is the branch hostMediateDraftPR opens against (issue #1919),
	// since a read-only Box never runs the PR-create itself.
	BaseBranch string

	// Capabilities is this run's resolved backend capabilities. The caller
	// resolves them via forge.ResolveCapabilities so New probes nothing itself
	// (issue #2945).
	Capabilities forge.Capabilities

	// Unclaimed marks a PR whose issue was never claimed (a butler patch
	// finding, ADR 0057, issue #4076): with no InProgress state to leave, the
	// gate commits no tracker state but Complete on a real merge, and a red
	// gate leaves the PR draft with a failure comment rather than
	// agent-failed. New clamps MaxFixAttempts to 0, since no Dispatcher exists
	// to run a fix pass.
	Unclaimed bool

	// Progress receives the per-issue progress steps; New defaults nil to
	// StdoutProgress.
	Progress Progress
}

// Settler is the "settle a dispatch result" interface every generic caller
// depends on: the waves engine and the launcher's selective list dispatch.
type Settler interface {
	// Settle interprets result and drives num to its terminal label. gen is the
	// terminate.Registry generation (issue #743) this dispatch launched under,
	// so a re-pick's later generation is never mistaken for it; a caller that
	// never calls Begin (recover, headless waves) passes 0 and still matches a
	// Mark.
	Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) Settlement
}

// MergeSettler is the merge gate that work, recover and the Butler's unclaimed
// patch gate settle through. Research never touches the Code Forge, so it
// settles through ResearchSettle instead.
type MergeSettler interface {
	Settler
	// Recover is spindrift recover's single entry; see Settle.Recover.
	Recover(in RecoverInput) (Settlement, error)
	// SettlePatch is the Butler patch gate's entry; see Settle.SettlePatch.
	SettlePatch(owner dispatchkey.Key, num, prURL string) Settlement
}

// Settle is the prod adapter, constructed once per top-level dispatch entry
// point and reused across every issue in that invocation. Safe for concurrent
// use by dispatchWave goroutines: the only mutable state beyond the
// concurrency-safe it/cf is the per-issue maps settledMu guards.
type Settle struct {
	cfg Config
	it  forge.IssueTracker
	cf  forge.CodeForge
	// lander is chosen once in New: prLander when the Code Forge has a PR forge,
	// else pushOnlyLander.
	lander lander
	// pr is nil for a push-only forge. Outside the lander, the merged arm,
	// read-only delivery and relayed-branch adopt paths check it directly.
	pr forge.PRForge
	// landing is nil for every tracker but local (ADR 0029).
	landing forge.LandingRecorder
	// landingPass is nil for every tracker but local (issue #2983).
	landingPass forge.LandingPassRecorder
	// ciRun is nil for forges that cannot report a CI run URL.
	ciRun forge.CIRunReporter
	// Every CI-watch, fix-pass and merge-gate checkpoint checks term, so a
	// Reclaim mid-settle (ADR 0024, issue #649) abandons the settle instead of
	// corrupting state it already reclaimed. Written once in New, so readers
	// need no lock and no caller can displace it mid-run (issue #3522).
	term *terminate.Registry
	// cfForNum defaults to returning cf when Config.CodeForgeForIssue is nil
	// (issue #1734).
	cfForNum func(num string) forge.CodeForge
	// clock defaults to dispatch.RealClock() when Config.Clock is unset.
	clock dispatch.Clock
	// settledMu guards the per-issue maps below (issue #3627): dispatchWave settles
	// different issues concurrently, though each issue's own latch and flush
	// run on one goroutine.
	settledMu sync.Mutex
	// settledLatch holds each in-flight issue's most recent terminal
	// transition, last write wins; see transitionState (issue #3627).
	settledLatch map[string]settledLatch
	// prLatch holds the PR each in-flight issue's gate is working, apart from
	// settledLatch because transitionState overwrites that entry and the PR
	// must survive it. Guarded by settledMu.
	prLatch map[string]string
	// reportKey is the key num's report records go out under when it is not
	// dispatchkey.Issue(num). Guarded by settledMu.
	reportKey map[string]dispatchkey.Key
}

// Registry returns the termination registry this Settle's checkpoints read:
// the one New minted.
func (s *Settle) Registry() *terminate.Registry { return s.term }

// terminated reports whether num was marked terminated at generation gen
// specifically (issue #743), not whether some other generation of num was.
func (s *Settle) terminated(num string, gen uint64) bool {
	return s.term.Marked(num, gen)
}

var _ Settler = (*Settle)(nil)
var _ MergeSettler = (*Settle)(nil)

// New constructs a Settle, reading pr and landing from cfg.Capabilities rather
// than re-deriving them here (issue #2945).
func New(cfg Config, it forge.IssueTracker, cf forge.CodeForge) *Settle {
	if cfg.Unclaimed {
		cfg.MaxFixAttempts = 0
	}
	pr := cfg.Capabilities.PRForge
	landing := cfg.Capabilities.LandingRecorder
	landingPass := cfg.Capabilities.LandingPassRecorder
	ciRun := cfg.Capabilities.CIRunReporter
	cfForNum := cfg.CodeForgeForIssue
	if cfForNum == nil {
		cfForNum = func(string) forge.CodeForge { return cf }
	}
	if cfg.Progress == nil {
		cfg.Progress = StdoutProgress{}
	}
	clock := cfg.Clock
	if clock.Sleep == nil {
		clock = dispatch.RealClock()
	}
	s := &Settle{cfg: cfg, it: it, cf: cf, pr: pr, landing: landing, landingPass: landingPass, ciRun: ciRun, cfForNum: cfForNum, clock: clock, term: terminate.NewRegistry()}
	if pr != nil {
		s.lander = prLander{s}
	} else {
		s.lander = pushOnlyLander{s}
	}
	return s
}

// agentBranch is num's agent branch under the configured BRANCH_PREFIX.
func (s *Settle) agentBranch(num string) string {
	return forge.AgentBranchName(s.cfg.BranchPrefix, num)
}

// rebase rebases ref through cf, pinned to num's agent branch when cf resolves
// PRHeadRebaser (issue #6961).
func (s *Settle) rebase(cf forge.CodeForge, num, ref string) error {
	return forge.RebasePinned(cf, ref, s.agentBranch(num))
}
