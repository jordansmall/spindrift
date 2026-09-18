package main

import (
	"fmt"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/forge/github"
	"spindrift.dev/launcher/internal/forge/jira"
	"spindrift.dev/launcher/internal/forge/local"
)

// backendRow is one registry entry for a named backend: axis validity and
// validation, constructors, token knobs (ADR 0038), and the capability fields
// callers key off instead of comparing names (issue #2267). A nil or zero
// field means this backend does not participate in that axis or capability;
// jira's newCodeForge is nil because jira is a tracker only.
type backendRow struct {
	backend.Descriptor

	// validateTracker and validateCodeForge run only when this row is the
	// active ISSUE_TRACKER or CODE_FORGE selection; nil means no validation
	// beyond axis membership.
	validateTracker   func(c config) error
	validateCodeForge func(c config) error

	newIssueTracker      func(c config) forge.IssueTracker
	newCodeForge         func(c config, parent local.SanitizedParent, it forge.IssueTracker) forge.CodeForge
	newReadOnlyCodeForge func(c config, parent local.SanitizedParent, it forge.IssueTracker) forge.CodeForge

	// boxTokenEnvVar is the ADR 0016 Box-side token override name; empty when
	// the backend carries no bearer token (git, local).
	boxTokenEnvVar string
}

func forgejoCodeForgeConfig(c config) forgejo.ForgejoCodeForgeConfig {
	return forgejo.ForgejoCodeForgeConfig{
		BaseURL:      c.forgejoBaseURL,
		Repo:         c.repoSlug,
		Token:        c.forgejoToken,
		BaseBranch:   c.baseBranch,
		UserName:     c.gitUserName,
		UserEmail:    c.gitUserEmail,
		BranchPrefix: c.branchPrefix,
		MergeMethod:  c.mergeMethod,
	}
}

