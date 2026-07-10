package forge

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// isMergeConflict returns true when gh's stderr indicates a merge-conflict
// failure rather than a permissions error, network failure, or other cause.
func isMergeConflict(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "merge conflict") ||
		strings.Contains(s, "not mergeable")
}

// gitForcePush force-with-lease-pushes the current branch of the repo
// checked out at dir, capturing git's stderr into the returned error so
// callers can tell a stale lease apart from an auth or network fault. A
// failure without a genuine ref-rejection marker in stderr is wrapped in
// ErrTransientPushFailure so callers know it's safe to retry.
func gitForcePush(dir string) error {
	var stderr bytes.Buffer
	cmd := exec.Command("git", "-C", dir, "push", "--force-with-lease")
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		s := strings.TrimSpace(stderr.String())
		suffix := ""
		if s != "" {
			suffix = ": " + s
		}
		if isStalePushRejection(s) {
			return fmt.Errorf("git push --force-with-lease: %w%s", err, suffix)
		}
		return fmt.Errorf("git push --force-with-lease: %w%s: %w", err, suffix, ErrTransientPushFailure)
	}
	return nil
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
