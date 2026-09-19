package local

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// AccumulationLock is a held cross-process advisory lock on an Accumulation repo's path.
type AccumulationLock struct {
	path string
	file *os.File
}

// AcquireAccumulationLock takes a non-blocking exclusive lock on repoPath+".lock".
// It serializes two independent spindrift processes (research and dispatch, say) that
// would otherwise seed or mount the same Accumulation repo at once (issue #2441). It
// errors instead of blocking so an operator sees "try again" rather than a silent hang.
// It creates the lock file's parent directory, since the repo may not exist yet.
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

// Release unlocks and closes the lock file. Call it exactly once; there is no finalizer.
func (l *AccumulationLock) Release() error {
	if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
		_ = l.file.Close()
		return fmt.Errorf("unlock accumulation lock file %s: %w", l.path, err)
	}
	return l.file.Close()
}
