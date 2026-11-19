package forgejo

import (
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
)

// RelayBundle imports ref from outboxDir/seambundle.FileName into a fresh
// clone of the target repo and force-pushes it to origin. An absent bundle
// returns forge.ErrBundleNotFound, the benign "Box wrote nothing" case.
// A present bundle that is unreadable or fails verification returns a generic
// error, so a broken hand-off blocks the seam instead of landing nothing.
func (c *forgejoCodeForge) RelayBundle(outboxDir, ref string) error {
	return bundlerelay.Relay("forgejo", outboxDir, ref, c.cred, c.relayClone("relay bundle"))
}

// CommitSubjects returns the one-line commit subjects the bundle at
// outboxDir/seambundle.FileName carries for ref, relative to base, oldest
// first, for settle's PR-intent fallback (issue #2447). Unlike RelayBundle it
// never checks anything out or pushes, so it cannot mutate the remote.
func (c *forgejoCodeForge) CommitSubjects(outboxDir, base, ref string) ([]string, error) {
	return bundlerelay.CommitSubjects("forgejo", outboxDir, base, ref, c.relayClone("commit subjects"))
}

var _ forge.BundleRelay = (*forgejoCodeForge)(nil)
var _ forge.BundleCommitSubjects = (*forgejoCodeForge)(nil)
