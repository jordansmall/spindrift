package forge

// DispatchState is the canonical state of an issue in the dispatch lifecycle.
type DispatchState int

const (
	Dispatchable DispatchState = iota
	InProgress
	Complete    // agent work merged and green
	Failed      // box exited non-zero; needs human triage
	Recoverable // work is salvageable; needs recovery, not a fresh dispatch
	Ambiguous   // title/body describe materially unrelated work; needs human triage
	// Untriaged is not a real tracker state. It is the "from" state that a
	// promoting TransitionState(Untriaged, Dispatchable) call names for an
	// issue carrying no dispatch label yet. Its Label is "", so every
	// adapter's remove-label step is a no-op (#646).
	Untriaged
)

// DispatchLabels maps DispatchState values to issue-tracker labels. Only the
// GitHub adapter reads them; Jira and local use their own native markers.
type DispatchLabels struct {
	Dispatchable string // default "ready-for-agent"
	InProgress   string // default "agent-in-progress"
	Complete     string // default "agent-complete"
	Failed       string // default "agent-failed"
	Recoverable  string // local-only frontmatter marker; not a real GitHub label
	// Ambiguous, unlike Recoverable, is a real issue-tracker label: the fixed
	// literal "agent-ambiguous-spec".
	Ambiguous string
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
	case Recoverable:
		return d.Recoverable
	case Ambiguous:
		return d.Ambiguous
	default:
		return ""
	}
}

// AllLabels returns the dispatch labels that back a real GitHub label.
// Recoverable is excluded because it is a local-only frontmatter marker, so
// adapters like the local tracker's ListLabels must not report it as present.
// Ambiguous is a real label and is included.
func (d DispatchLabels) AllLabels() []string {
	return []string{d.Dispatchable, d.InProgress, d.Complete, d.Failed, d.Ambiguous}
}

// ClaimRemoveLabels returns the labels a TransitionState call should remove.
// A claim (to == InProgress) also strips any stale Complete/Failed label left
// by a prior run, matching the claim-remove-labels set in
// .github/workflows/agent-dispatch.yml (#1985).
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
