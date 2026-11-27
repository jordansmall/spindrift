package github

import (
	"spindrift.dev/launcher/internal/forge"
)

// RelayBundle imports ref from the bundle in outboxDir into a fresh clone and
// force-pushes it to origin. A missing bundle returns forge.ErrBundleNotFound,
// the benign "Box wrote nothing" case; a bundle that is present but unreadable
// or fails `git bundle verify` returns an error so a broken hand-off blocks
// the seam (issue #2096).
func (c *execClient) RelayBundle(outboxDir, ref string) error {
	row, err := routeRow(c.connectors)
	if err != nil {
		return err
	}
	return c.relayBundle(row, outboxDir, ref)
}

// CommitSubjects returns the bundle's one-line commit subjects for ref
// relative to base, oldest first, for settle's PR-intent fallback
// (issue #2447). It never checks anything out or pushes, so it cannot mutate
// the remote.
func (c *execClient) CommitSubjects(outboxDir, base, ref string) ([]string, error) {
	row, err := routeRow(c.connectors)
	if err != nil {
		return nil, err
	}
	return c.commitSubjects(row, outboxDir, base, ref)
}

var _ forge.BundleCommitSubjects = (*execClient)(nil)
var _ forge.BundleRelay = (*execClient)(nil)
