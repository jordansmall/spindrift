package github

import (
	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
)

// Strategy is the github backend's Go half (ADR 0068). It has no knobs, so
// its closures ignore the Lookup.
var Strategy = backendkit.Strategy{
	Descriptor: backend.GitHub,
	NewIssueTracker: func(s backendkit.Shared, _ backendkit.Lookup) (forge.IssueTracker, error) {
		return NewExecClient(s.RepoSlug, s.Labels, s.BranchPrefix, WithVerdictLabels(s.VerdictLabels)), nil
	},
	NewCodeForge: func(s backendkit.Shared, _ backendkit.Lookup, _ forge.SanitizedParent, _ forge.IssueTracker) (forge.CodeForge, error) {
		return NewExecClient(s.RepoSlug, s.Labels, s.BranchPrefix, codeForgeOpts(s)...), nil
	},
	NewReadOnlyCodeForge: func(s backendkit.Shared, _ backendkit.Lookup, _ forge.SanitizedParent, _ forge.IssueTracker) (forge.CodeForge, error) {
		return NewReadOnlyCodeForge(s.RepoSlug, s.Labels, s.BranchPrefix, codeForgeOpts(s)...), nil
	},
	NewLedger: func(s backendkit.Shared, _ backendkit.Lookup) (backendkit.LedgerSite, error) {
		url, gitArgs := GitRemote(s.RepoSlug)
		return backendkit.LedgerSite{URL: url, GitArgs: gitArgs}, nil
	},
}

func codeForgeOpts(s backendkit.Shared) []ExecOption {
	return []ExecOption{
		WithMergeMethod(s.MergeMethod),
		WithSyncMethod(s.SyncMethod),
		WithCommitIdentity(s.GitUserName, s.GitUserEmail),
	}
}
