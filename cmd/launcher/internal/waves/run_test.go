package waves

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// TestRun_Selective_NoEdges_TouchOverlapDefersThenExits verifies that,
// post-#524, OriginSelective shares drainMaxJobs with the queue path: a
// declared-touches overlap defers the candidate and Run exits with
// ErrOpenNoneDispatchable instead of dispatching immediately — the old
// selective-only overlap bypass existed solely to gate entry into the
// deleted multi-wave loop and has no reason to survive it.
func TestRun_Selective_NoEdges_TouchOverlapDefersThenExits(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"

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
	s := newSettle(fc, fc)
	plan, err := NewPlan(c, Input{Origin: OriginSelective, Issues: []Issue{{Number: "10", Title: "candidate"}}})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if err := run(c, fc, fc, dir, f, s, plan); !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("Run: got %v, want ErrOpenNoneDispatchable", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("issue 10 must not be dispatched while its touches overlap in-progress #20; got %d run calls", len(fr.RunCalls))
	}
}

// TestRun_Discovered_MaxJobsZero_DependencyEdge_DispatchesOnlyUnblockedWave
// is the regression test for #477: with MAX_JOBS=0 (uncapped drain) and a
// dependency edge, one Run invocation dispatches only the currently-unblocked
// issue; the dependent is neither claimed (still on the dispatch label, not
// InProgress) nor dispatched — it waits for a fresh invocation, which
// re-evaluates the image, rather than running from the blocker's frozen
// image inside the same process.
func TestRun_Discovered_MaxJobsZero_DependencyEdge_DispatchesOnlyUnblockedWave(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"}) // #2's blocker, not yet complete

	fr := runner.NewFake()

	edges := map[string][]string{"2": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	plan, err := NewPlan(c, Input{
		Origin: OriginDiscovered,
		Issues: []Issue{{Number: "1", Title: "unblocked"}, {Number: "2", Title: "dependent"}},
		Edges:  edges,
	})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if plan.Mode != ModeDrain {
		t.Fatalf("Mode = %v, want ModeDrain", plan.Mode)
	}
	if err := run(c, fc, fc, dir, f, s, plan); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("RunCalls: got %v, want exactly issue 1", fr.RunCalls)
	}

	iss2, err := fc.Issue("2")
	if err != nil {
		t.Fatalf("Issue(2): %v", err)
	}
	if !containsLabel(iss2.Labels, c.Label) {
		t.Errorf("issue 2 must stay on the dispatch label for the next invocation; labels=%v", iss2.Labels)
	}
	if containsLabel(iss2.Labels, c.InProgressLabel) {
		t.Errorf("issue 2 must not be claimed while its blocker is unmet; labels=%v", iss2.Labels)
	}
}

// TestRun_Discovered_NoEdges_TouchOverlapDefersThenExits verifies that
// OriginDiscovered (the run() queue-drain path) defers a no-edges candidate
// whose declared touches overlap an in-progress issue and exits immediately
// with ErrOpenNoneDispatchable — no in-process wait, no deadlock timer
// (ADR 0019: the queue path is drain-only; a held issue is picked up by the
// next invocation instead).
func TestRun_Discovered_NoEdges_TouchOverlapDefersThenExits(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"

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
	s := newSettle(fc, fc)
	plan, err := NewPlan(c, Input{Origin: OriginDiscovered, Issues: []Issue{{Number: "10", Title: "candidate"}}})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	err = run(c, fc, fc, dir, f, s, plan)
	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("Run: got %v, want ErrOpenNoneDispatchable", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("issue 10 must not be dispatched while its touches overlap in-progress #20; got %d run calls", len(fr.RunCalls))
	}

	iss10, err := fc.Issue("10")
	if err != nil {
		t.Fatalf("Issue(10): %v", err)
	}
	if !containsLabel(iss10.Labels, c.Label) {
		t.Errorf("issue 10 must stay on the dispatch label for the next invocation; labels=%v", iss10.Labels)
	}
}

// TestRun_Discovered_NoEdges_TouchOverlapDispatchesOnNextInvocation verifies
// that once the colliding in-progress issue leaves InProgress, a fresh Run
// invocation dispatches the previously-deferred candidate — the two-call
// sequence a real driving loop (dogfood.sh, CI, or an operator re-running
// dispatch) performs across process invocations.
func TestRun_Discovered_NoEdges_TouchOverlapDispatchesOnNextInvocation(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"

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
	s := newSettle(fc, fc)
	plan, err := NewPlan(c, Input{Origin: OriginDiscovered, Issues: []Issue{{Number: "10", Title: "candidate"}}})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}

	// First invocation: the collider is still in-progress, so #10 defers.
	if err := run(c, fc, fc, dir, f, s, plan); !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("first Run: got %v, want ErrOpenNoneDispatchable", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("first Run: got %d run calls, want 0", len(fr.RunCalls))
	}

	// The collider completes; a fresh invocation now dispatches #10.
	fc.TransitionState("20", forge.InProgress, forge.Complete)
	if err := run(c, fc, fc, dir, f, s, plan); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("second Run: got %d run calls, want 1", len(fr.RunCalls))
	}
}
