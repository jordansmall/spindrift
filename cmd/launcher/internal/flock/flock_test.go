package flock

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func openLockFile(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestTryExclusiveFree(t *testing.T) {
	f := openLockFile(t, filepath.Join(t.TempDir(), "lock"))
	if err := TryExclusive(f); err != nil {
		t.Fatalf("TryExclusive on a free lock: %v", err)
	}
}

func TestTryExclusiveHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, second := openLockFile(t, path), openLockFile(t, path)
	if err := TryExclusive(first); err != nil {
		t.Fatal(err)
	}
	err := TryExclusive(second)
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("err = %v, want ErrHeld", err)
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("err = %v, want it to wrap EWOULDBLOCK", err)
	}
}

func TestTryExclusiveReleased(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	first, second := openLockFile(t, path), openLockFile(t, path)
	if err := TryExclusive(first); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := TryExclusive(second); err != nil {
		t.Fatalf("TryExclusive after release: %v", err)
	}
}

func TestTryExclusiveOtherErrnoIsNotHeld(t *testing.T) {
	f := openLockFile(t, filepath.Join(t.TempDir(), "lock"))
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	err := TryExclusive(f)
	if err == nil {
		t.Fatal("TryExclusive on a closed file succeeded")
	}
	if errors.Is(err, ErrHeld) {
		t.Fatalf("err = %v, a non-EWOULDBLOCK failure must not read as held", err)
	}
}
