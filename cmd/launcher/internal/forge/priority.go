package forge

import "sort"

// The agent-priority-{critical,high,low} label strings (ADR 0040). These are
// the single source of the label names: ResolvePriority's switch and
// PriorityLabelNames both read from these constants rather than duplicating
// the literals.
const (
	labelPriorityCritical = "agent-priority-critical"
	labelPriorityHigh     = "agent-priority-high"
	labelPriorityLow      = "agent-priority-low"
)

// PriorityLabelNames returns the three agent-priority-* label strings
// (ADR 0040) in critical/high/low order — the same order ResolvePriority
// checks precedence in. Callers that need to enumerate the label family
// (e.g. doctor's label-existence check) use this instead of re-deriving the
// literals, mirroring ResearchDispatchLabels/ResearchVerdictLabels.Entries's
// role for the research label family (see verdict.go).
func PriorityLabelNames() []string {
	return []string{labelPriorityCritical, labelPriorityHigh, labelPriorityLow}
}

// ResolvePriority scans an issue's label names for the agent-priority-
// {critical,high,low} labels (ADR 0040) and returns the canonical Priority
// tier, highest wins if an issue somehow carries more than one. Label names
// are matched exactly (case-sensitive); an issue with none of the three,
// including one with unrelated labels, resolves to PriorityNormal — the
// zero value.
//
// Shared by every IssueTracker adapter (today: github; Fake follows so the
// upcoming forgetest conformance contract can assert the same rule against
// both) so none of them re-derives the same label-matching switch —
// mirroring DispatchLabels.ClaimRemoveLabels's shared-helper precedent.
func ResolvePriority(labels []string) Priority {
	priority := PriorityNormal
	// found tracks whether any priority label matched yet — required
	// because PriorityLow < PriorityNormal, so a lone agent-priority-low
	// label must still win over the zero-value default via `!found`, not
	// just via the `candidate > priority` comparison.
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

// SortByPriority stably orders issues by Priority descending (Critical >
// High > Normal > Low); a stable sort means equal-priority issues keep
// their input relative order, which — since every Issue Tracker adapter
// already returns issues oldest-first — makes oldest-first the natural,
// zero-extra-code tiebreaker within a tier (ADR 0040).
func SortByPriority(issues []Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		return issues[i].Priority > issues[j].Priority
	})
}
