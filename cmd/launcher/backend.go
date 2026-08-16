package main

import (
	"fmt"
	"io"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/forge/github"
	"spindrift.dev/launcher/internal/forge/jira"
	"spindrift.dev/launcher/internal/forge/local"
)

// backendRow is one registry entry for a single named backend: everything
// the launcher's per-axis name switches used to inline -- axis validity/
// validation, constructors, token knobs (ADR 0038), doctor hints, the
// read-only token gate, and the capability fields the mount/box/outcome-
// backstop sites key off instead of comparing names (extending the
// PRForge capability-by-assertion precedent, issue #2267). A nil/zero field
// means this backend doesn't participate in that axis/capability -- e.g.
// jira's newCodeForge is nil since jira is a tracker only.
type backendRow struct {
	backend.Descriptor

	// validateTracker/validateCodeForge run only when this row is the
	// active ISSUE_TRACKER/CODE_FORGE selection, beyond the bare axis
	// membership check; nil means no extra validation beyond membership.
	validateTracker   func(c config) error
	validateCodeForge func(c config) error

	newIssueTracker      func(c config) forge.IssueTracker
	newCodeForge         func(c config, parent local.SanitizedParent, it forge.IssueTracker) forge.CodeForge
	newReadOnlyCodeForge func(c config, parent local.SanitizedParent, it forge.IssueTracker) forge.CodeForge

	// boxTokenEnvVar is this backend's ADR 0016 Box-side token override
	// name; empty when the backend carries no bearer token (git, local).
	boxTokenEnvVar string

	// readOnlyTokenGate enforces BOX_FORGE_AND_ISSUE_ACCESS=read-only's
	// token-distinctness gate for this backend's token; nil when the
	// backend carries no such gate today (jira, local, git).
	readOnlyTokenGate func(c config, w io.Writer) (bool, error)
	// readOnlyGateOkMessage renders the doctor success line for this gate;
	// nil iff readOnlyTokenGate is nil.
	readOnlyGateOkMessage func(verified bool) string

	// outboxRelayCapable is true for a backend whose CODE_FORGE selection
	// gets the outbox mount/relay treatment under read-only today (issue
	// #1918: "github" only). NOTE: forgejo also has its own read-only
	// CodeForge constructor (NewReadOnlyForgejoCodeForge) but is NOT
	// included in this today -- that's a pre-existing asymmetry in the
	// current code (mount.go / dispatch/box.go's needsOutbox / the
	// outcome-backstop switch all check CodeForge=="github" specifically,
	// never "forgejo", for this one capability), and #2267 is explicitly
	// behavior-preserving, so this field must reproduce that asymmetry
	// (true for github, false for forgejo) rather than "fixing" it.
	outboxRelayCapable bool
	// inBoxUnreachableTracker is true only for a tracker with no in-box
	// reachability at all (ADR 0032: "local"), gating the read-only
	// /issues mount.
	inBoxUnreachableTracker bool
}

// forgejoCodeForgeConfig builds the forgejo.ForgejoCodeForgeConfig shared by
// both the read-write and read-only forgejo CodeForge constructors, so the
// struct literal isn't duplicated between them.
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

// backendRows is the registry of every named backend the launcher's
// ISSUE_TRACKER/CODE_FORGE knobs select among. validate, newIssueTracker,
// newCodeForge, boxTokenResolver, reportReadOnlyTokenGates, runDoctor's hint
// lookup, runnerConfig, and dispatchConfig all resolve their per-backend
// behavior through backendByName/this slice instead of a name switch.
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

		readOnlyTokenGate: func(c config, w io.Writer) (bool, error) {
			return checkReadOnlyTokenGate(c, ghTokenIntrospector, w)
		},
		readOnlyGateOkMessage: func(verified bool) string {
			if verified {
				return "ok: read-only token gate satisfied — BOX_GH_TOKEN is set, distinct, and confirmed not write-capable"
			}
			return "ok: read-only token gate satisfied — BOX_GH_TOKEN is set and distinct (see warning above: its write capability could not be verified)"
		},

		outboxRelayCapable: true,
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

		readOnlyTokenGate: func(c config, w io.Writer) (bool, error) {
			return checkReadOnlyForgejoTokenGate(c, w)
		},
		readOnlyGateOkMessage: func(_ bool) string {
			return "ok: read-only token gate satisfied — BOX_FORGEJO_TOKEN is set and distinct (see warning above: its write capability could not be verified — Forgejo exposes no introspection endpoint)"
		},

		outboxRelayCapable: false,
	},
	{
		Descriptor: backend.Jira,

		validateTracker: func(c config) error {
			return jira.ValidateJiraEnv(c.jiraBaseURL, c.jiraProjectKey, c.jiraToken, c.jiraStatusMapping)
		},

		newIssueTracker: func(c config) forge.IssueTracker {
			statusMapping, err := jira.ParseStatusMapping(c.jiraStatusMapping)
			if err != nil {
				// validate() already rejects a malformed mapping before this is
				// reached; treat it as unmapped (label-only lifecycle) as a
				// fallback.
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

		inBoxUnreachableTracker: true,
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

// backendByName looks up the registry row for name (an ISSUE_TRACKER or
// CODE_FORGE knob value). ok is false for an unregistered name.
func backendByName(name string) (backendRow, bool) {
	for _, r := range backendRows {
		if r.Name == name {
			return r, true
		}
	}
	return backendRow{}, false
}

// validTrackerNames returns the Name of every backendRows entry valid as an
// ISSUE_TRACKER, in backendRows' declaration order. Unlike validateChoice
// (schema-static, generated at build time), this reads the package-level
// backendRows slice directly, so a row appended to it at runtime -- the
// issue #2267 AC5 extensibility guarantee
// (TestBackendRegistry_NewBackendNeedsOnlyRowAndNoOtherChanges) -- is
// reflected immediately, including in validate()'s ISSUE_TRACKER-invalid
// error message.
func validTrackerNames() []string {
	var names []string
	for _, r := range backendRows {
		if r.ValidAsTracker {
			names = append(names, r.Name)
		}
	}
	return names
}

// validCodeForgeNames returns the Name of every backendRows entry valid as a
// CODE_FORGE, in backendRows' declaration order. See validTrackerNames for
// why this reads backendRows directly instead of routing through
// validateChoice.
func validCodeForgeNames() []string {
	var names []string
	for _, r := range backendRows {
		if r.ValidAsCodeForge {
			names = append(names, r.Name)
		}
	}
	return names
}
