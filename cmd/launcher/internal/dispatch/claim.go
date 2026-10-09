package dispatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/flock"
)

// ErrIssueClaimed reports that another Dispatch, in this process or another
// sharing the working dir, already holds the issue's claim (issue #3885).
var ErrIssueClaimed = errors.New("dispatch: issue already claimed by another process")

// issueClaimPath never matches AllAttemptLogPaths' names, so quarantine cannot
// rename it out from under its holder.
func issueClaimPath(pwd, number string) string {
	return filepath.Join(HostLogDirFor(pwd), "issue-"+number+".lock")
}

// ClaimIssue takes a non-blocking advisory flock on number's claim file under
// pwd, returning ErrIssueClaimed if another holder has it. The kernel drops
// the flock when the fd closes, so a crashed launcher leaves no stale claim.
// Go opens the file O_CLOEXEC, so a podman/bwrap child never inherits the fd
// and cannot hold the claim past this process's life.
func ClaimIssue(pwd, number string) (release func(), err error) {
	dir := HostLogDirFor(pwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir claim dir %s: %w", dir, err)
	}

	path := issueClaimPath(pwd, number)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open claim file %s: %w", path, err)
	}

	if err := flock.TryExclusive(file); err != nil {
		_ = file.Close()
		if errors.Is(err, flock.ErrHeld) {
			return nil, ErrIssueClaimed
		}
		return nil, fmt.Errorf("flock claim file %s: %w", path, err)
	}

	// Left in place on release, as checkoutLockFileName is
	// (internal/daemon/lock.go): closing the fd is what drops the lock.
	return func() { _ = file.Close() }, nil
}
