package forge

import "sort"

// The agent-priority-* label strings (ADR 0040). ResolvePriority's switch and
// PriorityLabelNames both read these rather than repeating the literals.
const (
	labelPriorityCritical = "agent-priority-critical"
	labelPriorityHigh     = "agent-priority-high"
	labelPriorityLow      = "agent-priority-low"
)

// PriorityLabelNames returns the three agent-priority-* labels (ADR 0040) in
// critical/high/low order, the same order ResolvePriority checks precedence in.
func PriorityLabelNames() []string {
	return []string{labelPriorityCritical, labelPriorityHigh, labelPriorityLow}
}

// ResolvePriority returns the priority tier an issue's labels name (ADR 0040);
// the highest wins if an issue carries more than one. Label names match
// case-sensitively, and an issue carrying none of the three resolves to
// PriorityNormal, the zero value. Every IssueTracker adapter calls this so none
// re-derives the switch.
func ResolvePriority(labels []string) Priority {
	priority := PriorityNormal
	// PriorityLow sorts below PriorityNormal, so a lone agent-priority-low
	// label wins over the zero-value default only through found, never
	// through the candidate > priority comparison.
	found := false
	for _, label := range labels {
		var candidate Priority
		switch label {
		case labelPriorityCritical:
			candidate = PriorityCritical
		case labelPriorityHigh:
			candidate = PriorityHigh
		case labelPriorityLow:
			candidate = PriorityLow
		default:
			continue
		}
		if !found || candidate > priority {
			priority = candidate
			found = true
		}
	}
	return priority
}

// SortByPriority stably orders items by Priority descending (ADR 0040). The
// sort must stay stable: every Issue Tracker adapter returns issues
// oldest-first, so stability alone makes oldest-first the tiebreaker within a
// tier. Generic so forge and waves share one implementation.
func SortByPriority[T any](items []T, priority func(T) Priority) {
	sort.SliceStable(items, func(i, j int) bool {
		return priority(items[i]) > priority(items[j])
	})
}

// Numbers maps items to their number strings, preserving input order. Generic
// so forge and waves share one implementation.
func Numbers[T any](items []T, number func(T) string) []string {
	nums := make([]string, len(items))
	for i, item := range items {
		nums[i] = number(item)
	}
	return nums
}
