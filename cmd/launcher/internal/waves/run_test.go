package waves

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// TestRun_Selective_NoEdges_IgnoresTouchOverlap verifies that OriginSelective
// with no blocker edges dispatches immediately even when the batch's
// declared touches overlap an in-progress issue's — matching the original
// selectiveListDispatch behavior, which never consulted the overlap gate for
// its mode decision. Run must not fall into the wave-retry engine (and its
// deadlock timer) for an operator-specified list with no dependency edges.
func TestRun_Selective_NoEdges_IgnoresTouchOverlap(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"
	c.DepsPollSecs = 1
	c.DepsWaitSecs = 1

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{c.Label},
	})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{c.InProgressLabel},
	})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc)
	plan, err := NewPlan(c, Input{Origin: OriginSelective, Issues: []Issue{{Number: "10", Title: "candidate"}}})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if err := Run(c, fc, dir, f, s, plan); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("selective dispatch with no edges must ignore touch overlap and dispatch immediately; got %d run calls", len(fr.RunCalls))
	}
}

// TestRun_Discovered_NoEdges_TouchOverlapDefersToWaves verifies that
// OriginDiscovered (the run() queue-drain path) still routes a no-edges
// batch through the wave-retry engine when its declared touches overlap an
// in-progress issue — the deadlock timer proves the overlap gate is active
// (matching the original run()'s batchHasTouchOverlap-forced wave mode).
func TestRun_Discovered_NoEdges_TouchOverlapDefersToWaves(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"
	c.DepsPollSecs = 1
	c.DepsWaitSecs = 1

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{c.Label},
	})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{c.InProgressLabel},
	})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc)
	plan, err := NewPlan(c, Input{Origin: OriginDiscovered, Issues: []Issue{{Number: "10", Title: "candidate"}}})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if err := Run(c, fc, dir, f, s, plan); err == nil {
		t.Fatal("Run must deadlock while #20 stays in-progress with an overlapping touch-set")
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("issue 10 must not be dispatched while its touches overlap in-progress #20; got %d run calls", len(fr.RunCalls))
	}
}
