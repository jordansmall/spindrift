package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// This is the exhaustive case matrix for the agent-priority-* label rule
// (ADR 0040). Every IssueTracker adapter calls ResolvePriority instead of
// re-deriving it, so the full table only needs to live here.
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

// The order matters: PriorityLabelNames lists the labels in ResolvePriority's
// own precedence order, critical before high before low.
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

// prioritizedThing is deliberately not a forge.Issue, so the tests below pin
// that SortByPriority and Numbers stay generic over any item type.
type prioritizedThing struct {
	name     string
	priority forge.Priority
}

// The want order keeps "a" before "e", so this also pins that the sort is
// stable within a tier.
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

func issueNumber(i forge.Issue) string { return i.Number }

// The input is deliberately out of numeric order: Numbers preserves input
// order and must not sort. Testing it through a non-forge.Issue type covers
// waves' own Issue type by the same contract.
func TestNumbers_MapsInOrder(t *testing.T) {
	items := []prioritizedThing{{name: "3"}, {name: "1"}, {name: "2"}}
	got := forge.Numbers(items, func(t prioritizedThing) string { return t.name })
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

func TestNumbers_Empty(t *testing.T) {
	got := forge.Numbers([]prioritizedThing{}, func(t prioritizedThing) string { return t.name })
	if len(got) != 0 {
		t.Errorf("Numbers([]) = %v, want empty", got)
	}
}

// Critical, High, Normal, Low is the order the launcher's headless dispatch
// pool drains (ADR 0040).
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

// Every IssueTracker adapter returns issues oldest-first, so a stable sort
// makes oldest-first the tiebreaker within a tier.
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
