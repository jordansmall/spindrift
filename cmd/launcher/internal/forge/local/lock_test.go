package local

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestAcquireAccumulationLock_FreshPathSucceedsAndReleases(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "accum.git")

	lock, err := AcquireAccumulationLock(repoPath)
	if err != nil {
		t.Fatalf("AcquireAccumulationLock(%q): unexpected error: %v", repoPath, err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release(): unexpected error: %v", err)
	}
}

func TestAcquireAccumulationLock_SecondAcquireFailsWhileHeld(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "accum.git")

	first, err := AcquireAccumulationLock(repoPath)
	if err != nil {
		t.Fatalf("first AcquireAccumulationLock(%q): unexpected error: %v", repoPath, err)
	}
	t.Cleanup(func() {
		_ = first.Release()
	})

	_, err = AcquireAccumulationLock(repoPath)
	if err == nil {
		t.Fatalf("second AcquireAccumulationLock(%q): expected error, got nil", repoPath)
	}

	msg := err.Error()
	if !strings.Contains(msg, repoPath) {
		t.Errorf("error %q: expected to mention repo path %q", msg, repoPath)
	}
	if !strings.Contains(strings.ToLower(msg), "locked") && !strings.Contains(strings.ToLower(msg), "another") {
		t.Errorf("error %q: expected to mention that the lock is held by another process", msg)
	}
}

func TestAcquireAccumulationLock_HeldErrorWrapsEWOULDBLOCKExactly(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "accum.git")

	first, err := AcquireAccumulationLock(repoPath)
	if err != nil {
		t.Fatalf("first AcquireAccumulationLock(%q): unexpected error: %v", repoPath, err)
	}
	t.Cleanup(func() {
		_ = first.Release()
	})

	_, err = AcquireAccumulationLock(repoPath)
	if err == nil {
		t.Fatalf("second AcquireAccumulationLock(%q): expected error, got nil", repoPath)
	}

	want := fmt.Sprintf("accumulation repo %s is locked by another process (lock file %s): %s", repoPath, repoPath+".lock", syscall.EWOULDBLOCK)
	if err.Error() != want {
		t.Errorf("error message = %q, want %q", err.Error(), want)
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Errorf("error %v: expected to wrap syscall.EWOULDBLOCK", err)
	}
}

func TestAcquireAccumulationLock_SucceedsAgainAfterRelease(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "accum.git")

	first, err := AcquireAccumulationLock(repoPath)
	if err != nil {
		t.Fatalf("first AcquireAccumulationLock(%q): unexpected error: %v", repoPath, err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("first Release(): unexpected error: %v", err)
	}

	second, err := AcquireAccumulationLock(repoPath)
	if err != nil {
		t.Fatalf("second AcquireAccumulationLock(%q) after release: unexpected error: %v", repoPath, err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second Release(): unexpected error: %v", err)
	}
}

func TestAcquireAccumulationLock_CreatesMissingParentDir(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "nested", "dir", "accum.git")

	lock, err := AcquireAccumulationLock(repoPath)
	if err != nil {
		t.Fatalf("AcquireAccumulationLock(%q) with missing parent dir: unexpected error: %v", repoPath, err)
	}

	if err := lock.Release(); err != nil {
		t.Fatalf("Release(): unexpected error: %v", err)
	}
}
