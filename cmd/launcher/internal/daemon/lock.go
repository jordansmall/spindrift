package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"spindrift.dev/launcher/internal/flock"
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

// acquireGraceTotal bounds how long AcquireCheckoutLock retries a
// LOCK_EX|LOCK_NB attempt that found the lock held (flock.ErrHeld) before
// it gives up and reports it. It exists to tell a status reader's momentary
// LOCK_SH probe (held for a few syscalls, then released — see
// probeCheckoutLock) from a genuine daemon, which holds LOCK_EX for its
// whole life and so still exhausts the full grace period and gets
// reported. Kept short: an operator's mistaken second daemon must still
// see "already running" promptly, not a hang.
const acquireGraceTotal = 50 * time.Millisecond

// acquireGraceStep is the sleep between retries within acquireGraceTotal.
const acquireGraceStep = 2 * time.Millisecond

// acquireSleep is AcquireCheckoutLock's between-retry sleep, a var so a test
// can release a simulated probe inside the retry window deterministically
// instead of racing acquireGraceTotal on a loaded machine.
var acquireSleep = time.Sleep

// AcquireCheckoutLock takes a non-blocking exclusive lock on
// filepath.Join(dir, "spindrift-daemon.lock") and stamps the holder's
// identity into the file. dir is expected to already exist (the caller
// passes a git dir); a missing dir is a real error, not silently created.
//
// Every attempt uses LOCK_NB, so it never blocks the kernel-level way; an
// operator running a second daemon by mistake still sees "already running"
// within acquireGraceTotal, not a hang. But a lone flock.ErrHeld is retried
// for up to acquireGraceTotal before being reported, because a status
// reader's LOCK_SH probe can transiently hold the lock across a few
// syscalls — without the retry, that window would make a starting daemon
// mistake a passing reader for a second daemon and refuse to start.
func AcquireCheckoutLock(dir string, kinds []Kind) (*CheckoutLock, error) {
	lockPath := filepath.Join(dir, checkoutLockFileName)

	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open checkout lock file %s: %w", lockPath, err)
	}

	deadline := time.Now().Add(acquireGraceTotal)
	var flockErr error
	for {
		flockErr = flock.TryExclusive(file)
		if flockErr == nil || !errors.Is(flockErr, flock.ErrHeld) || time.Now().After(deadline) {
			break
		}
		acquireSleep(acquireGraceStep)
	}

	if flockErr != nil {
		// Check before reading: a non-held errno (e.g. ENOLCK) is a real
		// failure, not a second daemon (see package flock), and the 4 KiB
		// identity read is only worth paying for when it will be used.
		if !errors.Is(flockErr, flock.ErrHeld) {
			_ = file.Close()
			return nil, fmt.Errorf("lock checkout lock file %s: %w", lockPath, flockErr)
		}
		holder := readHolderIdentity(file)
		_ = file.Close()
		return nil, fmt.Errorf("another spindrift daemon already holds this checkout (lock file %s): %s", lockPath, holder)
	}

	if err := writeHolderIdentity(file, kinds); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("write checkout lock identity to %s: %w", lockPath, err)
	}

	return &CheckoutLock{path: lockPath, file: file}, nil
}

// InheritCheckoutLock wraps a lock descriptor inherited across an in-place exec
// (ADR 0063). The old image's open file description already holds LOCK_EX, so
// TryExclusive on the shared description succeeds, while a description held
// by anyone else fails with flock.ErrHeld. The descriptor is not closed on any
// failure: a forged handoff document may name an fd this process uses for
// something else. The path is unknown until Claim.
func InheritCheckoutLock(fd int) (*CheckoutLock, error) {
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return nil, fmt.Errorf("inherit checkout lock: stat descriptor %d: %w", fd, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return nil, fmt.Errorf("inherit checkout lock: descriptor %d is not a regular file", fd)
	}
	// The probe is a dup because an *os.File closes its descriptor when
	// collected, and fd must stay untouched if the lock is refused. The dup
	// shares fd's open file description, so it sees and takes the same lock.
	syscall.ForkLock.RLock()
	probeFD, err := syscall.Dup(fd)
	if err == nil {
		syscall.CloseOnExec(probeFD)
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, fmt.Errorf("inherit checkout lock: dup descriptor %d: %w", fd, err)
	}
	probe := os.NewFile(uintptr(probeFD), "checkout-lock-probe")
	lockErr := flock.TryExclusive(probe)
	_ = probe.Close()
	if lockErr != nil {
		return nil, fmt.Errorf("inherit checkout lock: descriptor %d: %w", fd, lockErr)
	}
	syscall.CloseOnExec(fd)
	return &CheckoutLock{file: os.NewFile(uintptr(fd), "checkout-lock")}, nil
}

// Claim binds an inherited lock to dir, refusing unless the descriptor is that
// checkout's lock file: an inherited lock on some other file would leave dir
// unprotected.
func (l *CheckoutLock) Claim(dir string) error {
	lockPath := filepath.Join(dir, checkoutLockFileName)
	have, err := l.file.Stat()
	if err != nil {
		return fmt.Errorf("claim checkout lock %s: stat inherited descriptor: %w", lockPath, err)
	}
	want, err := os.Stat(lockPath)
	if err != nil {
		return fmt.Errorf("claim checkout lock %s: %w", lockPath, err)
	}
	if !os.SameFile(have, want) {
		return fmt.Errorf("inherited checkout lock descriptor (%s) is not the lock file %s", l.file.Name(), lockPath)
	}
	l.path = lockPath
	return nil
}

// File is the held lock file; the exec step dups its descriptor into the
// handoff.
func (l *CheckoutLock) File() *os.File { return l.file }

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
func writeHolderIdentity(file *os.File, kinds []Kind) error {
	// Truncate first so a killed predecessor's identity line is invalidated
	// as soon as the flock is ours. This narrows the window in which a
	// reader sees it but does not close it; ReadStatus's pid probe closes it
	// for a same-host holder short of pid recycling or a separate pid
	// namespace (see docs/reference.md "Status file").
	if err := file.Truncate(0); err != nil {
		return err
	}

	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}

	var exePart string
	if exe, err := os.Executable(); err == nil {
		exePart = " exe=" + exe
	}

	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}

	line := fmt.Sprintf("pid=%d host=%s kind=%s started=%s%s\n",
		os.Getpid(), host, strings.Join(names, ","), time.Now().UTC().Format(time.RFC3339), exePart)

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
