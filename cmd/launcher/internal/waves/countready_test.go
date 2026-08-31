package waves

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
)

// TestCountReady_ExcludesBlockedIssue is the regression test for issue
// #2939's review finding: the headless CLI's stale-drain heldBack count
// used to be dispatch-readiness-filtered (the old countReady, now deleted
// with the Config-closure-switch machinery it belonged to), excluding an
// issue blocked by an unresolved edge. CountReady restores that same
// filtering through the current Batch type, so of two candidate issues --
// #1 ready, #2 blocked by open #9 -- only #1 counts.
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

// TestCountReady_ExcludesClaimedIssue is the regression test for the
// dropClaimed omission a review flagged on #2939: main.go's headless
// stale-drain pending closure was built and wired into NewHeadlessQueue
// before RunContinuous ever runs, so it has no access to RunContinuous's
// own in-run claimed set -- meaning a GitHub listing that's still
// eventually-consistent after an in-run claim (dropClaimed's own doc
// comment) inflated heldBack by counting an issue this run already
// dispatched. CountReady now takes that claimed set directly and applies
// dropClaimed before scanning, so of two otherwise-ready issues, the one
// already claimed this run is excluded from the tally.
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

// TestCountReady_AllReadyCountsEveryIssue is CountReady's straightforward
// happy path: with no blockers and no touch-overlap in play, every issue in
// the batch counts.
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