// backendRows is the registry of every named backend the ISSUE_TRACKER and
// CODE_FORGE knobs select among; callers resolve per-backend behavior through
// backendByName instead of a name switch. The read-only token gates are the
// one exception: gateRegistry (launchgates.go, issue #2942) hardcodes the
// github and forgejo gates rather than walking this slice.
var backendRows = []backendRow{
	{
		Descriptor: backend.GitHub,

		newIssueTracker: func(c config) forge.IssueTracker {
			return github.NewExecClient(c.repoSlug, dispatchLabels(c), c.branchPrefix, github.WithVerdictLabels(researchVerdictLabels(c)))
		},
		newCodeForge: func(c config, _ local.SanitizedParent, _ forge.IssueTracker) forge.CodeForge {
			return github.NewExecClient(c.repoSlug, dispatchLabels(c), c.branchPrefix, github.WithMergeMethod(c.mergeMethod), github.WithSyncMethod(c.syncMethod))
		},
		newReadOnlyCodeForge: func(c config, _ local.SanitizedParent, _ forge.IssueTracker) forge.CodeForge {
			return github.NewReadOnlyCodeForge(c.repoSlug, dispatchLabels(c), c.branchPrefix, github.WithMergeMethod(c.mergeMethod), github.WithSyncMethod(c.syncMethod))
		},

		boxTokenEnvVar: "BOX_GH_TOKEN",
	},
	{
		Descriptor: backend.Forgejo,

		validateTracker: func(c config) error {
			return forgejo.ValidateForgejoEnv(c.forgejoBaseURL, c.forgejoToken)
		},
		validateCodeForge: func(c config) error {
			return forgejo.ValidateForgejoEnv(c.forgejoBaseURL, c.forgejoToken)
		},

		newIssueTracker: func(c config) forge.IssueTracker {
			return forgejo.NewForgejoClient(forgejo.ForgejoConfig{
				BaseURL:       c.forgejoBaseURL,
				Repo:          c.repoSlug,
				Token:         c.forgejoToken,
				Labels:        dispatchLabels(c),
				VerdictLabels: researchVerdictLabels(c),
			})
		},
		newCodeForge: func(c config, _ local.SanitizedParent, it forge.IssueTracker) forge.CodeForge {
			return forgejo.NewForgejoCodeForge(forgejoCodeForgeConfig(c), it)
		},
		newReadOnlyCodeForge: func(c config, _ local.SanitizedParent, it forge.IssueTracker) forge.CodeForge {
			return forgejo.NewReadOnlyForgejoCodeForge(forgejoCodeForgeConfig(c), it)
		},

		boxTokenEnvVar: "BOX_FORGEJO_TOKEN",
	},
	{
		Descriptor: backend.Jira,

		validateTracker: func(c config) error {
			return jira.ValidateJiraEnv(c.jiraBaseURL, c.jiraProjectKey, c.jiraToken, c.jiraStatusMapping)
		},

		newIssueTracker: func(c config) forge.IssueTracker {
			statusMapping, err := jira.ParseStatusMapping(c.jiraStatusMapping)
			if err != nil {
				// validate() already rejects a malformed mapping, so fall back
				// to unmapped (label-only lifecycle).
				statusMapping = map[forge.DispatchState]string{}
			}
			return jira.NewJiraClient(jira.JiraConfig{
				BaseURL:         c.jiraBaseURL,
				ProjectKey:      c.jiraProjectKey,
				Email:           c.jiraEmail,
				Token:           c.jiraToken,
				StatusMapping:   statusMapping,
				Labels:          dispatchLabels(c),
				VerdictLabels:   researchVerdictLabels(c),
				IncludeComments: c.jiraIncludeComments,
			})
		},
	},
	{
		Descriptor: backend.Local,

		validateCodeForge: func(c config) error {
			if c.mergeMode != "immediate" {
				return fmt.Errorf(
					"CODE_FORGE=local requires MERGE_MODE=immediate (got %q) — "+
						"only immediate relays the seam bundle into the Accumulation "+
						"repo; manual/auto strand it in the outbox", c.mergeMode)
			}
			return nil
		},

		newIssueTracker: func(c config) forge.IssueTracker {
			return local.NewLocalTracker(c.localIssuesDir, dispatchLabels(c), researchVerdictLabels(c))
		},
		newCodeForge: func(c config, parent local.SanitizedParent, _ forge.IssueTracker) forge.CodeForge {
			return local.NewLocalCodeForge(c.codeForgeAccumulationRepoDir, c.baseBranch, parent, c.gitUserName, c.gitUserEmail, c.branchPrefix)
		},
	},
	{
		Descriptor: backend.Git,

		validateCodeForge: func(c config) error {
			if c.codeForgeRemoteURL == "" {
				return fmt.Errorf("set CODE_FORGE_REMOTE_URL (the plain git remote to clone from and push to) when CODE_FORGE=git")
			}
			return nil
		},

		newCodeForge: func(c config, _ local.SanitizedParent, _ forge.IssueTracker) forge.CodeForge {
			return git.NewGitClient(c.codeForgeRemoteURL, c.baseBranch, c.gitUserName, c.gitUserEmail, c.branchPrefix)
		},
	},
}

// backendByName looks up the registry row for an ISSUE_TRACKER or CODE_FORGE
// knob value; ok is false for an unregistered name.
func backendByName(name string) (backendRow, bool) {
	for _, r := range backendRows {
		if r.Name == name {
			return r, true
		}
	}
	return backendRow{}, false
}

// validTrackerNames returns every ISSUE_TRACKER-valid name in backendRows'
// declaration order. It reads backendRows directly rather than validateChoice,
// which is generated at build time, so a row appended at runtime shows up
// immediately (the issue #2267 AC5 extensibility guarantee).
func validTrackerNames() []string {
	var names []string
	for _, r := range backendRows {
		if r.ValidAsTracker {
			names = append(names, r.Name)
		}
	}
	return names
}

// validCodeForgeNames returns every CODE_FORGE-valid name in backendRows'
// declaration order. It reads backendRows directly for the same reason
// validTrackerNames does.
func validCodeForgeNames() []string {
	var names []string
	for _, r := range backendRows {
		if r.ValidAsCodeForge {
			names = append(names, r.Name)
		}
	}
	return names
}
