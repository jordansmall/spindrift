package github

import (
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitremote"
)

// Strategy is the github backend's Go half (ADR 0068). It has no knobs, so
// its closures ignore the Lookup.
var Strategy = backendkit.Strategy{
	Descriptor: backend.GitHub,
	ValidateTracker: func(s backendkit.Shared, _ backendkit.Lookup) error {
		return backendkit.RequireRepoSlug(s, backend.GitHub.Name)
	},
	LandedBanner: func(s backendkit.Shared) string {
		return "branches pushed and PRs opened on " + s.RepoSlug
	},
	AutoMergeRemedy: func(backendkit.Shared) string {
		return `enable "Allow auto-merge" in repo Settings → General`
	},
	AcquireToken: acquireToken,
	NewIssueTracker: func(s backendkit.Shared, _ backendkit.Lookup) (forge.IssueTracker, error) {
		return NewExecClient(s.RepoSlug, s.Labels, s.BranchPrefix, WithVerdictLabels(s.VerdictLabels)), nil
	},
	NewCodeForge: func(s backendkit.Shared, _ backendkit.Lookup, _ forge.SanitizedParent, _ forge.IssueTracker) (forge.CodeForge, error) {
		return NewExecClient(s.RepoSlug, s.Labels, s.BranchPrefix, codeForgeOpts(s)...), nil
	},
	NewReadOnlyCodeForge: func(s backendkit.Shared, _ backendkit.Lookup, _ forge.SanitizedParent, _ forge.IssueTracker) (forge.CodeForge, error) {
		return NewReadOnlyCodeForge(s.RepoSlug, s.Labels, s.BranchPrefix, codeForgeOpts(s)...), nil
	},
	OpenPRForBranch: func(repoSlug, branch string) (bool, error) {
		_, open, err := NewExecClient(repoSlug, forge.DispatchLabels{}, "").OpenPRForBranch(branch)
		return open, err
	},
	NewLedger: func(s backendkit.Shared, _ backendkit.Lookup) (backendkit.LedgerSite, error) {
		url, gitArgs := GitRemote(s.RepoSlug)
		return backendkit.LedgerSite{URL: url, GitArgs: gitArgs}, nil
	},
	// The Box clones from literal github.com, not Host(): GH_HOST is the
	// host's gh setting and never reached the Box's clone.
	CloneURL: func(s backendkit.Shared, _ backendkit.Lookup, _ string) (string, error) {
		return "https://github.com/" + s.RepoSlug + ".git", nil
	},
	WebURL: func(s backendkit.Shared, _ backendkit.Lookup) string {
		if s.RepoSlug == "" {
			return ""
		}
		return "https://" + Host() + "/" + s.RepoSlug
	},
	RemoteMatch: func(s backendkit.Shared, _ backendkit.Lookup, remote string) bool {
		if s.RepoSlug == "" {
			return false
		}
		// ParseHostSlug strips any port, so a GH_HOST carrying one never
		// matches and the check skips itself.
		host, slug := gitremote.ParseHostSlug(remote)
		return strings.EqualFold(host, Host()) && strings.EqualFold(slug, s.RepoSlug)
	},
}

func codeForgeOpts(s backendkit.Shared) []ExecOption {
	return []ExecOption{
		WithMergeMethod(s.MergeMethod),
		WithSyncMethod(s.SyncMethod),
		WithCommitIdentity(s.GitUserName, s.GitUserEmail),
	}
}
