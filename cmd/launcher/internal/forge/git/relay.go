package git

import (
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
)

// RelayBundle imports ref from outboxDir/seambundle.FileName into a fresh
// clone of the remote and force-pushes it with the host credential. An absent
// bundle returns forge.ErrBundleNotFound, the benign "Box wrote nothing" case.
// Git has no PRs, so gitClient deliberately stops short of
// forge.DraftPRCreator and forge.BundleCommitSubjects: the PR pair moves
// together, and OutboxRelayCapable follows DraftPRCreator.
func (g *gitClient) RelayBundle(outboxDir, ref string) error {
	return bundlerelay.Relay("git", outboxDir, ref, g.cred, func(dir string) error {
		return cloneInto(g.remoteURL, g.cred, dir, g.cloneTimeout)
	})
}

var _ forge.BundleRelay = (*gitClient)(nil)
