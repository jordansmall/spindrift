package waves

import (
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// TestDispatchWaves_TouchOverlapDeadlocksWithoutRelease verifies that a
// Dispatchable issue whose declared ## Touches overlaps an InProgress
// issue's declared touches is held rather than dispatched — proven here by
// never releasing the collider: dispatchWaves must time out (dependency
// deadlock) rather than dispatch the overlapping issue.
func TestDispatchWaves_TouchOverlapDeadlocksWithoutRelease(t *testing.T) {
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
	s := newSettle(fc, fc)
	err := dispatchWaves(c, fc, fc, f, s, []Issue{
		{Number: "10", Title: "candidate"},
	}, map[string][]string{})
	if err == nil {
		t.Fatal("dispatchWaves must deadlock while #20 stays in-progress with an overlapping touch-set")
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("issue 10 must not be dispatched while its touches overlap in-progress #20; got %d run calls", len(fr.RunCalls))
	}
}

// TestDispatchWaves_TouchOverlapDispatchesAfterColliderCompletes verifies
// that once the colliding in-progress issue leaves InProgress, the deferred
// candidate is dispatched — the same defer-and-retry treatment declared
// blockers already get.
func TestDispatchWaves_TouchOverlapDispatchesAfterColliderCompletes(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"
	c.DepsPollSecs = 1
	c.DepsWaitSecs = 5

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

	go func() {
		time.Sleep(1200 * time.Millisecond)
		fc.TransitionState("20", forge.InProgress, forge.Complete)
	}()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	if err := dispatchWaves(c, fc, fc, f, s, []Issue{
		{Number: "10", Title: "candidate"},
	}, map[string][]string{}); err != nil {
		t.Fatalf("dispatchWaves: %v", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("want 1 run call once the collider completed, got %d", len(fr.RunCalls))
	}

	iss10, err := fc.Issue("10")
	if err != nil {
		t.Fatalf("Issue(10): %v", err)
	}
	if !containsLabel(iss10.Labels, c.InProgressLabel) {
		t.Errorf("issue 10 must have been claimed (label %q); labels=%v", c.InProgressLabel, iss10.Labels)
	}
}

// TestDispatchWaves_NoTouchesDeclaredDispatchesImmediately verifies that an
// issue with no ## Touches section is dispatched exactly as today, even
// while an unrelated issue is in progress.
func TestDispatchWaves_NoTouchesDeclaredDispatchesImmediately(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"
	c.DepsPollSecs = 1
	c.DepsWaitSecs = 5

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Labels: []string{c.Label}})
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
	if err := dispatchWaves(c, fc, fc, f, s, []Issue{
		{Number: "10", Title: "candidate"},
	}, map[string][]string{}); err != nil {
		t.Fatalf("dispatchWaves: %v", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("issue with no declared touches must dispatch immediately; got %d run calls", len(fr.RunCalls))
	}
}

// TestDispatchWaves_OverlapV2HoldsOnPRChangedFiles verifies a candidate is
// held when it collides with an in-progress issue's *actual* PR-changed
// files — not declared in that issue's ## Touches — proving the v2
// augmentation actually gates dispatchWaves, not just the unit-level check.
func TestDispatchWaves_OverlapV2HoldsOnPRChangedFiles(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"
	c.DepsPollSecs = 1
	c.DepsWaitSecs = 1

	branchPrefix := "agent/issue-"
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = branchPrefix
	fc.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- internal/pkgx/foo.go",
		Labels: []string{c.Label},
	})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- docs/reference.md",
		State:  "OPEN",
		Labels: []string{c.InProgressLabel},
	})
	fc.SetPR("agent/issue-20", forge.PR{URL: "https://github.com/owner/repo/pull/20"})
	fc.SetPRFiles("https://github.com/owner/repo/pull/20", []string{"internal/pkgx/foo.go"})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	err := dispatchWaves(c, fc, fc, f, s, []Issue{
		{Number: "10", Title: "candidate"},
	}, map[string][]string{})
	if err == nil {
		t.Fatal("dispatchWaves must deadlock while #20's open PR touches internal/pkgx/foo.go")
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("issue 10 must not be dispatched while colliding with #20's PR changed files; got %d run calls", len(fr.RunCalls))
	}
}

// TestDispatchWaves_FailsDependentWhenBlockerFails verifies that a dependent
// whose in-batch blocker reaches failedLabel is itself failed immediately,
// rather than holding until depsWaitSecs.
func TestDispatchWaves_FailsDependentWhenBlockerFails(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2
	c.DepsPollSecs = 1
	c.DepsWaitSecs = 2

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})

	fr := runner.NewFake()
	fr.RunErr = boxErr

	edges := map[string][]string{"2": {"1"}}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	if err := dispatchWaves(c, fc, fc, f, s, []Issue{
		{Number: "1", Title: "blocker"},
		{Number: "2", Title: "dependent"},
	}, edges); err != nil {
		t.Fatalf("dispatchWaves: %v", err)
	}

	iss2, err := fc.Issue("2")
	if err != nil {
		t.Fatalf("Issue(2): %v", err)
	}
	if !containsLabel(iss2.Labels, c.FailedLabel) {
		t.Errorf("issue 2 must have %q when blocker failed; labels=%v", c.FailedLabel, iss2.Labels)
	}
}

// TestDispatchWaves_OverlapGateOffDisablesCheck verifies OVERLAP_GATE=off
// dispatches an overlapping issue immediately, bypassing the gate entirely.
func TestDispatchWaves_OverlapGateOffDisablesCheck(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "off"
	c.DepsPollSecs = 1
	c.DepsWaitSecs = 5

	fc := forge.NewFake()
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
	if err := dispatchWaves(c, fc, fc, f, s, []Issue{
		{Number: "10", Title: "candidate"},
	}, map[string][]string{}); err != nil {
		t.Fatalf("dispatchWaves: %v", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("OVERLAP_GATE=off must dispatch immediately; got %d run calls", len(fr.RunCalls))
	}
}
