package waves

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
)

// nextReady holds, rather than cascade-fails, an issue whose in-batch blocker
// carries the failed label: agent-failed is recoverable (agent-recover retries
// it), so the dependent waits across retries instead of being mislabeled failed
// itself (#1984, incident #1972).
func TestNextReady_FailedBlockerHoldsDependent(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.FailedLabel}})

	edges := map[string][]string{"1": {"3"}}
	checkOverlap := func(string) (string, bool) { return "", false }

	out := testutil.CaptureStdout(t, func() {
		iss, ok := nextReady(c, fc, fc, checkOverlap, []Issue{
			{Number: "1", Title: "dependent"},
		}, edges, nil, nil, nil)
		if ok {
			t.Fatalf("nextReady: got (%v, true), want ok=false", iss)
		}
	})

	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if containsLabel(iss1.Labels, c.FailedLabel) {
		t.Errorf("issue 1 must not gain %q when blocker failed; labels=%v", c.FailedLabel, iss1.Labels)
	}
	if !containsLabel(iss1.Labels, label) {
		t.Errorf("issue 1 must keep %q while held; labels=%v", label, iss1.Labels)
	}
	if !strings.Contains(out, "~~ #1 blocked by #3; skipping") {
		t.Errorf("output must hold with the standard blocked-skip line; got:\n%s", out)
	}
}

// The blocked-skip line names the unready blockers, comma-joined, matching
// drainMaxJobs' enriched line.
func TestNextReady_BlockedLineNamesBlockers(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
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

// With a shared dedup map the blocked-skip line prints once across identical
// re-walks and re-prints only when the blocker set changes. Refill re-walks on
// every completion and the background poll re-walks every ~3m (#1637), which
// would otherwise reprint the same blocked line indefinitely.
func TestNextReady_BlockedLineLogsOncePerState(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})
	fc.SetIssue(forge.Issue{Number: "4", State: "OPEN"})
	checkOverlap := func(string) (string, bool) { return "", false }

	logged := map[string]string{}
	cand := []Issue{{Number: "1", Title: "blocked issue"}}

	out := testutil.CaptureStdout(t, func() {
		nextReady(c, fc, fc, checkOverlap, cand, map[string][]string{"1": {"3"}}, nil, nil, logged)
		nextReady(c, fc, fc, checkOverlap, cand, map[string][]string{"1": {"3"}}, nil, nil, logged)
	})
	if n := strings.Count(out, "#1 blocked by #3; skipping"); n != 1 {
		t.Fatalf("blocked-skip line must log once across identical re-walks; got %d:\n%s", n, out)
	}

	out = testutil.CaptureStdout(t, func() {
		nextReady(c, fc, fc, checkOverlap, cand, map[string][]string{"1": {"3", "4"}}, nil, nil, logged)
	})
	if !strings.Contains(out, "#1 blocked by #3, #4; skipping") {
		t.Errorf("a changed blocker set must re-log; got:\n%s", out)
	}
}

// Reproduces incident #1972: a blocker fails, agent-recover retries it, and it
// fails again before recovering. The dependent stays held, never gaining
// FailedLabel, through every failed round, then dispatches once the blocker is
// satisfied.
func TestNextReady_Issue1972_HeldAcrossBlockerRetries(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.FailedLabel}})

	edges := map[string][]string{"1": {"3"}}
	checkOverlap := func(string) (string, bool) { return "", false }

	if iss, ok := nextReady(c, fc, fc, checkOverlap, []Issue{
		{Number: "1", Title: "dependent"},
	}, edges, nil, nil, nil); ok {
		t.Fatalf("round 1: nextReady got (%v, true), want ok=false", iss)
	}

	// Round 2: agent-recover retried #3 and it failed again, so the fixture is
	// unchanged and #3 still carries the failed label.
	if iss, ok := nextReady(c, fc, fc, checkOverlap, []Issue{
		{Number: "1", Title: "dependent"},
	}, edges, nil, nil, nil); ok {
		t.Fatalf("round 2: nextReady got (%v, true), want ok=false", iss)
	}

	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if containsLabel(iss1.Labels, c.FailedLabel) {
		t.Errorf("issue 1 must never gain %q across retries; labels=%v", c.FailedLabel, iss1.Labels)
	}

	// Round 3: #3 recovers and closes while still labeled failed, so the closed
	// state alone has to satisfy the blocker.
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.FailedLabel}, State: "CLOSED"})
	iss, ok := nextReady(c, fc, fc, checkOverlap, []Issue{
		{Number: "1", Title: "dependent"},
	}, edges, nil, nil, nil)
	if !ok || iss.Number != "1" {
		t.Fatalf("round 3: nextReady got (%v, %v), want (#1, true) once blocker is satisfied", iss, ok)
	}
}

// nextReady defers an otherwise-ready issue whose declared touches overlap an
// in-progress issue's, continuing the scan to the next non-overlapping
// candidate.
func TestNextReady_TouchOverlapDefers(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.OverlapGate = "defer"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{
		Number: "1",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{label},
	})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{testInProgressLabel},
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
	if containsLabel(iss1.Labels, testInProgressLabel) || containsLabel(iss1.Labels, c.FailedLabel) {
		t.Errorf("deferred issue 1 must not be transitioned; labels=%v", iss1.Labels)
	}
}

