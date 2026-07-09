package main

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
	c.label = "agent-trigger"
	c.maxParallel = 1
	c.overlapGate = "defer"
	c.depsPollSecs = 1
	c.depsWaitSecs = 1

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{c.label},
	})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{c.inProgressLabel},
	})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	err := dispatchWaves(c, fc, dir, fr, []issue{
		{number: "10", title: "candidate"},
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
	c.label = "agent-trigger"
	c.maxParallel = 1
	c.overlapGate = "defer"
	c.depsPollSecs = 1
	c.depsWaitSecs = 5

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{c.label},
	})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{c.inProgressLabel},
	})

	fr := runner.NewFake()

	go func() {
		time.Sleep(1200 * time.Millisecond)
		fc.TransitionState("20", forge.InProgress, forge.Complete)
	}()

	dir := tempLogDir(t)
	if err := dispatchWaves(c, fc, dir, fr, []issue{
		{number: "10", title: "candidate"},
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
	if !containsLabel(iss10.Labels, c.inProgressLabel) {
		t.Errorf("issue 10 must have been claimed (label %q); labels=%v", c.inProgressLabel, iss10.Labels)
	}
}

// TestDispatchWaves_NoTouchesDeclaredDispatchesImmediately verifies that an
// issue with no ## Touches section is dispatched exactly as today, even
// while an unrelated issue is in progress.
func TestDispatchWaves_NoTouchesDeclaredDispatchesImmediately(t *testing.T) {
	c := baseConfig()
	c.label = "agent-trigger"
	c.maxParallel = 1
	c.overlapGate = "defer"
	c.depsPollSecs = 1
	c.depsWaitSecs = 5

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{c.inProgressLabel},
	})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	if err := dispatchWaves(c, fc, dir, fr, []issue{
		{number: "10", title: "candidate"},
	}, map[string][]string{}); err != nil {
		t.Fatalf("dispatchWaves: %v", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("issue with no declared touches must dispatch immediately; got %d run calls", len(fr.RunCalls))
	}
}

// TestDispatchWaves_OverlapGateOffDisablesCheck verifies OVERLAP_GATE=off
// dispatches an overlapping issue immediately, bypassing the gate entirely.
func TestDispatchWaves_OverlapGateOffDisablesCheck(t *testing.T) {
	c := baseConfig()
	c.label = "agent-trigger"
	c.maxParallel = 1
	c.overlapGate = "off"
	c.depsPollSecs = 1
	c.depsWaitSecs = 5

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{c.label},
	})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{c.inProgressLabel},
	})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	if err := dispatchWaves(c, fc, dir, fr, []issue{
		{number: "10", title: "candidate"},
	}, map[string][]string{}); err != nil {
		t.Fatalf("dispatchWaves: %v", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("OVERLAP_GATE=off must dispatch immediately; got %d run calls", len(fr.RunCalls))
	}
}
