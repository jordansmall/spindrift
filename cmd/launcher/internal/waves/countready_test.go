package waves

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
)

// Regression test for issue #2939's review finding: the headless CLI's
// stale-drain heldBack count used to filter on dispatch readiness, so an
// issue blocked by an unresolved edge did not count. CountReady restores
// that filtering through the current Batch type.
func TestCountReady_ExcludesBlockedIssue(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "9", State: "OPEN"}) // #2's blocker, unmet

	batch := Batch{
		Issues: []Issue{
			{Number: "1", Title: "ready"},
			{Number: "2", Title: "blocked"},
		},
		Edges: map[string][]string{"2": {"9"}},
	}

	var got int
	testutil.CaptureStdout(t, func() {
		got = CountReady(c, fc, fc, batch, nil)
	})

	if got != 1 {
		t.Errorf("CountReady: got %d, want 1 (issue #2 excluded by its unresolved blocker)", got)
	}
}

// Regression test for the dropClaimed omission a review flagged on #2939:
// main.go built the headless stale-drain pending closure before RunContinuous
// ran, so it could not see the in-run claimed set, and an eventually
// consistent GitHub listing inflated heldBack. CountReady now takes that
// claimed set and applies dropClaimed before scanning.
func TestCountReady_ExcludesClaimedIssue(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	batch := Batch{
		Issues: []Issue{
			{Number: "1", Title: "ready"},
			{Number: "2", Title: "claimed this run"},
		},
	}

	got := CountReady(c, fc, fc, batch, map[string]bool{"2": true})

	if got != 1 {
		t.Errorf("CountReady: got %d, want 1 (issue #2 excluded as already claimed this run)", got)
	}
}

func TestCountReady_AllReadyCountsEveryIssue(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	batch := Batch{
		Issues: []Issue{
			{Number: "1", Title: "first"},
			{Number: "2", Title: "second"},
		},
		Edges: map[string][]string{},
	}

	got := CountReady(c, fc, fc, batch, nil)

	if got != 2 {
		t.Errorf("CountReady: got %d, want 2 (no blockers, both ready)", got)
	}
}
