package forge

import (
	"encoding/json"
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/outcome"
)

// Verdict is the research dispatch's relevance judgment (ADR 0022). It is data
// the Complete transition carries, not a lifecycle state, so kinds still share
// the four canonical DispatchState values. It is a string rather than a closed
// enum because an operator can replace the vocabulary through
// RESEARCH_VERDICTS (issue #2201).
type Verdict string

const (
	Recommend Verdict = Verdict(outcome.StatusRecommend)
	Reject    Verdict = Verdict(outcome.StatusReject)
	Unclear   Verdict = Verdict(outcome.StatusUnclear)
)

// String renders the verdict as the outcome-line status token.
func (v Verdict) String() string { return string(v) }

// VerdictLabel is one entry of the research verdict vocabulary: a token, the
// issue-tracker label its Complete transition swaps to, and the meaning
// rendered into the prompt.
type VerdictLabel struct {
	Verdict     Verdict
	Label       string
	Description string
}

// VerdictLabels is the ordered verdict-to-label mapping the research kind's
// Complete transition swaps to, where the work kind has a single Complete
// label. The zero value is inert: Empty is true and Label returns "".
type VerdictLabels struct {
	entries []VerdictLabel
}

// NewVerdictLabels builds a VerdictLabels from an ordered list of entries.
func NewVerdictLabels(entries ...VerdictLabel) VerdictLabels {
	return VerdictLabels{entries: entries}
}

// Label returns the native label string for verdict, or "" if it is not in the set.
func (v VerdictLabels) Label(verdict Verdict) string {
	for _, e := range v.entries {
		if e.Verdict == verdict {
			return e.Label
		}
	}
	return ""
}

// Parse parses an outcome-line status token into a Verdict. ok is false unless
// status matches a configured entry, and research settle maps a false ok to
// Failed instead of a Complete-with-verdict transition.
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

// ResearchDispatchLabels returns the fixed github research label family (ADR
// 0022). agent-research-failed means only that the Box crashed or produced no
// verdict. The research CI workflow and prompt hardcode these names, so unlike
// the work kind's DispatchLabels they are not operator-configurable. Complete
// is blank because ResearchVerdictLabels carries that transition.
func ResearchDispatchLabels() DispatchLabels {
	return DispatchLabels{
		Dispatchable: "agent-research",
		InProgress:   "agent-research-in-progress",
		Failed:       "agent-research-failed",
	}
}

// ResearchVerdictLabels returns the compiled-default verdict terminals (ADR
// 0022), which ParseResearchVerdicts falls back to when RESEARCH_VERDICTS is
// unset.
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

// blockedVerdict means the researcher reached no verdict at all, so it is
// reserved and can never be a configured verdict token.
const blockedVerdict = outcome.StatusBlocked

// ParseResearchVerdicts parses the RESEARCH_VERDICTS knob, a JSON array of
// {verdict,label,description} objects whose order it preserves. An empty
// string returns ResearchVerdictLabels so behavior is unchanged when unset.
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
