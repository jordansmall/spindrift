// Package waves owns the launcher's dependency-wave engine: the blocker
// graph (edge building, cycle detection, readiness), the drain dispatch
// engine (concurrent wave fan-out, MAX_JOBS cap), and the declared-##
// Touches overlap gate. Plan is pure — given a batch of issues and their
// blocker edges, it validates them (or reports a cycle) with no side
// effects. Run executes a Plan: the claim/dispatch/settle loop,
// MAX_PARALLEL/MAX_JOBS concurrency, and the Touches overlap check, all in
// a single selection-pass-then-exit wave (ADR 0019).
package waves

import (
	"errors"
	"fmt"

	"spindrift.dev/launcher/internal/terminate"
)

// ErrOpenNoneDispatchable is returned by Run when ModeDrain selects zero
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
}

// Input is what a caller supplies to NewPlan: the batch to dispatch and the
// blocker edges among them (child -> blockers), already resolved by the
// caller — Plan itself makes no Forge calls.
type Input struct {
	Origin  Origin
	Issues  []Issue
	Edges   map[string][]string
	Sources Sources
}

// Plan is the pure result of validating a batch of issues for dispatch:
// which Mode (always ModeDrain), in what order, and against which blocker
// edges.
type Plan struct {
	Mode    Mode
	Origin  Origin
	Issues  []Issue
	Edges   map[string][]string
	Sources Sources
}

// Config carries the subset of launcher config the wave engine needs.
type Config struct {
	MaxParallel     int
	MaxJobs         int
	OverlapGate     string
	Label           string
	InProgressLabel string
	CompleteLabel   string
	FailedLabel     string

	// IgnoreBlockers skips blocker-edge gating entirely — the research
	// dispatch kind (ADR 0022): research lands no code, so it is never held
	// on an unmerged dependency, a batch sibling's Failed label never
	// cascades to it, and the OriginClaimed single-issue path never writes
	// logs/blocked.txt. Wave caps (MaxParallel/MaxJobs) and dispatch order
	// still apply unchanged.
	IgnoreBlockers bool

	// Verb is the CLI subcommand name a selective wave's rerun hint tells the
	// operator to re-invoke (e.g. "spindrift research --yes <nums>" instead
	// of "spindrift dispatch --yes <nums>"). Empty defaults to "dispatch",
	// matching every pre-existing (kind-unaware) construction site.
	Verb string

	// Terminated is checked by RunContinuous's per-issue goroutine after a
	// Box exits, so an issue the operator Terminated (ADR 0024, issue #649)
	// while it was running is neither transitioned to Failed nor handed to
	// Settle — Terminate already reclaimed it. Nil (every headless dispatch
	// path) means "never terminated"; only the Console wires a Registry.
	Terminated *terminate.Registry
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
		if node, cycle := detectCycle(in.Edges, issueNums(in.Issues)); cycle {
			return Plan{}, fmt.Errorf("ERROR: dependency cycle detected (issue #%s is in the cycle)", node)
		}
	}
	return Plan{Mode: ModeDrain, Origin: in.Origin, Issues: in.Issues, Edges: in.Edges, Sources: in.Sources}, nil
}

// issueNums returns the number strings from a slice of issues.
func issueNums(issues []Issue) []string {
	nums := make([]string, len(issues))
	for i, iss := range issues {
		nums[i] = iss.Number
	}
	return nums
}
