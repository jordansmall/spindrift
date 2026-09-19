// Package waves owns the launcher's dependency-wave engine: the blocker
// graph, the drain dispatch engine, and the declared-Touches overlap gate.
// Plan is pure; Dispatch runs a validated Plan as one
// selection-pass-then-exit wave (ADR 0019). staleDrain*/StaleDrain* names an
// unrelated second drain (#2678), the CONTINUOUS_DISPATCH stale-image pause.
package waves

import (
	"errors"
	"fmt"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// ErrOpenNoneDispatchable reports that ModeDrain selected zero issues, so the
// caller stops instead of hot-looping. main.go maps it to exit code 3.
var ErrOpenNoneDispatchable = errors.New("open issues exist but none are dispatchable")

// Origin records how a Plan's issue batch was resolved.
type Origin int

const (
	// OriginDiscovered is a batch resolved by a Dispatchable-label query.
	OriginDiscovered Origin = iota
	// OriginClaimed is a single issue the caller already claimed: the workflow
	// swapped its label to in-progress, and ISSUE_NUMBER names it directly.
	OriginClaimed
	// OriginSelective is an operator-supplied issue list (`dispatch <nums>`)
	// that bypasses the label/barrier gates.
	OriginSelective
)

// Mode is the dispatch strategy a Plan selects. ModeDrain is the only value,
// since every Origin selects it (ADR 0019 / #524); it stays a named type so
// regression tests can pin that decision down.
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
	// launched under (#743). Every headless dispatch path leaves it zero, which
	// Registry.Marked never matches. Console sets it so the eventual Settle
	// checks this incarnation, not whichever one last held the issue number.
	Generation uint64

	// Priority is the issue's agent-priority-{critical,high,low} tier (ADR
	// 0040). Only forge.SortByPriority reads it.
	Priority forge.Priority
}

// Batch is discovery's sealed result: the candidate issues plus the blocker
// graph resolved for them, kept together so the fields do not drift apart.
type Batch struct {
	Issues  []Issue
	Edges   map[string][]string
	Sources Sources

	// Failed names issues whose own NewReadiness/DepsOf call errored (#752,
	// #1103). Such a transient tracker hiccup looks identical to a confirmed
	// zero-blocker issue in Edges alone, so drainMaxJobs holds these issues for
	// retry instead of reading the missing Edges entry as ready.
	Failed map[string]bool
}

// Input is what a caller supplies to NewPlan: an Origin plus an
// already-resolved Batch. Plan itself makes no Forge calls.
type Input struct {
	Origin Origin
	Batch
}

// NewInput is the sole production path for a dispatch Input, so a new Batch or
// Input field reaches every call site instead of drifting across literals.
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

// Plan is the pure result of validating a batch of issues for dispatch: the
// Mode (always ModeDrain), the order, the Origin, and the Batch it applies to.
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

	// IgnoreBlockers skips blocker-edge gating for the research dispatch kind
	// (ADR 0022): research lands no code, so it is never held on an unmerged
	// dependency, a sibling's Failed label never cascades to it, and
	// OriginClaimed never writes .spindrift/logs/blocked.txt. Caps still apply.
	IgnoreBlockers bool

	// Verb is the CLI subcommand a selective wave's rerun hint tells the operator
	// to re-invoke (e.g. "spindrift research --yes <nums>"). Empty means dispatch.
	Verb string

	// SeedScopeOf resolves a dependent issue number to the SeedScope its blocker
	// gate is checked against: the seed branch a blocker's landed work must have
	// reached before the dependent is ready. Set only under CODE_FORGE=local;
	// nil elsewhere, where a blocker is judged solely by its PR/issue state.
	SeedScopeOf func(num string) forge.SeedScope

	// pollInterval overrides RunContinuous's background refill-poll cadence
	// (#1637). Every production site leaves it zero, which means
	// defaultPollInterval.
	pollInterval time.Duration

	// now overrides RunContinuous's clock, which #2678's stale-drain report reads
	// to accumulate free-slot-seconds. Nil means time.Now; only same-package
	// tests inject a sequence that makes freeSlotSecs exactly assertable.
	now func() time.Time

	// Policy is retry.Policy's transient-retry tuning (#2928). Only Max and Unit
	// are read here: waves' re-discover retry has never held or jittered, so
	// Policy.Jitter is deliberately unused in this package.
	Policy retry.Policy

	// Clock is the injectable sleep seam a rate-limited re-discover retry backs
	// off through. When unset, it defaults to retry.RealClock().
	Clock retry.Clock
}

// NewPlan decides how in.Issues should be dispatched. Every Origin selects
// ModeDrain (ADR 0019 / #524): one selection pass gates each issue, the
// selected set dispatches as a single wave, and the invocation exits.
// MAX_JOBS=0 means an uncapped drain batch. A dependency cycle in in.Edges is
// reported as an error here, the one place that decision is made.
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
