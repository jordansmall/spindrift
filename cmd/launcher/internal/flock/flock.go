// Package flock makes the shared non-blocking exclusive advisory-lock
// attempt. Only EWOULDBLOCK means held; any other errno (e.g. ENOLCK on a
// filesystem without locking) is a real failure, not a phantom holder.
package flock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// ErrHeld reports that another open file description holds the lock.
var ErrHeld = errors.New("flock: lock is held")

// TryExclusive takes LOCK_EX|LOCK_NB on f. A contended lock returns an error
// wrapping both ErrHeld and syscall.EWOULDBLOCK; any other flock error is
// returned as-is. EINTR is not retried: a LOCK_NB flock never sleeps, so no
// blocking window exists for a signal to interrupt (and Go installs its
// handlers with SA_RESTART).
func TryExclusive(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return fmt.Errorf("%w: %w", ErrHeld, err)
	}
	return err
}
