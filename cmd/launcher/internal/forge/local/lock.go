package local

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// AccumulationLock is a held cross-process advisory lock on an Accumulation
// repo's path, acquired via AcquireAccumulationLock. Release unlocks and
// closes the underlying lock file.
type AccumulationLock struct {
	path string
	file *os.File
}

// AcquireAccumulationLock takes a non-blocking exclusive lock on
// repoPath+".lock", creating the lock file (and its parent directory, if
// missing — repoPath's parent isn't guaranteed to exist ahead of time,
// since git init --bare creates any missing leading directories itself)
// if absent. Returns a descriptive error if another process already holds
// the lock rather than blocking: this is a cross-process serialization
// point (issue #2441) guarding against two independent `spindrift`
// processes (e.g. research + dispatch) seeding/mounting the same
// Accumulation repo concurrently, and for an operator-driven workflow a
// clear "try again" error beats a silent hang.
func AcquireAccumulationLock(repoPath string) (*AccumulationLock, error) {
	lockPath := repoPath + ".lock"

	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return nil, fmt.Errorf("create accumulation lock dir for %s: %w", lockPath, err)
	}

	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open accumulation lock file %s: %w", lockPath, err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("accumulation repo %s is locked by another process (lock file %s): %w", repoPath, lockPath, err)
	}

	return &AccumulationLock{path: lockPath, file: file}, nil
}

// Release unlocks and closes the lock file. Safe to call once; the caller
// owns the returned *AccumulationLock's lifecycle (no finalizer).
func (l *AccumulationLock) Release() error {
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
		_ = l.file.Close()
		return fmt.Errorf("unlock accumulation lock file %s: %w", l.path, err)
	}
	return l.file.Close()
}
