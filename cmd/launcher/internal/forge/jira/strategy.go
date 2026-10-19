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
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return err
		}
		return ValidateJiraEnv(k.JiraBaseURL, k.JiraProjectKey, k.JiraToken, k.JiraStatusMapping)
	},
	NewIssueTracker: func(s backendkit.Shared, l backendkit.Lookup) (forge.IssueTracker, error) {
		k, err := ReadKnobs(Lookup(l))
		if err != nil {
			return nil, err
		}
		mapping := k.JiraStatusMapping
		// The status mapping covers the work lifecycle only; research states
		// ride the label fallback (ADR 0022, issue #4919). Mirrors
		// trackerbuild.JiraStatusMappingFor until trackerbuild is deleted (ADR 0068).
		// TestKindAxesGuard requires a switch on a descriptor tag, not ==.
		switch s.Tracker {
		case dispatchkind.TrackerResearch:
			mapping = ""
		}
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
