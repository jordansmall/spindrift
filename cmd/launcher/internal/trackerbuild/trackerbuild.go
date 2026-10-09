// Package trackerbuild is the one construction path for an Issue Tracker
// adapter (issue #4592). The launcher builds its tracker from its parsed
// config and the daemon, a separate package main that cannot reach the
// launcher's backend registry, builds one from raw env values; both go
// through a Settings value here so the two cannot drift on which adapter an
// ISSUE_TRACKER name yields. Validation is the daemon's; the launcher
// validates through its own backend registry.
package trackerbuild

import (
	"fmt"
	"net/http"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/github"
	"spindrift.dev/launcher/internal/forge/jira"
	"spindrift.dev/launcher/internal/forge/local"
)

// Settings carries every value an Issue Tracker adapter is built from, so a
// caller without the launcher's config (the daemon) builds the same adapter.
type Settings struct {
	RepoSlug       string
	BranchPrefix   string
	LocalIssuesDir string

	ForgejoBaseURL string
	ForgejoToken   string

	JiraBaseURL       string
	JiraProjectKey    string
	JiraEmail         string
	JiraToken         string
	JiraStatusMapping string
	// JiraHTTPClient nil leaves the adapter's default client.
	JiraHTTPClient *http.Client

	Labels        forge.DispatchLabels
	VerdictLabels forge.VerdictLabels
}

// Tracker is one ISSUE_TRACKER backend's construction.
type Tracker struct {
	// Validate reports the first setting New cannot build a working adapter
	// without. Only the daemon calls it.
	Validate func(Settings) error
	New      func(Settings) forge.IssueTracker
}

// GitHub builds the gh-CLI-backed tracker.
var GitHub = Tracker{
	Validate: func(s Settings) error {
		return requireSlug(s, backend.GitHub.Name)
	},
	New: func(s Settings) forge.IssueTracker {
		return github.NewExecClient(s.RepoSlug, s.Labels, s.BranchPrefix, github.WithVerdictLabels(s.VerdictLabels))
	},
}

// Forgejo builds the Forgejo REST tracker.
var Forgejo = Tracker{
	Validate: func(s Settings) error {
		if err := forgejo.ValidateForgejoEnv(s.ForgejoBaseURL, s.ForgejoToken); err != nil {
			return err
		}
		return requireSlug(s, backend.Forgejo.Name)
	},
	New: func(s Settings) forge.IssueTracker {
		return forgejo.NewForgejoClient(forgejo.ForgejoConfig{
			BaseURL:       s.ForgejoBaseURL,
			Repo:          s.RepoSlug,
			Token:         s.ForgejoToken,
			Labels:        s.Labels,
			VerdictLabels: s.VerdictLabels,
		})
	},
}

// Jira builds the Jira REST tracker.
var Jira = Tracker{
	Validate: func(s Settings) error {
		return jira.ValidateJiraEnv(s.JiraBaseURL, s.JiraProjectKey, s.JiraToken, s.JiraStatusMapping)
	},
	New: func(s Settings) forge.IssueTracker {
		statusMapping, err := jira.ParseStatusMapping(s.JiraStatusMapping)
		if err != nil {
			// Callers validate the mapping first (Validate, or the launcher's
			// validateTracker), so fall back to unmapped (label-only lifecycle).
			statusMapping = map[forge.DispatchState]string{}
		}
		return jira.NewJiraClient(jira.JiraConfig{
			BaseURL:       s.JiraBaseURL,
			ProjectKey:    s.JiraProjectKey,
			Email:         s.JiraEmail,
			Token:         s.JiraToken,
			StatusMapping: statusMapping,
			Labels:        s.Labels,
			VerdictLabels: s.VerdictLabels,
			HTTPClient:    s.JiraHTTPClient,
		})
	},
}

// Local builds the directory-backed tracker.
var Local = Tracker{
	Validate: func(s Settings) error {
		if s.LocalIssuesDir == "" {
			return fmt.Errorf("set LOCAL_ISSUES_DIR when ISSUE_TRACKER=%s", backend.Local.Name)
		}
		return nil
	},
	New: func(s Settings) forge.IssueTracker {
		return local.NewLocalTracker(s.LocalIssuesDir, s.Labels, s.VerdictLabels)
	},
}

// ByName resolves an ISSUE_TRACKER value; ok is false for an unknown name or
// a backend that is not a tracker (git).
func ByName(name string) (Tracker, bool) {
	switch name {
	case backend.GitHub.Name:
		return GitHub, true
	case backend.Forgejo.Name:
		return Forgejo, true
	case backend.Jira.Name:
		return Jira, true
	case backend.Local.Name:
		return Local, true
	}
	return Tracker{}, false
}

func requireSlug(s Settings, name string) error {
	if s.RepoSlug == "" {
		return fmt.Errorf("set REPO_SLUG when ISSUE_TRACKER=%s", name)
	}
	return nil
}

// JiraStatusMappingFor is the JIRA_STATUS_MAPPING the tracker instance t is
// built with. The mapping covers the work lifecycle only;
// research states always ride the label fallback (ADR 0022, issue #4919), so
// the research tracker gets none.
func JiraStatusMappingFor(t dispatchkind.Tracker, mapping string) string {
	if t == dispatchkind.TrackerResearch {
		return ""
	}
	return mapping
}
