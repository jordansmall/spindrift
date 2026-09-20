package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// checkoutLockFileName is the well-known lock file name within a checkout's
// git dir. It is never removed on Release: the point of flock is that the
// kernel releases it when the holder dies (including an uncatchable
// SIGKILL), so there is nothing stale to clean up and a fresh daemon can
// always re-acquire it. Deleting the file on release would only reintroduce
// a race with a concurrent acquirer.
const checkoutLockFileName = "spindrift-daemon.lock"

// CheckoutLock is a held, per-checkout exclusive lock enforcing that at
// most one spindrift daemon drives a given checkout at a time (issue
// #3543). Without it, two daemons against one checkout would make the
// configured concurrency a lie.
type CheckoutLock struct {
	path string
	file *os.File
}

// AcquireCheckoutLock takes a non-blocking exclusive lock on
// filepath.Join(dir, "spindrift-daemon.lock") and stamps the holder's
// identity into the file. dir is expected to already exist (the caller
// passes a git dir); a missing dir is a real error, not silently created.
//
// The lock never blocks and never retries: an operator running a second
// daemon by mistake must see "already running" at once, not a hang.
func AcquireCheckoutLock(dir string, kind Kind) (*CheckoutLock, error) {
	lockPath := filepath.Join(dir, checkoutLockFileName)

	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open checkout lock file %s: %w", lockPath, err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Only EWOULDBLOCK means "someone else holds it". Anything else —
		// ENOLCK on a filesystem without locking, say — would be reported
		// as a phantom second daemon if it shared that message, sending an
		// operator hunting for a process that does not exist. Check the
		// errno before reading: the 4 KiB identity read is only worth
		// paying for when the result will actually be used.
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, fmt.Errorf("lock checkout lock file %s: %w", lockPath, err)
		}
		holder := readHolderIdentity(file)
		_ = file.Close()
		return nil, fmt.Errorf("another spindrift daemon already holds this checkout (lock file %s): %s", lockPath, holder)
	}

	if err := writeHolderIdentity(file, kind); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("write checkout lock identity to %s: %w", lockPath, err)
	}

	return &CheckoutLock{path: lockPath, file: file}, nil
}

// Release unlocks and closes the lock file. Call it exactly once; there is
// no finalizer. The file itself is left in place — see checkoutLockFileName.
func (l *CheckoutLock) Release() error {
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
		_ = l.file.Close()
		return fmt.Errorf("unlock checkout lock file %s: %w", l.path, err)
	}
	return l.file.Close()
}

// writeHolderIdentity stamps a single greppable line naming this process
// into the just-acquired lock file, for a refused second acquirer to read
// back and report. A failing Hostname/Executable must not fail the acquire
// itself — the identity line is a diagnostic, not the lock.
func writeHolderIdentity(file *os.File, kind Kind) error {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}

	var exePart string
	if exe, err := os.Executable(); err == nil {
		exePart = " exe=" + exe
	}

	line := fmt.Sprintf("pid=%d host=%s kind=%s started=%s%s\n",
		os.Getpid(), host, kind, time.Now().UTC().Format(time.RFC3339), exePart)

	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.WriteAt([]byte(line), 0); err != nil {
		return err
	}
	return file.Sync()
}

// readHolderIdentity reads back whatever identity line a lock-holding
// process wrote, for a refused acquirer's error message. The file may be
// empty or unreadable in the narrow window where the holder has flocked
// but not yet written (or is mid-write of) its identity line — in that
// case, say so plainly rather than inventing a PID.
func readHolderIdentity(file *os.File) string {
	buf := make([]byte, 4096)
	// ReadAt returns a non-nil error at or before EOF even on a successful
	// partial read; n is what matters here, not err — this is a best-effort
	// diagnostic read, not a correctness-critical one.
	n, _ := file.ReadAt(buf, 0)
	line := strings.TrimSpace(string(buf[:n]))
	if line == "" {
		return "holder details unavailable"
	}
	return line
}
