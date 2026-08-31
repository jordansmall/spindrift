// Package waves owns the launcher's dependency-wave engine: the blocker
// graph (edge building, cycle detection, readiness), the drain dispatch
// engine (concurrent wave fan-out, MAX_JOBS cap), and the declared-##
// Touches overlap gate. Plan is pure — given a batch of issues and their
// blocker edges, it validates them (or reports a cycle) with no side
// effects. Dispatch validates a Plan and runs it: the claim/dispatch/settle
// loop, MAX_PARALLEL/MAX_JOBS concurrency, and the Touches overlap check,
// all in a single selection-pass-then-exit wave (ADR 0019).
//
// This package also tracks a second, unrelated "drain" (#2678): a
// CONTINUOUS_DISPATCH stale-image pause, reported as a StaleDrainReport
// (stale_drain_report.go, stale_drain_tracker.go) and named staleDrain*/StaleDrain*
// throughout to keep it distinct from the MAX_JOBS refill drain above,
// which every identifier here keeps naming plain "drain".
package waves

import (
	"errors"
	"fmt"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// ErrOpenNoneDispatchable is returned by Dispatch when ModeDrain selects zero
// issues (all remaining candidates are blocked or deferred) — the caller
// should stop with a triage message rather than hot-looping. Exported so
// main.go can map it to the launcher's exit code 3.
var ErrOpenNoneDispatchable = errors.New("open issues exist but none are dispatchable")

// Origin records how a Plan's issue batch was resolved, replacing the
// former issueNumber != "" sentinel that was checked independently at each
// call site (discovery, run, drain, preview).
type Origin int

const (
	// OriginDiscovered is a batch resolved by a Dispatchable-label query.
	OriginDiscovered Origin = iota
	// OriginClaimed is a single issue the caller already claimed (the
	// workflow swapped its label to in-progress before the launcher
	// started; ISSUE_NUMBER names it directly).
	OriginClaimed
	// OriginSelective is an operator-supplied issue list (`dispatch
	// <nums>`) that bypasses the label/barrier gates.
	OriginSelective
)

// Mode is the dispatch strategy a Plan selects. ModeDrain is the only value
// — every Origin selects it (ADR 0019 / #524) — kept as a named type rather
// than inlined so Plan continues to document the decision NewPlan makes and
// regression tests can pin it down.
type Mode int

const (
	// ModeDrain selects up to Config.MaxJobs currently-unblocked issues and
	// dispatches exactly that set once.
	ModeDrain Mode = iota
)

// Issue is the minimal issue identity the wave engine dispatches.
type Issue struct {
	Number string
	Title  string

	// Generation is the terminate.Registry generation this issue's claim was
	// launched under (terminate.Registry.Begin, issue #743) — nil-Registry
	// callers (every headless dispatch path) leave it at its zero value,
	// which Registry.Marked never matches. Console's discover closure sets
	// it on every freshly claimed issue so the Settle call this dispatch
	// eventually makes checks termination against its own incarnation, not
	// whichever one last happened to hold the issue number.
	Generation uint64

	// Priority is the issue's own agent-priority-{critical,high,low} tier
	// (ADR 0040), carried through from forge.Issue.Priority by every caller
	// that builds an Issue from a tracker query. forge.SortByPriority is the
	// only thing that reads it.
	Priority forge.Priority
}

// Batch is discovery's sealed result: the candidate issues plus the blocker
// graph resolved for them. A Discoverer returns one; Input and Plan each
// embed one so the four fields travel together instead of drifting apart
// as separate parameters.
type Batch struct {
	Issues  []Issue
	Edges   map[string][]string
	Sources Sources

	// Failed names issues whose own NewReadiness/DepsOf call errored (#752,
	// #1103) — a transient tracker hiccup that looks identical to a
	// confirmed zero-blocker issue in Edges alone. NewPlan carries it
	// through to Plan unchanged so drainMaxJobs can hold these issues for
	// retry instead of treating the missing Edges entry as "ready".
	Failed map[string]bool
}

// Input is what a caller supplies to NewPlan: an Origin plus the Batch to
// dispatch, already resolved by the caller — Plan itself makes no Forge
// calls.
type Input struct {
	Origin Origin
	Batch
}

// NewInput is the sole production path for a dispatch Input: it assembles
// the Batch from a resolved Readiness and its issues so a new Batch/Input
// field reaches every production call site by construction rather than
// drifting across hand-written Input{Batch: Batch{...}} literals.
func NewInput(origin Origin, readiness Readiness, issues []Issue) Input {
	return Input{
		Origin: origin,
		Batch: Batch{
			Issues:  issues,
			Edges:   readiness.Edges,
			Sources: readiness.Sources,
			Failed:  readiness.Failed,
		},
	}
}

// Plan is the pure result of validating a batch of issues for dispatch:
// which Mode (always ModeDrain), in what order, tagged with the dispatch's
// Origin, and against which Batch — the issues plus their resolved blocker
// graph.
type Plan struct {
	Mode   Mode
	Origin Origin
	Batch
}

// Config carries the subset of launcher config the wave engine needs.
type Config struct {
	MaxParallel   int
	MaxJobs       int
	OverlapGate   string
	CompleteLabel string
	FailedLabel   string

	// IgnoreBlockers skips blocker-edge gating entirely — the research
	// dispatch kind (ADR 0022): research lands no code, so it is never held
	// on an unmerged dependency, a batch sibling's Failed label never
	// cascades to it, and the OriginClaimed single-issue path never writes
	// .spindrift/logs/blocked.txt. Wave caps (MaxParallel/MaxJobs) and dispatch order
	// still apply unchanged.
	IgnoreBlockers bool

	// Verb is the CLI subcommand name a selective wave's rerun hint tells the
	// operator to re-invoke (e.g. "spindrift research --yes <nums>" instead
	// of "spindrift dispatch --yes <nums>"). Empty defaults to "dispatch",
	// matching every pre-existing (kind-unaware) construction site.
	Verb string

	// SeedScopeOf resolves a dependent issue number to the opaque SeedScope
	// its blocker gate is checked against — the seed branch a blocker's landed
	// work must have reached before the dependent is ready. Set only under
	// CODE_FORGE=local; nil for every other forge, where the seed-branch
	// containment gate never fires and a blocker is judged solely by its
	// PR/issue state.
	SeedScopeOf func(num string) forge.SeedScope

	// pollInterval overrides RunContinuous's background refill-poll cadence
	// (issue #1637) — zero (every production construction site) means "use
	// defaultPollInterval"; only same-package tests shrink it so the poll
	// test doesn't wait out a real-time interval.
	pollInterval time.Duration

	// now overrides RunContinuous's clock (issue #2678's stale-drain report
	// reads it to timestamp/accumulate free-slot-seconds) — nil (every
	// production construction site) means time.Now; only same-package tests
	// (continuous_test.go) inject a deterministic sequence so the
	// accumulated freeSlotSecs value is exactly assertable instead of only
	// >=0-checkable.
	now func() time.Time

	// TransientRetryMax caps continuous re-discover retries against a
	// rate-limited forge (forge.ErrRateLimit, issue #2866) — the same
	// TRANSIENT_RETRY_MAX knob dispatch.Config's own exit-retry loop honors,
	// reused here rather than inventing a second retry-count knob.
	TransientRetryMax int

	// TransientBackoffSecs is the linear-backoff unit a rate-limited
	// re-discover retry sleeps between attempts (issue #2866) — the same
	// TRANSIENT_BACKOFF_SECS knob dispatch.Config's own exit-retry loop
	// honors.
	TransientBackoffSecs int

	// Clock is the injectable sleep seam a rate-limited re-discover retry's
	// backoff sleeps through — defaults to retry.RealClock() when unset (its
	// Sleep field left nil).
	Clock retry.Clock
}

// NewPlan decides how in.Issues should be dispatched. Every origin —
// OriginDiscovered, OriginClaimed, and (per ADR 0019 / #524) OriginSelective
// — always selects ModeDrain: one selection pass gates each issue, the
// selected set dispatches as a single wave, and the invocation exits.
// MAX_JOBS=0 means an uncapped drain batch, not the old in-process wave
// loop. A dependency cycle in in.Edges is reported as an error rather than a
// Plan — this is the single place that decision is made; run, selective
// dispatch, and preview all consume its result instead of repeating it.
func NewPlan(cfg Config, in Input) (Plan, error) {
	if len(in.Edges) > 0 {
		if node, cycle := detectCycle(in.Edges, forge.Numbers(in.Issues, func(i Issue) string { return i.Number })); cycle {
			return Plan{}, fmt.Errorf("ERROR: dependency cycle detected (issue #%s is in the cycle)", node)
		}
	}
	if in.Origin != OriginSelective {
		forge.SortByPriority(in.Issues, func(i Issue) forge.Priority { return i.Priority })
	}
	return Plan{Mode: ModeDrain, Origin: in.Origin, Batch: in.Batch}, nil
}