// CODE_FORGE=local's offline chaining (#1700): with cf shaped like the local
// adapter (forge.CodeForge but no PRForge, ADR 0033), blockerStatus's only path
// to readiness is the issue's closed-on-disk state, because no PR lookup is
// possible. An independent seam still dispatches while the dependent waits.
func TestNextReady_Local_ClosedOnDiskUnblocksDependent_IndependentSeamUnaffected(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c, label))
	cf := fc.AsLocal()

	fc.SetIssue(forge.Issue{Number: "1", State: "OPEN"})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{label}})

	edges := map[string][]string{"2": {"1"}}
	checkOverlap := func(string) (string, bool) { return "", false }

	iss, ok := nextReady(c, fc, cf, checkOverlap, []Issue{
		{Number: "2", Title: "dependent"},
		{Number: "3", Title: "independent"},
	}, edges, nil, nil, nil)
	if !ok || iss.Number != "3" {
		t.Fatalf("nextReady before blocker closes: got (%v, %v), want (\"3\", true)", iss, ok)
	}

	// Seam 1 closes on disk (forge.IssueCloser, ADR 0029): a frontmatter flip,
	// no network call, and no PR for cf to look up.
	fc.SetIssue(forge.Issue{Number: "1", State: "CLOSED"})

	iss, ok = nextReady(c, fc, cf, checkOverlap, []Issue{
		{Number: "2", Title: "dependent"},
	}, edges, nil, nil, nil)
	if !ok || iss.Number != "2" {
		t.Fatalf("nextReady after blocker closes: got (%v, %v), want (\"2\", true)", iss, ok)
	}
}

// Guards #1850: a local blocker's landing contained in its Integration branch
// unblocks the dependent in the very next readiness check, not only once the
// post-loop reconcile closes the blocker issue. Needs a non-empty c.SeedScopeOf,
// because #2151 dropped the no-scope self-verification fallback a zero
// SeedScope used to take.
func TestNextReady_Local_LandingVerifiedUnblocksDependentInSameRun(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.SeedScopeOf = func(string) forge.SeedScope { return forge.NewSeedScope("parent", "integration/parent") }

	fc := forge.NewFake(dispatchLabels(c, label))
	cf := fc.AsLocal()

	fc.SetIssue(forge.Issue{Number: "1", State: "OPEN"})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{label}})

	edges := map[string][]string{"2": {"1"}}
	checkOverlap := func(string) (string, bool) { return "", false }

	iss, ok := nextReady(c, fc, cf, checkOverlap, []Issue{
		{Number: "2", Title: "dependent"},
		{Number: "3", Title: "independent"},
	}, edges, nil, nil, nil)
	if !ok || iss.Number != "3" {
		t.Fatalf("nextReady before blocker lands: got (%v, %v), want (\"3\", true)", iss, ok)
	}

	// Seam 1 lands on the parent's Integration branch, so settle records the
	// rich integration/<parent>@<sha> ref, but seam 1's issue is still OPEN:
	// reconcile runs once after the loop returns, not yet.
	fc.SetIssue(forge.Issue{Number: "1", State: "OPEN", Landing: "integration/parent@abc123"})
	fc.SetLandingContained("integration/parent@abc123", "parent", true, nil)

	iss, ok = nextReady(c, fc, cf, checkOverlap, []Issue{
		{Number: "2", Title: "dependent"},
	}, edges, nil, nil, nil)
	if !ok || iss.Number != "2" {
		t.Fatalf("nextReady after blocker lands: got (%v, %v), want (\"2\", true)", iss, ok)
	}
}

// nextReady's own returned dispatch decision honors Config.IgnoreBlockers
// (research-kind continuous dispatch), not just CountReady's tally. The
// ignoreblockers_test.go sibling pins drainMaxJobs' whole-batch path; this pins
// the separate nextReady refill path RunContinuous calls, which earlier
// IgnoreBlockers tests only reached through CountReady.
func TestNextReady_IgnoreBlockers_DispatchesDespiteUnmetBlocker(t *testing.T) {
	c := baseConfig()
	label := "agent-research"
	c.IgnoreBlockers = true

	fc := forge.NewFake(dispatchLabels(c, label))
	// Blocker #3 is open with no complete label, which would normally hold #1.
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})

	edges := map[string][]string{"1": {"3"}}
	checkOverlap := func(string) (string, bool) { return "", false }

	iss, ok := nextReady(c, fc, fc, checkOverlap, []Issue{
		{Number: "1", Title: "blocked issue"},
	}, edges, nil, nil, nil)

	if !ok || iss.Number != "1" {
		t.Fatalf("nextReady: got (%v, %v), want (\"1\", true) -- blocker edges must not gate research dispatch", iss, ok)
	}
}

// With no cascade or overlap in play, nextReady still selects the first
// dispatch-ready issue in scan order, so the cascade and overlap tests cannot
// mask a broken happy path.
func TestNextReady_HappyPath(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

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
