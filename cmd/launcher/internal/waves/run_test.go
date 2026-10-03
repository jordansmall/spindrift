package waves

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/testutil"
)

// Regression test for #524: OriginSelective shares drainMaxJobs with the queue
// path, so a declared-touches overlap defers the candidate and Run exits with
// ErrOpenNoneDispatchable instead of dispatching. The old selective-only
// overlap bypass existed only to gate entry into the deleted multi-wave loop.
func TestRun_Selective_NoEdges_TouchOverlapDefersThenExits(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{label},
	})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{testInProgressLabel},
	})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	plan, err := NewPlan(c, Input{Origin: OriginSelective, Batch: Batch{Issues: []Issue{{Number: "10", Title: "candidate"}}}})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if err := run(c, nil, fc, fc, dir, f, s, plan, claimer); !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("Run: got %v, want ErrOpenNoneDispatchable", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("issue 10 must not be dispatched while its touches overlap in-progress #20; got %d run calls", len(fr.RunCalls))
	}
}

// Regression test for #477: with MAX_JOBS=0 (uncapped drain) and a dependency
// edge, one Run invocation dispatches only the currently-unblocked issue. The
// dependent is neither claimed nor dispatched; it waits for a fresh invocation
// that re-evaluates the image instead of running from the blocker's frozen one.
func TestRun_Discovered_MaxJobsZero_DependencyEdge_DispatchesOnlyUnblockedWave(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"}) // #2's blocker, not yet complete

	fr := runner.NewFake()

	edges := map[string][]string{"2": {"3"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	plan, err := NewPlan(c, Input{
		Origin: OriginDiscovered,
		Batch:  Batch{Issues: []Issue{{Number: "1", Title: "unblocked"}, {Number: "2", Title: "dependent"}}, Edges: edges},
	})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	if plan.Mode != ModeDrain {
		t.Fatalf("Mode = %v, want ModeDrain", plan.Mode)
	}
	if err := run(c, nil, fc, fc, dir, f, s, plan, claimer); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("RunCalls: got %v, want exactly issue 1", fr.RunCalls)
	}

	iss2, err := fc.Issue("2")
	if err != nil {
		t.Fatalf("Issue(2): %v", err)
	}
	if !containsLabel(iss2.Labels, label) {
		t.Errorf("issue 2 must stay on the dispatch label for the next invocation; labels=%v", iss2.Labels)
	}
	if containsLabel(iss2.Labels, testInProgressLabel) {
		t.Errorf("issue 2 must not be claimed while its blocker is unmet; labels=%v", iss2.Labels)
	}
}

// ADR 0019 makes the queue path drain-only, so OriginDiscovered defers a
// no-edges candidate whose declared touches overlap an in-progress issue and
// exits with ErrOpenNoneDispatchable. No in-process wait, no deadlock timer;
// the next invocation picks up the held issue.
func TestRun_Discovered_NoEdges_TouchOverlapDefersThenExits(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{label},
	})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{testInProgressLabel},
	})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	plan, err := NewPlan(c, Input{Origin: OriginDiscovered, Batch: Batch{Issues: []Issue{{Number: "10", Title: "candidate"}}}})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	err = run(c, nil, fc, fc, dir, f, s, plan, claimer)
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
	if !containsLabel(iss10.Labels, label) {
		t.Errorf("issue 10 must stay on the dispatch label for the next invocation; labels=%v", iss10.Labels)
	}
}

// Once the colliding in-progress issue leaves InProgress, a fresh Run
// invocation dispatches the previously-deferred candidate. The two calls are
// the sequence a real driving loop (the daemon, CI, or an operator re-running
// dispatch) performs across process invocations.
func TestRun_Discovered_NoEdges_TouchOverlapDispatchesOnNextInvocation(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{label},
	})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{testInProgressLabel},
	})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	plan, err := NewPlan(c, Input{Origin: OriginDiscovered, Batch: Batch{Issues: []Issue{{Number: "10", Title: "candidate"}}}})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}

	if err := run(c, nil, fc, fc, dir, f, s, plan, claimer); !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("first Run: got %v, want ErrOpenNoneDispatchable", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("first Run: got %d run calls, want 0", len(fr.RunCalls))
	}

	fc.TransitionState("20", forge.InProgress, forge.Complete)
	if err := run(c, nil, fc, fc, dir, f, s, plan, claimer); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("second Run: got %d run calls, want 1", len(fr.RunCalls))
	}
}

// ADR 0019, folded from tests/run-dependency-waves.bats: one invocation
// dispatches only the blocker and carries it through merge during settle; the
// dependent waits for a fresh invocation, which then sees the blocker's merged
// PR and dispatches it.
func TestRun_Discovered_DependencyEdge_DispatchesDependentOnNextInvocation(t *testing.T) {
	const prURL = "https://github.com/owner/repo/pull/1"

	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Body: "depends on #1", Labels: []string{label}})
	fc.SetPR("agent/issue-1", forge.PR{URL: prURL})
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error {
		if box.Output != nil && box.Issue == "1" {
			fmt.Fprintf(box.Output, "SPINDRIFT_OUTCOME issue=1 landing=%s status=ready note=ok nonce=%s\n",
				prURL, box.Env["RUN_NONCE"])
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	edges := map[string][]string{"2": {"1"}}

	plan, err := NewPlan(c, Input{
		Origin: OriginDiscovered,
		Batch:  Batch{Issues: []Issue{{Number: "1", Title: "blocker"}, {Number: "2", Title: "dependent"}}, Edges: edges},
	})
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	out := testutil.CaptureStdout(t, func() {
		if err := run(c, nil, fc, fc, dir, f, s, plan, claimer); err != nil {
			t.Fatalf("first Run: %v", err)
		}
	})

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("first Run: RunCalls = %v, want exactly issue 1", fr.RunCalls)
	}
	if !strings.Contains(out, "1 issue(s) remain for a later invocation") {
		t.Errorf("first Run must report the held dependent; got:\n%s", out)
	}
	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if !containsLabel(iss1.Labels, c.CompleteLabel) {
		t.Errorf("blocker must reach %q through settle; labels=%v", c.CompleteLabel, iss1.Labels)
	}
	iss2, err := fc.Issue("2")
	if err != nil {
		t.Fatalf("Issue(2): %v", err)
	}
	if !containsLabel(iss2.Labels, label) || containsLabel(iss2.Labels, testInProgressLabel) {
		t.Errorf("dependent must stay unclaimed on %q after the first Run; labels=%v", label, iss2.Labels)
	}

	plan, err = NewPlan(c, Input{
		Origin: OriginDiscovered,
		Batch:  Batch{Issues: []Issue{{Number: "2", Title: "dependent"}}, Edges: edges},
	})
	if err != nil {
		t.Fatalf("NewPlan (second): %v", err)
	}
	if err := run(c, nil, fc, fc, dir, f, s, plan, claimer); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(fr.RunCalls) != 2 || fr.RunCalls[1].Issue != "2" {
		t.Fatalf("second Run: RunCalls = %v, want issue 2 dispatched second", fr.RunCalls)
	}
}
