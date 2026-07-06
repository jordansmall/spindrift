package main

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// TestDrainMaxJobs_SkipsBlockedDispatchesNext verifies that when MAX_JOBS=1
// the oldest blocked issue is skipped and the next unblocked issue is dispatched.
func TestDrainMaxJobs_SkipsBlockedDispatchesNext(t *testing.T) {
	c := baseConfig()
	c.label = "agent-trigger"
	c.maxParallel = 2
	c.maxJobs = 1

	fc := forge.NewFake()
	// Issue #1 is blocked by #3 (open, no complete label).
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"}) // blocker, not complete

	fr := runner.NewFake()

	edges := map[string][]string{"1": {"3"}}

	dir := tempLogDir(t)
	if err := drainMaxJobs(c, fc, dir, fr, []issue{
		{number: "1", title: "blocked issue"},
		{number: "2", title: "unblocked issue"},
	}, edges); err != nil {
		t.Fatalf("drainMaxJobs: %v", err)
	}

	// Only the unblocked issue #2 must have been dispatched.
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}
	if fr.RunCalls[0].Issue != "2" {
		t.Errorf("dispatched issue: got %q, want \"2\"", fr.RunCalls[0].Issue)
	}
}

// TestDrainMaxJobs_CycleErrors verifies that a dependency cycle in the batch
// causes drainMaxJobs to return an error before dispatching any issue.
func TestDrainMaxJobs_CycleErrors(t *testing.T) {
	c := baseConfig()
	c.label = "agent-trigger"
	c.maxParallel = 2
	c.maxJobs = 1

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.label}})

	fr := runner.NewFake()

	// 1→2→3→1 cycle
	edges := map[string][]string{
		"1": {"2"},
		"2": {"3"},
		"3": {"1"},
	}

	dir := tempLogDir(t)
	err := drainMaxJobs(c, fc, dir, fr, []issue{
		{number: "1", title: "a"},
		{number: "2", title: "b"},
		{number: "3", title: "c"},
	}, edges)

	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error %q should mention cycle", err.Error())
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0 (no dispatch before cycle check)", len(fr.RunCalls))
	}
}
