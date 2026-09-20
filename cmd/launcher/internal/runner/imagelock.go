package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// imageLockDir is a seam so tests redirect the lock off the shared temp dir.
var imageLockDir = os.TempDir

// imageLock is a held cross-process advisory lock on realizing one image tag
// on this host. Unlike AccumulationLock (forge/local/lock.go), it blocks
// rather than failing fast: issue #3632 wants a losing child to wait out
// however long the winner's realize/load/re-tag takes, not bail with an
// error.
type imageLock struct {
	path string
	file *os.File
}

// imageLockFilePrefix and imageLockFileSuffix name the per-image lock file.
// Like daemon/lock.go's checkoutLockFileName, the file is deliberately never
// unlinked on Release: unlinking would race a waiter that already opened
// (and holds an fd on) the old inode, reintroducing the very race flock
// exists to prevent. So the lock dir accumulates one zero-byte file per
// distinct image reference realized under that `os.TempDir()`, forever — an
// accepted tradeoff, not an oversight.
const (
	imageLockFilePrefix = "spindrift-image-"
	imageLockFileSuffix = ".lock"
)

// imageLockPath returns the lock file for image on this host. It hashes
// image rather than sanitizing it: an image reference carries "/" and ":",
// which a filename cannot hold directly, and a registry-qualified reference
// can outrun the 255-byte filename limit. 16 hex chars (64 bits) is ample to
// avoid collisions among the images one host realizes concurrently.
func imageLockPath(image string) string {
	sum := sha256.Sum256([]byte(image))
	h := hex.EncodeToString(sum[:])[:16]
	return filepath.Join(imageLockDir(), imageLockFilePrefix+h+imageLockFileSuffix)
}

// flockRetry runs flock(2) with how, retrying the EINTR that Go's own SIGURG
// goroutine-preemption signal can raise against a blocked (or even a
// non-blocking) call. It returns any other error — including EWOULDBLOCK for
// a LOCK_NB caller — unchanged.
func flockRetry(fd, how int) error {
	for {
		err := syscall.Flock(fd, how)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// acquireImageLock serializes concurrent Dispatch children on one host that
// would otherwise each realize, load, and re-tag the same absent image at
// once (issue #3632). It tries a non-blocking exclusive lock first so the
// common "nobody else is realizing this image" case pays no wait; only once
// that reports EWOULDBLOCK does it fall back to a blocking acquire, since a
// losing child must wait out the winner's build however long that takes,
// never time out or fail. onWait, if non-nil, runs exactly once, right
// before that blocking acquire — the caller's chance to explain a wait that
// can run minutes before there is anything else to show for it.
func acquireImageLock(image string, onWait func()) (*imageLock, error) {
	path := imageLockPath(image)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open image lock file %s: %w", path, err)
	}

	fd := int(file.Fd())

	if err := flockRetry(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, fmt.Errorf("probe image lock %s (lock file %s): %w", image, path, err)
		}
		if onWait != nil {
			onWait()
		}
		if err := flockRetry(fd, syscall.LOCK_EX); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("acquire image lock %s (lock file %s): %w", image, path, err)
		}
	}

	return &imageLock{path: path, file: file}, nil
}

// Release unlocks and closes the lock file. Call it exactly once; there is
// no finalizer. The file itself is left in place — see imageLockFilePrefix.
func (l *imageLock) Release() error {
	if err := flockRetry(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
		_ = l.file.Close()
		return fmt.Errorf("unlock image lock file %s: %w", l.path, err)
	}
	return l.file.Close()
}
