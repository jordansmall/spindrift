package github

import (
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/bundlerelay"
	"spindrift.dev/launcher/internal/gitexec"
)

// RelayBundle imports ref from the bundle in outboxDir into a fresh clone and
// force-pushes it to origin over HTTPS, authenticated by the gh credential
// helper (see GitRemote), not gh's per-host git_protocol. A
// missing bundle returns forge.ErrBundleNotFound, the benign "Box wrote
// nothing" case; a bundle that is present but unreadable or fails `git bundle
// verify` returns an error so a broken hand-off blocks the seam (issue #2096).
func (c *execClient) RelayBundle(outboxDir, ref string) error {
	if row, ok := routedRow(); ok {
		return c.relayBundleViaConnector(row, outboxDir, ref)
	}
	return bundlerelay.Relay("github", outboxDir, ref, gitexec.Credential{}, c.httpsClone("relay bundle"))
}

// CommitSubjects returns the bundle's one-line commit subjects for ref
// relative to base, oldest first, for settle's PR-intent fallback
// (issue #2447). It never checks anything out or pushes, so it cannot mutate
// the remote.
func (c *execClient) CommitSubjects(outboxDir, base, ref string) ([]string, error) {
	if row, ok := routedRow(); ok {
		return c.commitSubjectsViaConnector(row, outboxDir, base, ref)
	}
	return bundlerelay.CommitSubjects("github", outboxDir, base, ref, c.httpsClone("commit subjects"))
}

// GitRemote builds the git remote URL and auth args for pushing straight to
// repo, as the butler Ledger does (issue #3876): the host follows GH_HOST
// (falling back to github.com) so a GitHub Enterprise Consumer lands on the
// same host every other gh path talks to. The empty credential.helper first
// resets any ambient helper, so the second -c is the only one in effect: the
// launcher's own gh credential, the same one httpsClone authenticates with.
//
// The identity insteadOf/pushInsteadOf rules pin the URL against an ambient
// `url.git@<host>:.insteadOf https://<host>/` (issue #4665): git applies only
// the longest matching prefix, and the full URL outranks any shorter ambient
// rule. An ambient rule on the exact same URL ties, and the rule git reads
// first wins: global config and GIT_CONFIG_COUNT beat these -c args on the
// Ledger's path, while httpsClone's copies, persisted in the clone's local
// config, lose to global but beat GIT_CONFIG_COUNT. That tie is accepted.
func GitRemote(repo string) (url string, gitArgs []string) {
	url = "https://" + Host() + "/" + repo + ".git"
	return url, []string{
		"-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential",
		"-c", "url." + url + ".insteadOf=" + url,
		"-c", "url." + url + ".pushInsteadOf=" + url,
	}
}

var _ forge.BundleCommitSubjects = (*execClient)(nil)
var _ forge.BundleRelay = (*execClient)(nil)
var _ forge.MergeCommitReader = (*execClient)(nil)
var _ forge.MirrorSeeder = (*execClient)(nil)
