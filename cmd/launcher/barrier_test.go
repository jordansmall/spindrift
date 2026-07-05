package main

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// When barrierLabel is empty, filterByBarrier must return the input slice
// unchanged — no GitHub query, no filtering.
func TestBarrierFilter_EmptyLabel(t *testing.T) {
	c := baseConfig()
	// c.barrierLabel is "" (zero value)
	fc := forge.NewFake()
	input := []issue{{number: "1"}, {number: "5"}, {number: "10"}}

	got, err := filterByBarrier(c, fc, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("expected 3 issues unchanged, got %d: %+v", len(got), got)
	}
}

// When no open issues carry barrierLabel, nothing is filtered.
func TestBarrierFilter_NoOpenBarriers(t *testing.T) {
	c := baseConfig()
	c.barrierLabel = "my-barrier"
	fc := forge.NewFake()
	// No issues carry the barrier label at all.

	input := []issue{{number: "1"}, {number: "2"}, {number: "3"}}
	got, err := filterByBarrier(c, fc, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("expected all 3 issues, got %d: %+v", len(got), got)
	}
}

// When a barrier exists at B, issues numbered ≤ B are kept and issues > B are
// dropped. B itself dispatches normally.
func TestBarrierFilter_OneBarrier_HoldsHigher(t *testing.T) {
	c := baseConfig()
	c.barrierLabel = "my-barrier"
	fc := forge.NewFake()
	// Barrier issue: open, carries the barrier label.
	fc.SetIssue(forge.Issue{Number: "5", Title: "Barrier", State: "OPEN", Labels: []string{"my-barrier"}})

	input := []issue{{number: "1"}, {number: "5"}, {number: "7"}, {number: "10"}}

	got, err := filterByBarrier(c, fc, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantNums := []string{"1", "5"}
	if len(got) != len(wantNums) {
		t.Fatalf("expected %v, got %+v", wantNums, got)
	}
	for i, iss := range got {
		if iss.number != wantNums[i] {
			t.Errorf("position %d: got #%s, want #%s", i, iss.number, wantNums[i])
		}
	}
}

// A barrier issue that has been dispatched (carries the in-progress label in
// addition to the barrier label) must still fence higher-numbered issues. The
// fence lifts only when the issue is closed, not when it transitions labels.
func TestBarrierFilter_BarrierInProgress_StillFences(t *testing.T) {
	c := baseConfig()
	c.barrierLabel = "my-barrier"
	fc := forge.NewFake()
	// Barrier is open and carries BOTH the barrier label and the in-progress label.
	fc.SetIssue(forge.Issue{
		Number: "5",
		Title:  "Barrier in-progress",
		State:  "OPEN",
		Labels: []string{"my-barrier", "agent-in-progress"},
	})

	input := []issue{{number: "1"}, {number: "7"}}

	got, err := filterByBarrier(c, fc, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].number != "1" {
		t.Errorf("expected only #1, got %+v", got)
	}
}

// A closed barrier issue must not fence: closed issues are excluded from the
// barrier query because they no longer appear in the open-issue list.
func TestBarrierFilter_ClosedBarrier_DoesNotFence(t *testing.T) {
	c := baseConfig()
	c.barrierLabel = "my-barrier"
	fc := forge.NewFake()
	// CLOSED barrier — must not appear in ListIssues (which filters by open state).
	fc.SetIssue(forge.Issue{
		Number: "5",
		Title:  "Closed barrier",
		State:  "CLOSED",
		Labels: []string{"my-barrier"},
	})

	input := []issue{{number: "1"}, {number: "7"}}

	got, err := filterByBarrier(c, fc, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Barrier is closed → no fence → both issues pass through.
	if len(got) != 2 {
		t.Errorf("expected 2 issues (fence lifted), got %+v", got)
	}
}

// When two open barriers exist, the lower barrier number B is used so that
// barriers serialize lowest-first.
func TestBarrierFilter_TwoBarriers_LowestWins(t *testing.T) {
	c := baseConfig()
	c.barrierLabel = "my-barrier"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "5", Title: "Barrier A", State: "OPEN", Labels: []string{"my-barrier"}})
	fc.SetIssue(forge.Issue{Number: "10", Title: "Barrier B", State: "OPEN", Labels: []string{"my-barrier"}})

	input := []issue{{number: "3"}, {number: "5"}, {number: "7"}, {number: "10"}, {number: "12"}}

	got, err := filterByBarrier(c, fc, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Lowest barrier is #5; only issues ≤ 5 pass.
	wantNums := []string{"3", "5"}
	if len(got) != len(wantNums) {
		t.Fatalf("expected %v, got %+v", wantNums, got)
	}
	for i, iss := range got {
		if iss.number != wantNums[i] {
			t.Errorf("position %d: got #%s, want #%s", i, iss.number, wantNums[i])
		}
	}
}
