package forge

// DispatchState is the canonical state of an issue in the dispatch lifecycle.
type DispatchState int

const (
	Dispatchable DispatchState = iota // ready for an agent to pick up
	InProgress                        // an agent is actively working this issue
	Complete                          // agent work merged and green
	Failed                            // box exited non-zero; needs human triage
	// SpecMismatch is issue #2275's pre-implement halt: the Box's SPEC CHECK
	// step, run before SCOUT/implement, found the issue's title and body
	// describe materially unrelated work and stopped short rather than
	// guessing which one governs. Modeled on the research dispatch kind's
	// agent-research-unclear escalation (ADR 0022) -- a "needs a human
	// decision" outcome, not a crash -- but SpecMismatch is a distinct
	// terminal state within the work kind's own DispatchState enum, not a
	// research Verdict.
	SpecMismatch
	Recoverable // work is salvageable; needs recovery, not a fresh dispatch
	// Untriaged is not a real tracker state — it is the "from" state a
	// promotion TransitionState(Untriaged, Dispatchable) call names for an
	// issue that has never carried a dispatch label. Its Label is "", so
	// every adapter's remove-label step is a no-op (TransitionState only
	// ever adds the Dispatchable label), matching the "an unlabeled issue
	// is first promoted" step of the Console's Pick (#646).
	Untriaged
)

// SpecMismatchLabel is the fixed, non-configurable label the launcher
// swaps onto an issue whose title and body describe materially unrelated
// work (issue #2275) -- a successful "needs human decision" halt,
// distinct from agent-failed, modeled on the research kind's fixed
// agent-research-unclear vocabulary (ADR 0022). Unlike the four
// DispatchLabels struct fields, this is never operator-configurable --
// DispatchLabels.Label(SpecMismatch) returns it unconditionally.
const SpecMismatchLabel = "agent-spec-mismatch"

// SpecMismatchStatus is the SPINDRIFT_OUTCOME status token
// issue-prompt.md's SPEC CHECK step emits to signal the halt -- a third
// status alongside the ready/blocked pair the work kind's OUTCOME section
// documents, valid only from that early-exit step.
const SpecMismatchStatus = "spec-mismatch"

// DispatchLabels maps canonical DispatchState values to their issue-tracker
// labels. The GitHub adapter uses these to translate TransitionState calls
// into label swaps. Other adapters (Jira, local) use their own native markers.
type DispatchLabels struct {
	Dispatchable string // default "ready-for-agent"
	InProgress   string // default "agent-in-progress"
	Complete     string // default "agent-complete"
	Failed       string // default "agent-failed"
	Recoverable  string // local-only frontmatter marker; not a real GitHub label
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
	case SpecMismatch:
		return SpecMismatchLabel
	case Recoverable:
		return d.Recoverable
	default:
		return ""
	}
}

// AllLabels returns all four dispatch label strings that back a real
// GitHub label. Recoverable is deliberately excluded: it is a local-only
// frontmatter marker (never a real GitHub label), so it must not appear in
// the registry-membership set adapters like the local tracker's ListLabels
// report as present.
func (d DispatchLabels) AllLabels() []string {
	return []string{d.Dispatchable, d.InProgress, d.Complete, d.Failed}
}

// ClaimRemoveLabels returns the labels a from -> to TransitionState call
// should remove: ordinarily just the from-state label, but a claim (to ==
// InProgress) also strips any stale Complete/Failed terminal label, plus the
// fixed SpecMismatchLabel (#2275), the issue might still carry from a prior
// run — matching the dispatch workflow's claim-remove-labels set
// (.github/workflows/agent-dispatch.yml) for the subset of labels this
// DispatchState model tracks (#1985). Empty labels are skipped and the
// result is deduplicated, so both the github adapter and forge.Fake can
// call this instead of each re-deriving the same rule.
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
		add(d.Label(SpecMismatch))
	}
	return out
}
