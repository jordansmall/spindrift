package forge

// DispatchState is the canonical state of an issue in the dispatch lifecycle.
type DispatchState int

const (
	Dispatchable DispatchState = iota // ready for an agent to pick up
	InProgress                        // an agent is actively working this issue
	Complete                          // agent work merged and green
	Failed                            // box exited non-zero; needs human triage
	// Untriaged is not a real tracker state — it is the "from" state a
	// promotion TransitionState(Untriaged, Dispatchable) call names for an
	// issue that has never carried a dispatch label. Its Label is "", so
	// every adapter's remove-label step is a no-op (TransitionState only
	// ever adds the Dispatchable label), matching the "an unlabeled issue
	// is first promoted" step of the Console's Pick (#646).
	Untriaged
)

// DispatchLabels maps canonical DispatchState values to their issue-tracker
// labels. The GitHub adapter uses these to translate TransitionState calls
// into label swaps. Other adapters (Jira, local) use their own native markers.
type DispatchLabels struct {
	Dispatchable string // default "ready-for-agent"
	InProgress   string // default "agent-in-progress"
	Complete     string // default "agent-complete"
	Failed       string // default "agent-failed"
}

// Label returns the native label string for state s.
func (d DispatchLabels) Label(s DispatchState) string {
	switch s {
	case Dispatchable:
		return d.Dispatchable
	case InProgress:
		return d.InProgress
	case Complete:
		return d.Complete
	case Failed:
		return d.Failed
	default:
		return ""
	}
}

// AllLabels returns all four dispatch label strings.
func (d DispatchLabels) AllLabels() []string {
	return []string{d.Dispatchable, d.InProgress, d.Complete, d.Failed}
}

// ClaimRemoveLabels returns the labels a from -> to TransitionState call
// should remove: ordinarily just the from-state label, but a claim (to ==
// InProgress) also strips any stale Complete/Failed terminal label the issue
// might still carry from a prior run — matching the dispatch workflow's
// claim-remove-labels set (.github/workflows/agent-dispatch.yml) for the
// subset of labels this DispatchState model tracks (#1985). Empty labels are
// skipped and the result is deduplicated, so both the github adapter and
// forge.Fake can call this instead of each re-deriving the same rule.
func (d DispatchLabels) ClaimRemoveLabels(from, to DispatchState) []string {
	seen := map[string]bool{}
	var out []string
	add := func(l string) {
		if l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	add(d.Label(from))
	if to == InProgress {
		add(d.Complete)
		add(d.Failed)
	}
	return out
}
