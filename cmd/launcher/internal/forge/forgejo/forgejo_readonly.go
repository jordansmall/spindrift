package forgejo

import (
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
)

// readOnlyCodeForge adds forge.BundleRelay and forge.BundleCommitSubjects to
// *forgejoCodeForge, so settle's generic type assertion (ready.go) keys off
// which constructor built the adapter rather than a runtime mode check.
// NewForgejoCodeForge (read-write, the Box pushes in-box) must never satisfy
// them, or settle would relay a bundle nobody wrote and block every land.
// forge.DraftPRCreator and forge.BranchPusher live on the embedded
// *forgejoCodeForge instead (issue #4071) -- read-write and read-only alike
// get them, and readOnlyCodeForge picks them up by promotion.
type readOnlyCodeForge struct {
	*forgejoCodeForge
}

// NewReadOnlyForgejoCodeForge returns the Forgejo adapter for
// BOX_FORGE_AND_ISSUE_ACCESS=read-only: NewForgejoCodeForge plus RelayBundle
// and CommitSubjects, the host-mediated hand-off for a Box that cannot push
// itself (issues #1918, #1919).
func NewReadOnlyForgejoCodeForge(cfg ForgejoCodeForgeConfig, tracker forge.IssueTracker) forge.CodeForge {
	cf := NewForgejoCodeForge(cfg, tracker).(*forgejoCodeForge)
	return &readOnlyCodeForge{forgejoCodeForge: cf}
}

// NewReadOnlyForgejoCodeForgeForTest mirrors NewForgejoCodeForgeForTest for
// the read-only wrapper.
func NewReadOnlyForgejoCodeForgeForTest(cfg ForgejoCodeForgeConfig, tracker forge.IssueTracker, gitRemoteURL string) forge.CodeForge {
	cf := newForgejoCodeForge(cfg, tracker, gitRemoteURL)
	return &readOnlyCodeForge{forgejoCodeForge: cf}
}

// RelayBundle imports ref from outboxDir/seambundle.FileName into a fresh
// clone of the target repo and force-pushes it to origin. An absent bundle
// returns forge.ErrBundleNotFound, the benign "Box wrote nothing" case.
// A present bundle that is unreadable or fails verification returns a generic
// error, so a broken hand-off blocks the seam instead of landing nothing.
func (c *readOnlyCodeForge) RelayBundle(outboxDir, ref string) error {
	return bundlerelay.Relay("forgejo", outboxDir, ref, c.relayClone("relay bundle"))
}

// CommitSubjects returns the one-line commit subjects the bundle at
// outboxDir/seambundle.FileName carries for ref, relative to base, oldest
// first, for settle's read-only PR-intent fallback (issue #2447). Unlike
// RelayBundle it never checks anything out or pushes, so it cannot mutate the
// remote.
func (c *readOnlyCodeForge) CommitSubjects(outboxDir, base, ref string) ([]string, error) {
	return bundlerelay.CommitSubjects("forgejo", outboxDir, base, ref, c.relayClone("commit subjects"))
}

var _ forge.BundleRelay = (*readOnlyCodeForge)(nil)
var _ forge.BundleCommitSubjects = (*readOnlyCodeForge)(nil)
