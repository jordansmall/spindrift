// Package gitplumbing holds git-specific plumbing helpers shared by the git
// and github forge adapters: stderr classification and force-push handling.
package gitplumbing

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// IsMergeConflict returns true when gh's stderr indicates a merge-conflict
// failure rather than a permissions error, network failure, or other cause.
func IsMergeConflict(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "merge conflict") ||
		strings.Contains(s, "not mergeable")
}

// GitForcePush force-with-lease-pushes the current branch of the repo
// checked out at dir, capturing git's stderr into the returned error so
// callers can tell a stale lease apart from an auth or network fault. A
// failure without a genuine ref-rejection marker in stderr is wrapped in
// forge.ErrTransientPushFailure so callers know it's safe to retry. Shared by
// the git and github adapters, both of which force-push a rebased branch.
func GitForcePush(dir string) error {
	var stderr bytes.Buffer
	cmd := exec.Command("git", "-C", dir, "push", "--force-with-lease")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return wrapForcePushError(err, stderr.String())
	}
	return nil
}

// wrapForcePushError builds GitForcePush's returned error from the push
// subprocess's failure and raw stderr. Stale-lease classification runs on
// the raw stderr (isStalePushRejection needs git's exact rejection markers),
// but the stderr embedded in the error message is redacted first — a
// credential-bearing CODE_FORGE_REMOTE_URL can appear in git's own
// diagnostics, and this error flows unmodified into a public GitHub issue
// comment (settle.mergeImmediate).
func wrapForcePushError(err error, stderr string) error {
	s := strings.TrimSpace(stderr)
	suffix := ""
	if s != "" {
		suffix = ": " + forge.RedactURLCredentials(s)
	}
	if isStalePushRejection(s) {
		return fmt.Errorf("git push --force-with-lease: %w%s", err, suffix)
	}
	return fmt.Errorf("git push --force-with-lease: %w%s: %w", err, suffix, forge.ErrTransientPushFailure)
}

// isStalePushRejection returns true when git's stderr indicates a genuine
// ref rejection — the branch moved since the last fetch and the rebase is
// out of date — as opposed to a transient infra or network fault.
func isStalePushRejection(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "stale info") ||
		strings.Contains(s, "non-fast-forward") ||
		strings.Contains(s, "failed to push some refs") ||
		strings.Contains(s, "[rejected]")
}
