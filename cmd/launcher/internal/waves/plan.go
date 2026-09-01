// Package waves owns the launcher's dependency-wave engine: the blocker
// graph, the drain dispatch engine, and the declared-Touches overlap gate.
// Plan is pure — it validates a batch of issues and their blocker edges (or
// reports a cycle) with no side effects. Dispatch runs a validated Plan: the
// claim/dispatch/settle loop, MAX_PARALLEL/MAX_JOBS concurrency, and the
// overlap check, in a single selection-pass-then-exit wave (ADR 0019).
//
// Beware a second, unrelated "drain": the CONTINUOUS_DISPATCH stale-image
// pause, named staleDrain*/StaleDrain* throughout to keep it distinct from
// the MAX_JOBS refill drain, which every identifier here calls plain
// "drain".
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

// Origin records how a Plan's issue batch was resolved.
type Origin int

const (
	// OriginDiscovered is a batch resolved by a Dispatchable-label query.
	OriginDiscovered Origin = iota
	// OriginClaimed is a single issue the caller already claimed — the
	// workflow swapped its label to in-progress before the launcher started.
	OriginClaimed
	// OriginSelective is an operator-supplied issue list that bypasses the
	// label/barrier gates.
	OriginSelective
)

// Mode is the dispatch strategy a Plan selects. ModeDrain is the only value
// — every Origin selects it (ADR 0019) — kept as a named type so Plan still
// documents the decision NewPlan makes and tests can pin it down.
type Mode int

const (
	// ModeDrain selects up to Config.MaxJobs currently-unblocked issues.
	ModeDrain Mode = iota
)

// Issue is the minimal issue identity the wave engine dispatches.
type Issue struct {
	Number string
	Title  string

	// Generation is the terminate.Registry generation this claim launched
	// under. Headless paths leave it zero, which Registry.Marked never
	// matches; Console sets it so the eventual Settle checks termination
	// against its own incarnation, not whichever last held the number.
	Generation uint64

	// Priority is the issue's agent-priority-{critical,high,low} tier (ADR
	// 0040). forge.SortByPriority is the only reader.
	Priority forge.Priority
}

// Batch is discovery's sealed result: the candidate issues plus the blocker
// graph resolved for them. Input and Plan each embed one so the fields
// travel together instead of drifting apart as separate parameters.
type Batch struct {
	Issues  []Issue
	Edges   map[string][]string
	Sources Sources

	// Failed names issues whose NewReadiness/DepsOf call errored — a
	// transient tracker hiccup that, in Edges alone, looks identical to a
	// confirmed zero-blocker issue. Carried through to Plan so drainMaxJobs
	// can hold these for retry instead of reading the missing Edges entry as
	// "ready".
	Failed map[string]bool
}

// Input is what a caller supplies to NewPlan: an Origin plus the Batch to
// dispatch, already resolved by the caller — Plan itself makes no Forge
// calls.
type Input struct {
	Origin Origin
	Batch
}

// NewInput is the sole production path for a dispatch Input, so a new
// Batch/Input field reaches every production call site by construction
// rather than drifting across hand-written literals.
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

// Plan is the pure result of validating a batch of issues for dispatch.
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
	// .spindrift/logs/blocked.txt. Wave caps and dispatch order still apply.
	IgnoreBlockers bool

	// Verb is the CLI subcommand name a selective wave's rerun hint tells the
	// operator to re-invoke (e.g. "spindrift research --yes <nums>"). Empty
	// defaults to "dispatch".
	Verb string

	// SeedScopeOf resolves a dependent issue number to the opaque SeedScope
	// its blocker gate is checked against — the seed branch a blocker's landed
	// work must have reached before the dependent is ready. Set only under
	// CODE_FORGE=local; nil elsewhere, where the seed-branch containment gate
	// never fires and a blocker is judged solely by its PR/issue state.
	SeedScopeOf func(num string) forge.SeedScope

	// pollInterval overrides RunContinuous's background refill-poll cadence;
	// zero means defaultPollInterval. Only same-package tests shrink it.
	pollInterval time.Duration

	// now overrides RunContinuous's clock, which the stale-drain report reads
	// to accumulate free-slot-seconds; nil means time.Now. Only same-package
	// tests inject a deterministic sequence.
	now func() time.Time

	// Policy is retry.Policy's transient-retry tuning. Only Max and Unit are
	// read here — waves' re-discover retry has never held or jittered, unlike
	// dispatch's rate-limit hold and settle's rebase-push backoff, so
	// Policy.Jitter is deliberately unused in this package.
	Policy retry.Policy

	// Clock is the sleep seam a rate-limited re-discover retry's backoff
	// sleeps through — defaults to retry.RealClock() when unset.
	Clock retry.Clock
}

// NewPlan decides how in.Issues should be dispatched. Every origin selects
// ModeDrain (ADR 0019): one selection pass gates each issue, the selected set
// dispatches as a single wave, and the invocation exits. MAX_JOBS=0 means an
// uncapped drain batch. A dependency cycle in in.Edges is reported as an
// error rather than a Plan — this is the single place that decision is made;
// run, selective dispatch, and preview all consume its result.
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
