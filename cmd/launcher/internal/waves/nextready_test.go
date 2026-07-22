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
	}, edges, nil, nil, nil)

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
		}, edges, nil, nil, nil)
		if ok {
			t.Fatalf("nextReady: got (%v, true), want ok=false", iss)
		}
	})

	if !strings.Contains(out, "~~ #1 blocked by #3, #4; skipping") {
		t.Errorf("output must name the unready blockers; got:\n%s", out)
	}
}

// TestNextReady_BlockedLineLogsOncePerState verifies that with a shared
// dedup map, nextReady's blocked-skip line prints once across identical
// re-walks — refill re-walks on every completion and the background poll
// re-walks every ~30s (#1637), which would otherwise reprint the same
// blocked line indefinitely — and re-prints only when the blocker set
// changes.
func TestNextReady_BlockedLineLogsOncePerState(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})
	fc.SetIssue(forge.Issue{Number: "4", State: "OPEN"})
	checkOverlap := func(string) (string, bool) { return "", false }

	logged := map[string]string{}
	cand := []Issue{{Number: "1", Title: "blocked issue"}}

	out := testutil.CaptureStdout(t, func() {
		// Two identical re-walks over the same blocked candidate.
		nextReady(c, fc, fc, checkOverlap, cand, map[string][]string{"1": {"3"}}, nil, nil, logged)
		nextReady(c, fc, fc, checkOverlap, cand, map[string][]string{"1": {"3"}}, nil, nil, logged)
	})
	if n := strings.Count(out, "#1 blocked by #3; skipping"); n != 1 {
		t.Fatalf("blocked-skip line must log once across identical re-walks; got %d:\n%s", n, out)
	}

	// A changed blocker set re-logs, so a genuine state change is surfaced.
	out = testutil.CaptureStdout(t, func() {
		nextReady(c, fc, fc, checkOverlap, cand, map[string][]string{"1": {"3", "4"}}, nil, nil, logged)
	})
	if !strings.Contains(out, "#1 blocked by #3, #4; skipping") {
		t.Errorf("a changed blocker set must re-log; got:\n%s", out)
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
		}, edges, nil, nil, nil)
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
	}, map[string][]string{}, nil, nil, nil)

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

// TestNextReady_Local_ClosedOnDiskUnblocksDependent_IndependentSeamUnaffected
// verifies CODE_FORGE=local's offline chaining (issue #1700): with cf shaped
// like the local adapter (forge.CodeForge but no PRForge, ADR 0033),
// blockerStatus's only path to readiness is it.Issue's closed-on-disk state
// — no PR lookup is even possible. A seam blocked by another stays unready
// until its blocker's frontmatter flips to closed, while a concurrently
// eligible independent seam is unaffected and dispatches regardless.
func TestNextReady_Local_ClosedOnDiskUnblocksDependent_IndependentSeamUnaffected(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c))
	cf := fc.AsLocal()

	// Seam 2 is blocked by seam 1 (still open); seam 3 has no blockers.
	fc.SetIssue(forge.Issue{Number: "1", State: "OPEN"})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.Label}})

	edges := map[string][]string{"2": {"1"}}
	checkOverlap := func(string) (string, bool) { return "", false }

	// Seam 1 still open: seam 2 stays blocked, so the independent seam 3 is
	// selected instead of waiting on it.
	iss, ok := nextReady(c, fc, cf, checkOverlap, []Issue{
		{Number: "2", Title: "dependent"},
		{Number: "3", Title: "independent"},
	}, edges, nil, nil, nil)
	if !ok || iss.Number != "3" {
		t.Fatalf("nextReady before blocker closes: got (%v, %v), want (\"3\", true)", iss, ok)
	}

	// Seam 1 lands and closes on disk (forge.IssueCloser, ADR 0029) -- a
	// frontmatter flip, no network call, and no PR for cf to even look up.
	fc.SetIssue(forge.Issue{Number: "1", State: "CLOSED"})

	iss, ok = nextReady(c, fc, cf, checkOverlap, []Issue{
		{Number: "2", Title: "dependent"},
	}, edges, nil, nil, nil)
	if !ok || iss.Number != "2" {
		t.Fatalf("nextReady after blocker closes: got (%v, %v), want (\"2\", true)", iss, ok)
	}
}

// TestNextReady_Local_LandingVerifiedUnblocksDependentInSameRun guards issue
// #1850: a local blocker's landing verifying merged into its Integration
// branch must unblock its dependent immediately, in the very next readiness
// check -- not held until the post-loop reconcile closes the blocker issue.
func TestNextReady_Local_LandingVerifiedUnblocksDependentInSameRun(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c))
	cf := fc.AsLocal()

	// Seam 2 is blocked by seam 1 (still open, no landing yet); seam 3 has
	// no blockers.
	fc.SetIssue(forge.Issue{Number: "1", State: "OPEN"})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.Label}})

	edges := map[string][]string{"2": {"1"}}
	checkOverlap := func(string) (string, bool) { return "", false }

	// Seam 1 still open with no landing: seam 2 stays blocked, so the
	// independent seam 3 is selected instead of waiting on it.
	iss, ok := nextReady(c, fc, cf, checkOverlap, []Issue{
		{Number: "2", Title: "dependent"},
		{Number: "3", Title: "independent"},
	}, edges, nil, nil, nil)
	if !ok || iss.Number != "3" {
		t.Fatalf("nextReady before blocker lands: got (%v, %v), want (\"3\", true)", iss, ok)
	}

	// Seam 1's Box finishes and its seam lands on the parent's Integration
	// branch -- settle's landing-upgrade records the rich
	// integration/<parent>@<sha> ref, but seam 1's issue is still OPEN
	// (reconcile hasn't run yet, it runs once after the loop returns).
	fc.SetIssue(forge.Issue{Number: "1", State: "OPEN", Landing: "integration/parent@abc123"})
	fc.SetVerifyLanding("integration/parent@abc123", true, nil)

	iss, ok = nextReady(c, fc, cf, checkOverlap, []Issue{
		{Number: "2", Title: "dependent"},
	}, edges, nil, nil, nil)
	if !ok || iss.Number != "2" {
		t.Fatalf("nextReady after blocker lands: got (%v, %v), want (\"2\", true)", iss, ok)
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
	}, map[string][]string{}, nil, nil, nil)

	if !ok {
		t.Fatalf("nextReady: got ok=false, want a match")
	}
	if iss.Number != "1" {
		t.Errorf("selected issue: got %q, want \"1\"", iss.Number)
	}
}
