package jira

import (
	"fmt"

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
		return ValidateJiraEnv(k.JiraBaseURL, k.JiraProjectKey, k.JiraToken, k.JiraStatusMapping, k.JiraLifecycle)
	},
	IssueScope: func(sh backendkit.Shared, l backendkit.Lookup) string {
		k, err := ReadKnobs(l)
		if err != nil || k.JiraProjectKey == "" {
			return sh.RepoSlug
		}
		return fmt.Sprintf("Jira project %s (%s)", k.JiraProjectKey, k.JiraBaseURL)
	},
	IssueReadScope: func(_ backendkit.Shared, l backendkit.Lookup) string {
		k, err := ReadKnobs(l)
		if err != nil {
			return ""
		}
		return k.JiraProjectKey
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
			statusMapping = map[forge.DispatchState]StatusTarget{}
		}
		return NewJiraClient(JiraConfig{
			BaseURL:       k.JiraBaseURL,
			ProjectKey:    k.JiraProjectKey,
			IssueType:     k.JiraIssueType,
			Email:         k.JiraEmail,
			Token:         k.JiraToken,
			StatusMapping: statusMapping,
			StatusOnly:    statusOnlyFor(s.Tracker, k.JiraLifecycle),
			Labels:        s.Labels,
			VerdictLabels: s.VerdictLabels,
		}), nil
	},
}

// labelOnly reports whether tracker instance t rides labels alone: the work
// lifecycle's native statuses never apply to research (ADR 0022, 0072).
func labelOnly(t dispatchkind.Tracker) bool {
	// TestKindAxesGuard requires a switch on a descriptor tag, not ==.
	switch t {
	case dispatchkind.TrackerResearch:
		return true
	}
	return false
}

// statusOnlyFor reports whether tracker instance t runs the status-only work
// lifecycle. Research verdicts stay on labels in either mode (ADR 0072).
func statusOnlyFor(t dispatchkind.Tracker, lifecycle string) bool {
	return !labelOnly(t) && lifecycle == LifecycleStatus
}

// statusMappingFor is the JIRA_STATUS_MAPPING the tracker instance t is built
// with. The mapping covers the work lifecycle only; research states always ride
// the label fallback (ADR 0022, issue #4919), so the research tracker gets none.
func statusMappingFor(t dispatchkind.Tracker, mapping string) string {
	if labelOnly(t) {
		return ""
	}
	return mapping
}

// WorkLifecycleConfig is the native lifecycle policy core hands the jira
// Connector for one tracker instance: the status and resolution each mapped
// work state moves to, and whether the lifecycle is status-only.
type WorkLifecycleConfig struct {
	Statuses    map[forge.DispatchState]string
	Resolutions map[forge.DispatchState]string
	StatusOnly  bool
}

// WorkLifecycle builds the WorkLifecycleConfig for tracker instance t. Research
// gets the zero config (it stays on labels, decided from the kind row's
// Tracker axis). An unparseable mapping is an error; callers validate it at
// startup.
func WorkLifecycle(t dispatchkind.Tracker, mapping, lifecycle string) (WorkLifecycleConfig, error) {
	var cfg WorkLifecycleConfig
	targets, err := ParseStatusMapping(statusMappingFor(t, mapping))
	if err != nil {
		return WorkLifecycleConfig{}, err
	}
	for state, target := range targets {
		if target.Status == "" {
			continue
		}
		if cfg.Statuses == nil {
			cfg.Statuses, cfg.Resolutions = map[forge.DispatchState]string{}, map[forge.DispatchState]string{}
		}
		cfg.Statuses[state] = target.Status
		if target.Resolution != "" {
			cfg.Resolutions[state] = target.Resolution
		}
	}
	cfg.StatusOnly = statusOnlyFor(t, lifecycle)
	return cfg, nil
}
