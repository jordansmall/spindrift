package forge

// Verdict is the research dispatch's closed, three-way relevance judgment
// (ADR 0022). It is data carried by the Complete transition, not a lifecycle
// state of its own — kinds still share the four canonical DispatchState
// values.
type Verdict int

const (
	Recommend Verdict = iota
	Reject
	Unclear
)

// String renders the verdict as the outcome-line status token.
func (v Verdict) String() string {
	switch v {
	case Recommend:
		return "recommend"
	case Reject:
		return "reject"
	case Unclear:
		return "unclear"
	default:
		return "unknown"
	}
}

// ParseVerdict parses an outcome-line status token into a Verdict. ok is
// false for "blocked" or any other non-verdict status, which research settle
// maps to Failed instead of a Complete-with-verdict transition.
func ParseVerdict(status string) (Verdict, bool) {
	switch status {
	case "recommend":
		return Recommend, true
	case "reject":
		return Reject, true
	case "unclear":
		return Unclear, true
	default:
		return 0, false
	}
}

// VerdictLabels maps canonical Verdict values to their issue-tracker labels
// — the research kind's analog of DispatchLabels for the Complete
// transition, which for research fans out to three verdict terminals
// instead of the work kind's single Complete label.
type VerdictLabels struct {
	Recommend string
	Reject    string
	Unclear   string
}

// Label returns the native label string for verdict v.
func (v VerdictLabels) Label(verdict Verdict) string {
	switch verdict {
	case Recommend:
		return v.Recommend
	case Reject:
		return v.Reject
	case Unclear:
		return v.Unclear
	default:
		return ""
	}
}

// ResearchDispatchLabels returns the fixed github research label family
// (ADR 0022): agent-research -> agent-research-in-progress -> a verdict
// terminal, with agent-research-failed strictly meaning the Box crashed or
// produced no verdict. Unlike DispatchLabels for the work kind, these names
// are not operator-configurable — the research CI workflow and prompt key
// off them directly. Complete is left blank: verdict labels (see
// ResearchVerdictLabels) carry the Complete transition instead.
func ResearchDispatchLabels() DispatchLabels {
	return DispatchLabels{
		Dispatchable: "agent-research",
		InProgress:   "agent-research-in-progress",
		Failed:       "agent-research-failed",
	}
}

// ResearchVerdictLabels returns the fixed verdict-terminal labels a research
// dispatch's Complete transition swaps to (ADR 0022).
func ResearchVerdictLabels() VerdictLabels {
	return VerdictLabels{
		Recommend: "agent-research-recommend",
		Reject:    "agent-research-reject",
		Unclear:   "agent-research-unclear",
	}
}
