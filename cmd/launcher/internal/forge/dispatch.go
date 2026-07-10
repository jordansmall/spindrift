package forge

// DispatchState is the canonical state of an issue in the dispatch lifecycle.
type DispatchState int

const (
	Dispatchable DispatchState = iota // ready for an agent to pick up
	InProgress                        // an agent is actively working this issue
	Complete                          // agent work merged and green
	Failed                            // box exited non-zero; needs human triage
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
