package waves

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// TestDispatch_DiscoveredWithDeclaredBlocker_DispatchesOnlyUnblocked verifies
// that Dispatch (#1547's single headless entry point) folds resolving
// issues' blocker readiness (formerly a hand-sequenced BuildEdges call),
// validating the Plan, and running it into one call: given a batch where
// issue #2 declares issue #3 as an unmet blocker via body text, Dispatch
// launches only the unblocked #1, leaving #2 on the dispatch label rather
// than claimed.
func TestDispatch_DiscoveredWithDeclaredBlocker_DispatchesOnlyUnblocked(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Body: "## Blocked by\n- #3", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"}) // #2's blocker, not yet complete

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	issues := []Issue{{Number: "1", Title: "unblocked"}, {Number: "2", Title: "dependent"}}
	if err := Dispatch(c, fc, fc, dir, f, s, OriginDiscovered, issues); err != nil {
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
