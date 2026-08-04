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
