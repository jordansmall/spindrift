package waves

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
)

// TestNextReady_FailedBlockerCascadesToFailed verifies that nextReady skips
// an issue whose in-batch blocker carries the failed label and transitions
// the dependent from Dispatchable to Failed, matching drainMaxJobs' cascade
// semantics on the single-slot refill path.
func TestNextReady_FailedBlockerCascadesToFailed(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c))
	// Issue #1 is blocked by #3, which has already reached the failed label.
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.FailedLabel}})

	edges := map[string][]string{"1": {"3"}}
	checkOverlap := func(string) (string, bool) { return "", false }

	iss, ok := nextReady(c, fc, fc, checkOverlap, []Issue{
		{Number: "1", Title: "dependent"},
	}, edges, nil, map[string]bool{})

	if ok {
		t.Fatalf("nextReady: got (%v, true), want ok=false", iss)
	}

	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if !containsLabel(iss1.Labels, c.FailedLabel) {
		t.Errorf("issue 1 must have %q when blocker failed; labels=%v", c.FailedLabel, iss1.Labels)
	}
}

// TestNextReady_BlockedLineNamesBlockers verifies that nextReady's
// blocked-skip line names the specific unready blocker issue number(s),
// comma-joined, matching drainMaxJobs' enriched line.
func TestNextReady_BlockedLineNamesBlockers(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c))
	// Issue #1 is blocked by both #3 and #4 (open, no complete label).
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})
	fc.SetIssue(forge.Issue{Number: "4", State: "OPEN"})

	edges := map[string][]string{"1": {"3", "4"}}
	checkOverlap := func(string) (string, bool) { return "", false }

	out := testutil.CaptureStdout(t, func() {
		iss, ok := nextReady(c, fc, fc, checkOverlap, []Issue{
			{Number: "1", Title: "blocked issue"},
		}, edges, nil, map[string]bool{})
		if ok {
			t.Fatalf("nextReady: got (%v, true), want ok=false", iss)
		}
	})

	if !strings.Contains(out, "~~ #1 blocked by #3, #4; skipping") {
		t.Errorf("output must name the unready blockers; got:\n%s", out)
	}
}

// TestNextReady_FailedBlockerLineNamesBlockers verifies that nextReady's
// failed-blocker skip line names the specific failed blocker issue
// number(s), comma-joined, rather than the generic "a dependency failed"
// message.
func TestNextReady_FailedBlockerLineNamesBlockers(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c))
	// Issue #1 is blocked by both #3 and #4, which have already failed.
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.FailedLabel}})
	fc.SetIssue(forge.Issue{Number: "4", Labels: []string{c.FailedLabel}})

	edges := map[string][]string{"1": {"3", "4"}}
	checkOverlap := func(string) (string, bool) { return "", false }

	out := testutil.CaptureStdout(t, func() {
		iss, ok := nextReady(c, fc, fc, checkOverlap, []Issue{
			{Number: "1", Title: "dependent"},
		}, edges, nil, map[string]bool{})
		if ok {
			t.Fatalf("nextReady: got (%v, true), want ok=false", iss)
		}
	})

	if !strings.Contains(out, "!! #1  status=blocker-failed  note=#3, #4 failed; skipping") {
		t.Errorf("output must name the failed blockers; got:\n%s", out)
	}
}

// TestNextReady_TouchOverlapDefers verifies that nextReady defers an
// otherwise-ready issue whose declared touches overlap an in-progress
// issue's, continuing the scan instead, and selects the next non-overlapping
// candidate.
func TestNextReady_TouchOverlapDefers(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.OverlapGate = "defer"

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{
		Number: "1",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{c.Label},
	})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{c.InProgressLabel},
	})

	checkOverlap := waveOverlapCheck(c, fc, fc)

	iss, ok := nextReady(c, fc, fc, checkOverlap, []Issue{
		{Number: "1", Title: "overlapping issue"},
		{Number: "2", Title: "clean issue"},
	}, map[string][]string{}, nil, map[string]bool{})

	if !ok {
		t.Fatalf("nextReady: got ok=false, want a match")
	}
	if iss.Number != "2" {
		t.Errorf("selected issue: got %q, want \"2\"", iss.Number)
	}

	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if containsLabel(iss1.Labels, c.InProgressLabel) || containsLabel(iss1.Labels, c.FailedLabel) {
		t.Errorf("deferred issue 1 must not be transitioned; labels=%v", iss1.Labels)
	}
}

// TestNextReady_HappyPath verifies that with no cascade or overlap in play,
// nextReady still selects the first dispatch-ready issue in scan order —
// guarding against the cascade and overlap tests masking the happy path.
func TestNextReady_HappyPath(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})

	checkOverlap := func(string) (string, bool) { return "", false }

	iss, ok := nextReady(c, fc, fc, checkOverlap, []Issue{
		{Number: "1", Title: "first"},
		{Number: "2", Title: "second"},
	}, map[string][]string{}, nil, map[string]bool{})

	if !ok {
		t.Fatalf("nextReady: got ok=false, want a match")
	}
	if iss.Number != "1" {
		t.Errorf("selected issue: got %q, want \"1\"", iss.Number)
	}
}
