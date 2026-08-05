package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestResolvePriority verifies ResolvePriority maps the agent-priority-*
// labels (ADR 0040) to the canonical Priority tiers, highest tier winning
// when an issue somehow carries more than one priority label, and unrelated
// labels never false-positive into a non-Normal tier. This is the single
// exhaustive matrix for the rule — every IssueTracker adapter (github,
// forge.Fake) calls ResolvePriority instead of re-deriving it, so this is
// the only place the full case table needs to live (see priority.go).
func TestResolvePriority(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   forge.Priority
	}{
		{
			name:   "no priority label",
			labels: nil,
			want:   forge.PriorityNormal,
		},
		{
			name:   "critical label",
			labels: []string{"agent-priority-critical"},
			want:   forge.PriorityCritical,
		},
		{
			name:   "high label",
			labels: []string{"agent-priority-high"},
			want:   forge.PriorityHigh,
		},
		{
			name:   "low label",
			labels: []string{"agent-priority-low"},
			want:   forge.PriorityLow,
		},
		{
			name:   "conflicting labels, highest wins",
			labels: []string{"agent-priority-low", "agent-priority-critical"},
			want:   forge.PriorityCritical,
		},
		{
			name:   "unrelated labels present, no priority label",
			labels: []string{"bug", "ready-for-agent"},
			want:   forge.PriorityNormal,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := forge.ResolvePriority(tt.labels); got != tt.want {
				t.Errorf("ResolvePriority(%v) = %v, want %v", tt.labels, got, tt.want)
			}
		})
	}
}

// TestPriorityLabelNames verifies PriorityLabelNames is the single source of
// the three agent-priority-* label strings, in critical/high/low order,
// matching ResolvePriority's precedence — mirroring how ResearchDispatchLabels
// single-sources the research label family (see verdict.go).
func TestPriorityLabelNames(t *testing.T) {
	want := []string{"agent-priority-critical", "agent-priority-high", "agent-priority-low"}
	got := forge.PriorityLabelNames()
	if len(got) != len(want) {
		t.Fatalf("PriorityLabelNames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("PriorityLabelNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// prioritizedThing is a non-forge.Issue type used to verify SortByPriority
// is generic over any type with an extractable Priority, not hardcoded to
// forge.Issue.
type prioritizedThing struct {
	name     string
	priority forge.Priority
}

// TestSortByPriority_GenericOverNonIssueType verifies SortByPriority sorts
// any []T given a priority-extractor func(T) forge.Priority, stably, not
// just []forge.Issue — the generic signature this slice introduces.
func TestSortByPriority_GenericOverNonIssueType(t *testing.T) {
	things := []prioritizedThing{
		{name: "a", priority: forge.PriorityNormal},
		{name: "b", priority: forge.PriorityCritical},
		{name: "c", priority: forge.PriorityLow},
		{name: "d", priority: forge.PriorityHigh},
		{name: "e", priority: forge.PriorityNormal},
	}
	forge.SortByPriority(things, func(t prioritizedThing) forge.Priority { return t.priority })
	want := []string{"b", "d", "a", "e", "c"}
	got := make([]string, len(things))
	for i, th := range things {
		got[i] = th.name
	}
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("order = %v, want %v", got, want)
			break
		}
	}
}

// issueNumber extracts an Issue's Number field, passed to forge.Numbers
// below for concise ordering assertions.
func issueNumber(i forge.Issue) string { return i.Number }

// TestNumbers_MapsInOrder verifies Numbers maps each item to its number
// string via the caller-supplied extractor, preserving input order and
// length — including an empty input mapping to an empty (non-nil-length)
// slice — the direct unit coverage for this exported helper, generic over
// any item type (here a non-forge.Issue prioritizedThing-shaped type) so
// waves' own Issue type is covered by the same contract without a
// forge.Issue-specific test.
func TestNumbers_MapsInOrder(t *testing.T) {
	type numbered struct{ id string }
	items := []numbered{{id: "3"}, {id: "1"}, {id: "2"}}
	got := forge.Numbers(items, func(n numbered) string { return n.id })
	want := []string{"3", "1", "2"}
	if len(got) != len(want) {
		t.Fatalf("Numbers(...) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Numbers(...) = %v, want %v", got, want)
			break
		}
	}
}

// TestNumbers_Empty verifies Numbers on an empty slice returns an empty
// (zero-length) slice rather than panicking or returning nil-vs-non-nil in
// a way callers need to special-case.
func TestNumbers_Empty(t *testing.T) {
	type numbered struct{ id string }
	got := forge.Numbers([]numbered{}, func(n numbered) string { return n.id })
	if len(got) != 0 {
		t.Errorf("Numbers([]) = %v, want empty", got)
	}
}

// TestSortByPriority_SortsDescending verifies a mixed-priority batch sorts
// to Critical, High, Normal, Low regardless of input order (ADR 0040) —
// the same order the launcher's headless dispatch pool uses.
func TestSortByPriority_SortsDescending(t *testing.T) {
	issues := []forge.Issue{
		{Number: "1", Priority: forge.PriorityNormal},
		{Number: "2", Priority: forge.PriorityCritical},
		{Number: "3", Priority: forge.PriorityLow},
		{Number: "4", Priority: forge.PriorityHigh},
	}
	forge.SortByPriority(issues, func(i forge.Issue) forge.Priority { return i.Priority })
	got := forge.Numbers(issues, issueNumber)
	want := []string{"2", "4", "1", "3"}
	if len(got) != len(want) {
		t.Fatalf("issue order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("issue order = %v, want %v", got, want)
			break
		}
	}
}

// TestSortByPriority_StableWithinTier verifies equal-priority issues keep
// their original relative (input) order after the sort — since every Issue
// Tracker adapter already returns issues oldest-first, this makes
// oldest-first the natural tiebreaker within a tier.
func TestSortByPriority_StableWithinTier(t *testing.T) {
	issues := []forge.Issue{
		{Number: "5", Priority: forge.PriorityNormal},
		{Number: "2", Priority: forge.PriorityNormal},
	}
	forge.SortByPriority(issues, func(i forge.Issue) forge.Priority { return i.Priority })
	got := forge.Numbers(issues, issueNumber)
	want := []string{"5", "2"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("issue order = %v, want %v (input order preserved within tier)", got, want)
			break
		}
	}
}

// TestSortByPriority_AllNormalUnchanged verifies an all-Normal (unlabeled)
// input's order is unchanged — a byte-identical passthrough.
func TestSortByPriority_AllNormalUnchanged(t *testing.T) {
	issues := []forge.Issue{
		{Number: "1"},
		{Number: "2"},
		{Number: "3"},
	}
	want := forge.Numbers(issues, issueNumber)
	forge.SortByPriority(issues, func(i forge.Issue) forge.Priority { return i.Priority })
	got := forge.Numbers(issues, issueNumber)
	if len(got) != len(want) {
		t.Fatalf("issue order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("issue order = %v, want %v (unchanged)", got, want)
			break
		}
	}
}
