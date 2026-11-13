package jira

import (
	"net/http"
	"net/url"

	"spindrift.dev/launcher/internal/forge"
)

// CheckMappedStatuses implements forge.StatusMappingChecker: it reads the
// project's workflow statuses and returns the mapped ones it lacks. Statuses
// compare case-insensitively (lowercased), as the transition match does.
func (j *jiraClient) CheckMappedStatuses() ([]string, error) {
	var mapped []string
	for _, target := range j.cfg.StatusMapping {
		if target.Status != "" {
			mapped = append(mapped, target.Status)
		}
	}
	if len(mapped) == 0 {
		return nil, nil
	}
	// The endpoint lists statuses per issue type; any type's workflow counts.
	var types []struct {
		Statuses []struct {
			Name string `json:"name"`
		} `json:"statuses"`
	}
	if err := j.rest.Do(http.MethodGet, "/rest/api/2/project/"+url.PathEscape(j.cfg.ProjectKey)+"/statuses", nil, &types); err != nil {
		return nil, err
	}
	var have []string
	for _, ty := range types {
		for _, s := range ty.Statuses {
			have = append(have, s.Name)
		}
	}
	return forge.MissingStatuses(mapped, have), nil
}

var _ forge.StatusMappingChecker = (*jiraClient)(nil)
