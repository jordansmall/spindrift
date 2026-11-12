package git

import (
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
)

// readOnlyGitClient adds forge.BundleRelay to *gitClient for
// BOX_FORGE_AND_ISSUE_ACCESS=read-only, so settle's type assertion keys off
// which constructor built the adapter. Git has no PRs, so it deliberately
// stops short of forge.DraftPRCreator and forge.BundleCommitSubjects: the PR
// pair moves together, and OutboxRelayCapable follows DraftPRCreator.
type readOnlyGitClient struct {
	*gitClient
}

// NewReadOnlyGitClient returns NewGitClient plus RelayBundle, the
// host-mediated hand-off for a Box that cannot push itself (issue #6898).
func NewReadOnlyGitClient(remoteURL, baseBranch, userName, userEmail string, opts ...Option) forge.CodeForge {
	return &readOnlyGitClient{gitClient: NewGitClient(remoteURL, baseBranch, userName, userEmail, opts...).(*gitClient)}
}

// RelayBundle imports ref from outboxDir/seambundle.FileName into a fresh
// clone of the remote and force-pushes it with the host credential. An absent
// bundle returns forge.ErrBundleNotFound, the benign "Box wrote nothing" case.
func (g *readOnlyGitClient) RelayBundle(outboxDir, ref string) error {
	return bundlerelay.Relay("git", outboxDir, ref, g.cred, func(dir string) error {
		return cloneInto(g.remoteURL, g.cred, dir, g.cloneTimeout)
	})
}

var _ forge.BundleRelay = (*readOnlyGitClient)(nil)
