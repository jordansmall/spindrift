package jira

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

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
	have := map[string]bool{}
	for _, ty := range types {
		for _, s := range ty.Statuses {
			have[strings.ToLower(s.Name)] = true
		}
	}
	slices.Sort(mapped) // map order is random; sorting first keeps the spelling kept on a case-only dedup stable
	var missing []string
	for _, status := range mapped {
		if key := strings.ToLower(status); !have[key] {
			have[key] = true // report a case-only duplicate once
			missing = append(missing, status)
		}
	}
	return missing, nil
}

var _ forge.StatusMappingChecker = (*jiraClient)(nil)
