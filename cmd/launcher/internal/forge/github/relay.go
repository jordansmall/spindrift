package github

import (
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
)

// readOnlyCodeForge exists so that only the read-only constructor satisfies
// forge.BundleRelay and forge.BundleCommitSubjects, which settle type-asserts
// (ready.go): a read-write Box pushes and opens its own PR in-box, so settle
// must never relay a bundle or reconstruct commit subjects for it. Both
// forge.DraftPRCreator and forge.BranchPusher live on the embedded
// *execClient instead (issue #4071) -- read-write and read-only alike get
// them, and readOnlyCodeForge picks them up by promotion.
type readOnlyCodeForge struct {
	*execClient
}

// NewReadOnlyCodeForge returns the gh-exec adapter for
// BOX_FORGE_AND_ISSUE_ACCESS=read-only. It adds the host-mediated bundle
// hand-off to NewExecClient, which a Box that cannot push needs (issue #1918).
func NewReadOnlyCodeForge(repo string, labels forge.DispatchLabels, branchPrefix string, opts ...ExecOption) forge.CodeForge {
	return &readOnlyCodeForge{execClient: NewExecClient(repo, labels, branchPrefix, opts...)}
}

// RelayBundle imports ref from the bundle in outboxDir into a fresh clone and
// force-pushes it to origin with the launcher's own gh-cli credential. A
// missing bundle returns forge.ErrBundleNotFound, the benign "Box wrote
// nothing" case; a bundle that is present but unreadable or fails `git bundle
// verify` returns an error so a broken hand-off blocks the seam (issue #2096).
func (c *readOnlyCodeForge) RelayBundle(outboxDir, ref string) error {
	return bundlerelay.Relay("github", outboxDir, ref, c.relayClone("relay bundle"))
}

// CommitSubjects returns the bundle's one-line commit subjects for ref
// relative to base, oldest first, for settle's read-only PR-intent fallback
// (issue #2447). It never checks anything out or pushes, so it cannot mutate
// the remote.
func (c *readOnlyCodeForge) CommitSubjects(outboxDir, base, ref string) ([]string, error) {
	return bundlerelay.CommitSubjects("github", outboxDir, base, ref, c.relayClone("commit subjects"))
}

// GitRemote builds the git remote URL and auth args for pushing straight to
// repo, as the butler Ledger does (issue #3876): the host follows GH_HOST
// (falling back to github.com) so a GitHub Enterprise Consumer lands on the
// same host every other gh path talks to. The empty credential.helper first
// resets any ambient helper, so the second -c is the only one in effect: the
// launcher's own gh credential, the same one RelayBundle authenticates with.
func GitRemote(repo string) (url string, gitArgs []string) {
	return "https://" + Host() + "/" + repo + ".git",
		[]string{"-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential"}
}

var _ forge.BundleCommitSubjects = (*readOnlyCodeForge)(nil)
var _ forge.BundleRelay = (*readOnlyCodeForge)(nil)
