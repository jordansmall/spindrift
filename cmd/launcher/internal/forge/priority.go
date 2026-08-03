package forge

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
	found := false
	for _, label := range labels {
		var candidate Priority
		switch label {
		case "agent-priority-critical":
			candidate = PriorityCritical
		case "agent-priority-high":
			candidate = PriorityHigh
		case "agent-priority-low":
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
