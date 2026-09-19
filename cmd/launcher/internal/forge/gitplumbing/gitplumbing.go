// Package gitplumbing holds the helpers shared by the git and github forge
// adapters: git stderr classification and force-push handling.
package gitplumbing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// MatchesAnyMarker reports whether stderr contains any of markers,
// case-insensitively. markers must already be lowercase, and none may be
// empty: an empty marker matches every stderr.
func MatchesAnyMarker(stderr string, markers []string) bool {
	s := strings.ToLower(stderr)
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

var mergeConflictMarkers = []string{
	"merge conflict",
	"not mergeable",
}

// IsMergeConflict reports whether gh's stderr indicates a merge conflict
// rather than a permissions error, network failure, or other cause.
func IsMergeConflict(stderr string) bool {
	return MatchesAnyMarker(stderr, mergeConflictMarkers)
}

var mergeTransientMarkers = []string{
	"502",
	"503",
	"504",
	"bad gateway",
	"service unavailable",
	"gateway timeout",
	"timeout",
	"connection reset",
	"eof",
	"i/o timeout",
	"temporary failure in name resolution",
	"context deadline exceeded",
}

// IsMergeTransient reports whether gh's stderr indicates a transient
// transport or server failure rather than a genuine merge rejection. The
// "timeout" and "eof" markers are broad enough to appear in unrelated stderr,
// so callers must check IsMergeConflict first.
func IsMergeTransient(stderr string) bool {
	return MatchesAnyMarker(stderr, mergeTransientMarkers)
}

// GitForcePush force-with-lease-pushes the current branch checked out at dir,
// appending extraArgs after --force-with-lease (e.g. "-u", "origin", ref for a
// branch with no upstream, issue #1918). A failure with no ref-rejection marker
// in stderr wraps forge.ErrTransientPushFailure, so callers know a retry is
// safe. ctx bounds the subprocess because git applies no timeout of its own.
func GitForcePush(ctx context.Context, dir string, extraArgs ...string) error {
	// stderr goes to a file, not an io.Writer: Cmd.Run's copy goroutine waits
	// for EOF on the pipe, which a hung grandchild (git-receive-pack's
	// pre-receive hook) holds open after the context kills git itself.
	stderrFile, err := os.CreateTemp("", "spindrift-force-push-stderr-*")
	if err != nil {
		return fmt.Errorf("git push --force-with-lease: create stderr temp file: %w", err)
	}
	defer os.Remove(stderrFile.Name())
	defer stderrFile.Close()

	args := append([]string{"-C", dir, "push", "--force-with-lease"}, extraArgs...)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stderr = stderrFile
	if err := cmd.Run(); err != nil {
		// A deadline kill leaves none of git's own rejection markers in stderr,
		// so the timeout is reported rather than run through wrapForcePushError.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("git push --force-with-lease: timed out: %w", ctx.Err())
		}
		stderr, _ := os.ReadFile(stderrFile.Name())
		return wrapForcePushError(err, string(stderr))
	}
	return nil
}

// wrapForcePushError classifies the failure on raw stderr (isStalePushRejection
// needs git's exact markers) but redacts the stderr it embeds in the message: a
// credential-bearing CODE_FORGE_REMOTE_URL can appear in git's diagnostics, and
// this error reaches a public GitHub issue comment (settle.mergeImmediate).
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

var stalePushRejectionMarkers = []string{
	"stale info",
	"non-fast-forward",
	"failed to push some refs",
	"[rejected]",
}

// isStalePushRejection reports whether git's stderr indicates a genuine ref
// rejection (the branch moved since the last fetch, so the rebase is out of
// date) rather than a transient infra or network fault.
func isStalePushRejection(stderr string) bool {
	return MatchesAnyMarker(stderr, stalePushRejectionMarkers)
}
