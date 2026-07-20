package waves

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// TestDispatch_DependencyEdge_DispatchesOnlyUnblocked verifies that Dispatch
// (#1547's single headless entry point) folds validating in as a Plan and
// running it into one call: given a batch where issue #2 declares issue #3
// as an unmet blocker, Dispatch launches only the unblocked #1, leaving #2
// on the dispatch label rather than claimed — the same outcome the
// pre-#1547 hand-sequenced NewPlan-then-Run pair produced.
func TestDispatch_DependencyEdge_DispatchesOnlyUnblocked(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"}) // #2's blocker, not yet complete

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	in := Input{
		Origin: OriginDiscovered,
		Issues: []Issue{{Number: "1", Title: "unblocked"}, {Number: "2", Title: "dependent"}},
		Edges:  map[string][]string{"2": {"3"}},
	}
	if err := Dispatch(c, fc, fc, dir, f, s, in); err != nil {
		t.Fatalf("Dispatch: %v", err)
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

// TestDispatch_Cycle_ReturnsErrorWithoutDispatching verifies that Dispatch
// surfaces NewPlan's dependency-cycle error rather than silently running an
// invalid batch — the validation half of the plan-then-run pair it folds.
func TestDispatch_Cycle_ReturnsErrorWithoutDispatching(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	in := Input{
		Origin: OriginDiscovered,
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{"1": {"2"}, "2": {"1"}},
	}
	err := Dispatch(c, fc, fc, dir, f, s, in)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("Dispatch: got %v, want a dependency-cycle error", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %d, want 0 (a cycle must never dispatch)", len(fr.RunCalls))
	}
}
