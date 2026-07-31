package forge

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Verdict is the research dispatch's relevance judgment (ADR 0022). It is
// data carried by the Complete transition, not a lifecycle state of its own
// — kinds still share the four canonical DispatchState values. Verdict is
// string-valued (not a closed enum) because the vocabulary is operator
// configurable via RESEARCH_VERDICTS (issue #2201) — the compiled default
// set (ResearchVerdictLabels) remains the fallback when unset.
type Verdict string

const (
	Recommend Verdict = "recommend"
	Reject    Verdict = "reject"
	Unclear   Verdict = "unclear"
)

// String renders the verdict as the outcome-line status token.
func (v Verdict) String() string { return string(v) }

// VerdictLabel is one entry of the research kind's configurable verdict
// vocabulary: a verdict token, the issue-tracker label its Complete
// transition swaps to, and the one-line meaning rendered into the prompt.
type VerdictLabel struct {
	Verdict     Verdict
	Label       string
	Description string
}

// VerdictLabels is the ordered verdict->label mapping the research kind's
// Complete transition swaps to — the research kind's analog of
// DispatchLabels for the Complete transition, which for research fans out
// to a configurable set of verdict terminals instead of the work kind's
// single Complete label. The zero value is inert: Empty is true and Label
// always returns "", matching the work kind's unconfigured construction
// path.
type VerdictLabels struct {
	entries []VerdictLabel
}

// NewVerdictLabels builds a VerdictLabels from an ordered list of entries.
func NewVerdictLabels(entries ...VerdictLabel) VerdictLabels {
	return VerdictLabels{entries: entries}
}

// Label returns the native label string for verdict, or "" if verdict is
// not present in the set.
func (v VerdictLabels) Label(verdict Verdict) string {
	for _, e := range v.entries {
		if e.Verdict == verdict {
			return e.Label
		}
	}
	return ""
}

// Parse parses an outcome-line status token into a Verdict. ok is false
// unless status matches one of the configured entries' tokens — research
// settle maps a false ok to Failed instead of a Complete-with-verdict
// transition.
func (v VerdictLabels) Parse(status string) (Verdict, bool) {
	for _, e := range v.entries {
		if string(e.Verdict) == status {
			return Verdict(status), true
		}
	}
	return "", false
}

// Verdicts returns the configured verdict tokens in order.
func (v VerdictLabels) Verdicts() []Verdict {
	verdicts := make([]Verdict, len(v.entries))
	for i, e := range v.entries {
		verdicts[i] = e.Verdict
	}
	return verdicts
}

// Entries returns an ordered copy of the configured entries.
func (v VerdictLabels) Entries() []VerdictLabel {
	entries := make([]VerdictLabel, len(v.entries))
	copy(entries, v.entries)
	return entries
}

// Empty reports whether no verdict entries are configured.
func (v VerdictLabels) Empty() bool { return len(v.entries) == 0 }

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

// ResearchVerdictLabels returns the compiled-default verdict-terminal
// labels a research dispatch's Complete transition swaps to (ADR 0022).
// This is the fallback ParseResearchVerdicts returns when RESEARCH_VERDICTS
// is unset.
func ResearchVerdictLabels() VerdictLabels {
	return NewVerdictLabels(
		VerdictLabel{
			Verdict:     Recommend,
			Label:       "agent-research-recommend",
			Description: "relevant, now enriched with real context; promote it.",
		},
		VerdictLabel{
			Verdict:     Reject,
			Label:       "agent-research-reject",
			Description: "false positive, not worth doing, or a duplicate.",
		},
		VerdictLabel{
			Verdict:     Unclear,
			Label:       "agent-research-unclear",
			Description: "relevance can't be determined without a human's answer.",
		},
	)
}

// blockedVerdict is the reserved escape-hatch outcome-line status: it means
// the researcher couldn't reach a verdict at all (crash or no verdict),
// never a concluded verdict, so it can never be a configurable verdict
// token.
const blockedVerdict = "blocked"

// ParseResearchVerdicts parses the RESEARCH_VERDICTS knob: a JSON array of
// {verdict,label,description} objects, order preserved. Empty string
// returns the compiled default set (ResearchVerdictLabels) so behavior is
// unchanged when unset. Validates: array non-empty; each verdict and label
// non-empty; verdict tokens unique; no verdict token contains whitespace;
// no verdict token is the reserved "blocked" escape-hatch status.
func ParseResearchVerdicts(s string) (VerdictLabels, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ResearchVerdictLabels(), nil
	}

	var raw []struct {
		Verdict     string `json:"verdict"`
		Label       string `json:"label"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return VerdictLabels{}, fmt.Errorf("parse RESEARCH_VERDICTS: %w", err)
	}
	if len(raw) == 0 {
		return VerdictLabels{}, fmt.Errorf("parse RESEARCH_VERDICTS: must contain at least one entry")
	}

	seen := make(map[string]bool, len(raw))
	entries := make([]VerdictLabel, 0, len(raw))
	for i, r := range raw {
		if r.Verdict == "" {
			return VerdictLabels{}, fmt.Errorf("parse RESEARCH_VERDICTS: entry %d: verdict must not be empty", i)
		}
		if r.Label == "" {
			return VerdictLabels{}, fmt.Errorf("parse RESEARCH_VERDICTS: entry %d (verdict %q): label must not be empty", i, r.Verdict)
		}
		if strings.ContainsAny(r.Verdict, " \t\n\r\v\f") {
			return VerdictLabels{}, fmt.Errorf("parse RESEARCH_VERDICTS: entry %d: verdict %q must not contain whitespace", i, r.Verdict)
		}
		if r.Verdict == blockedVerdict {
			return VerdictLabels{}, fmt.Errorf("parse RESEARCH_VERDICTS: entry %d: verdict %q is reserved for the crash/no-verdict escape hatch", i, r.Verdict)
		}
		if seen[r.Verdict] {
			return VerdictLabels{}, fmt.Errorf("parse RESEARCH_VERDICTS: duplicate verdict token %q", r.Verdict)
		}
		seen[r.Verdict] = true
		entries = append(entries, VerdictLabel{
			Verdict:     Verdict(r.Verdict),
			Label:       r.Label,
			Description: r.Description,
		})
	}
	return NewVerdictLabels(entries...), nil
}
