// Package waves owns the launcher's dependency-wave engine: the blocker
// graph (edge building, cycle detection, readiness), the wave/drain
// dispatch engine (concurrent wave fan-out, MAX_JOBS drain loop, deadlock
// timer), and the declared-## Touches overlap gate. Plan is pure — given a
// batch of issues and their blocker edges, it decides drain vs. wave mode
// (or reports a cycle) with no side effects. Run executes a Plan: the
// claim/dispatch/settle loop, MAX_PARALLEL/MAX_JOBS concurrency, the
// deadlock timer, and the Touches overlap check.
package waves

import (
	"errors"
	"fmt"
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

// Mode is the dispatch strategy a Plan selects.
type Mode int

const (
	// ModeWaves dispatches issues in dependency order: each wave fires the
	// currently-unblocked set, rechecking blocked issues (and the Touches
	// overlap gate) until the batch drains or the deadlock timer fires.
	ModeWaves Mode = iota
	// ModeDrain selects up to Config.MaxJobs currently-unblocked issues and
	// dispatches exactly that set once.
	ModeDrain
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
	Origin Origin
	Issues []Issue
	Edges  map[string][]string
}

// Plan is the pure result of deciding how a batch of issues should be
// dispatched: which Mode, in what order, and against which blocker edges.
type Plan struct {
	Mode   Mode
	Origin Origin
	Issues []Issue
	Edges  map[string][]string
}

// Config carries the subset of launcher config the wave engine needs.
type Config struct {
	MaxParallel     int
	MaxJobs         int
	DepsPollSecs    int
	DepsWaitSecs    int
	OverlapGate     string
	Label           string
	InProgressLabel string
	CompleteLabel   string
	FailedLabel     string
}

// NewPlan decides how in.Issues should be dispatched. OriginDiscovered and
// OriginClaimed (the queue path) always select ModeDrain per ADR 0019:
// MAX_JOBS=0 means an uncapped drain batch, not the old in-process wave
// loop. OriginSelective keeps the legacy cfg.MaxJobs-gated choice — the
// hand-picked list still uses ModeWaves until #524 reroutes it. A dependency
// cycle in in.Edges is reported as an error rather than a Plan — this is the
// single place that decision is made; run, selective dispatch, and preview
// all consume its result instead of repeating it.
func NewPlan(cfg Config, in Input) (Plan, error) {
	if len(in.Edges) > 0 {
		if node, cycle := detectCycle(in.Edges, issueNums(in.Issues)); cycle {
			return Plan{}, fmt.Errorf("ERROR: dependency cycle detected (issue #%s is in the cycle)", node)
		}
	}
	mode := ModeDrain
	if in.Origin == OriginSelective {
		mode = ModeWaves
		if cfg.MaxJobs > 0 {
			mode = ModeDrain
		}
	}
	return Plan{Mode: mode, Origin: in.Origin, Issues: in.Issues, Edges: in.Edges}, nil
}

// issueNums returns the number strings from a slice of issues.
func issueNums(issues []Issue) []string {
	nums := make([]string, len(issues))
	for i, iss := range issues {
		nums[i] = iss.Number
	}
	return nums
}
