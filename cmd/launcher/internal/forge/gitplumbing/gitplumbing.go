// Package gitplumbing holds the helpers shared by the git and github forge
// adapters: git stderr classification and force-push handling.
package gitplumbing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/gitexec"
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

// sharedTransientMarkers are the transport and server faults both
// mergeTransientMarkers and pushTransientMarkers include, so the entries cannot
// drift between them. None may be a bare "timeout" or "eof": those match
// branch names and unrelated rejection text.
var sharedTransientMarkers = []string{
	"connection reset",
	"i/o timeout",
	"temporary failure in name resolution",
	"bad gateway",
	"service unavailable",
	"gateway timeout",
}

// mergeTransientMarkers' "http 5xx" entries anchor on gh's status-line prefix,
// since bare digits match PR numbers, branch names and URLs. The EOF and
// timeout entries are specific phrases from Go net/http transport errors
// (`Post "<url>": EOF`, which carries no status line) and the GraphQL timeout
// text GitHub's API returns, never the bare words. Residual exposure: a PR
// title or branch echoed into stderr that contains a full phrase (e.g.
// "unexpected EOF"), or ends a line in ": eof" (a branch named "eof"), still
// matches. The ": eof\n" entry is end-anchored on an LF line ending;
// IsMergeTransient appends the newline.
var mergeTransientMarkers = slices.Concat(sharedTransientMarkers, []string{
	"http 502",
	"http 503",
	"http 504",
	": eof\n",
	"unexpected eof",
	"tls handshake timeout",
	"context deadline exceeded",
	"may be the result of a timeout",
	"client.timeout exceeded",
	"timeout awaiting response headers",
})

// IsMergeTransient reports whether gh's stderr indicates a transient
// transport or server failure rather than a genuine merge rejection. Callers
// check IsMergeConflict first: a conflict's stderr is classified by mergeable
// state, not by these markers.
func IsMergeTransient(stderr string) bool {
	return MatchesAnyMarker(stderr+"\n", mergeTransientMarkers)
}

// GitForcePush force-with-lease-pushes the current branch checked out at dir,
// appending extraArgs after --force-with-lease (e.g. "-u", "origin", ref for a
// branch with no upstream, issue #1918). A failure whose stderr carries a
// transport or server marker, or a ctx deadline kill, wraps
// forge.ErrTransientPushFailure so callers know a retry is safe. ctx bounds
// the subprocess because git applies no timeout of its own.
func GitForcePush(ctx context.Context, dir string, cred gitexec.Credential, extraArgs ...string) error {
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
	cmd := cred.Apply(exec.CommandContext(ctx, "git", args...))
	cmd.Stderr = stderrFile
	if err := cmd.Run(); err != nil {
		// A deadline kill is transient: every caller builds a fresh deadline per
		// push, and a retry re-takes the lease. It carries no stderr markers, so
		// it skips wrapForcePushError.
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("git push --force-with-lease: timed out: %w: %w", ctx.Err(), forge.ErrTransientPushFailure)
		}
		stderr, _ := os.ReadFile(stderrFile.Name())
		return wrapForcePushError(err, string(stderr))
	}
	return nil
}

// wrapForcePushError classifies the failure on raw stderr (the marker checks
// need git's exact text) but redacts the stderr it embeds in the message: a
// credential-bearing CODE_FORGE_REMOTE_URL can appear in git's diagnostics, and
// this error reaches a public GitHub issue comment (settle.mergeImmediate).
// Only a positive transport/server marker is transient; everything else,
// including unrecognised stderr, is permanent.
func wrapForcePushError(err error, stderr string) error {
	s := strings.TrimSpace(stderr)
	suffix := ""
	if s != "" {
		suffix = ": " + forge.RedactURLCredentials(s)
	}
	var sentinel error
	switch {
	case isWorkflowPermissionDenied(s):
		sentinel = forge.ErrWorkflowPermissionDenied
	case !isStalePushRejection(s) && isPushTransient(s):
		sentinel = forge.ErrTransientPushFailure
	}
	if sentinel != nil {
		return fmt.Errorf("git push --force-with-lease: %w%s: %w", err, suffix, sentinel)
	}
	return fmt.Errorf("git push --force-with-lease: %w%s", err, suffix)
}

// isWorkflowPermissionDenied matches GitHub's refusal to let a credential touch
// `.github/workflows/*`. The PAT form ends "without `workflow` scope" and the
// App form "without `workflows` permission"; only the shared stem is stable.
func isWorkflowPermissionDenied(stderr string) bool {
	lower := strings.ToLower(stderr)
	return strings.Contains(lower, "refusing to allow a") &&
		strings.Contains(lower, "to create or update workflow")
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

// pushTransientMarkers match transport and server faults a retry may clear. The
// "returned error: 5" and "rpc failed; http 5" entries catch curl's HTTP 5xx
// lines without bare digit markers, which would match SHAs and URLs in stderr.
var pushTransientMarkers = slices.Concat(sharedTransientMarkers, []string{
	"connection refused",
	"timed out",
	"remote end hung up",
	"unexpected disconnect",
	"early eof",
	"broken pipe",
	"kex_exchange_identification",
	"gnutls_handshake() failed",
	"ssl_error_syscall",
	"could not resolve host",
	"network is unreachable",
	"returned error: 5",
	"rpc failed; http 5",
	"internal server error",
})

// pushClientErrorMarkers flag an HTTP 4xx from git's POST stage. Git follows
// those with "the remote end hung up unexpectedly", so without this veto the
// symptom markers above would misclassify a permanent 403/413 as transient.
var pushClientErrorMarkers = []string{
	"returned error: 4",
	"rpc failed; http 4",
}

// pushRateLimitMarkers flag HTTP 429, the one 4xx a retry can clear.
var pushRateLimitMarkers = []string{
	"returned error: 429",
	"rpc failed; http 429",
}

// isPushTransient reports whether git's stderr positively indicates a
// transport or server fault that a retry may clear.
func isPushTransient(stderr string) bool {
	if MatchesAnyMarker(stderr, pushRateLimitMarkers) {
		return true
	}
	if MatchesAnyMarker(stderr, pushClientErrorMarkers) {
		return false
	}
	return MatchesAnyMarker(stderr, pushTransientMarkers)
}

// HasUnmergedPaths reports whether the index at dir holds unmerged entries.
// It classifies git's own merge/rebase failures independent of locale and
// conflict kind (modify/delete and rename/rename print no "merge conflict"
// text), so call it before --abort clears the index.
func HasUnmergedPaths(ctx context.Context, dir string) (bool, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "-u").Output()
	if err != nil {
		return false, fmt.Errorf("git ls-files -u: %w", err)
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}
