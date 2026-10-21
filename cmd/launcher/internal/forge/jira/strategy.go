package jira

import (
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
)

// Strategy is the jira backend's Go half (ADR 0068).
var Strategy = backendkit.Strategy{
	Descriptor: backend.Jira,
	ValidateTracker: func(_ backendkit.Shared, l backendkit.Lookup) error {
		k, err := ReadKnobs(l)
		if err != nil {
			return err
		}
		return ValidateJiraEnv(k.JiraBaseURL, k.JiraProjectKey, k.JiraToken, k.JiraStatusMapping)
	},
	NewIssueTracker: func(s backendkit.Shared, l backendkit.Lookup) (forge.IssueTracker, error) {
		k, err := ReadKnobs(l)
		if err != nil {
			return nil, err
		}
		mapping := statusMappingFor(s.Tracker, k.JiraStatusMapping)
		statusMapping, err := ParseStatusMapping(mapping)
		if err != nil {
			// Callers validate the mapping first, so fall back to unmapped
			// (label-only lifecycle).
			statusMapping = map[forge.DispatchState]string{}
		}
		return NewJiraClient(JiraConfig{
			BaseURL:       k.JiraBaseURL,
			ProjectKey:    k.JiraProjectKey,
			Email:         k.JiraEmail,
			Token:         k.JiraToken,
			StatusMapping: statusMapping,
			Labels:        s.Labels,
			VerdictLabels: s.VerdictLabels,
		}), nil
	},
}

// statusMappingFor is the JIRA_STATUS_MAPPING the tracker instance t is built
// with. The mapping covers the work lifecycle only; research states always ride
// the label fallback (ADR 0022, issue #4919), so the research tracker gets none.
func statusMappingFor(t dispatchkind.Tracker, mapping string) string {
	// TestKindAxesGuard requires a switch on a descriptor tag, not ==.
	switch t {
	case dispatchkind.TrackerResearch:
		return ""
	}
	return mapping
}
