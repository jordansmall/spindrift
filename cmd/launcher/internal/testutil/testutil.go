// Package testutil holds test-only helpers shared across cmd/launcher's internal packages.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// SameHash and DiffHash are 32-char store-hash-shaped fixtures for tests that
// compare an evaluated nix store hash against a loaded one.
const (
	SameHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	DiffHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// GitRun runs git in dir, failing the test on error.
func GitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// NewCloneWithOrigin returns a clone directory whose bare "origin" holds one
// commit on baseBranch, the shape a real launcher pwd has: a checkout with a
// fetchable "origin" remote.
func NewCloneWithOrigin(t *testing.T, baseBranch string) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	clone := filepath.Join(dir, "clone")

	GitRun(t, "", "init", "--bare", bare)
	GitRun(t, "", "clone", bare, clone)
	GitRun(t, clone, "checkout", "-B", baseBranch)
	GitRun(t, clone, "config", "user.email", "test@example.com")
	GitRun(t, clone, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(clone, "flake.nix"), []byte("{ }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	GitRun(t, clone, "add", "flake.nix")
	GitRun(t, clone, "commit", "-m", "base")
	GitRun(t, clone, "push", "-u", "origin", baseBranch)

	return clone
}

// CaptureStderr returns everything fn writes to os.Stderr.
func CaptureStderr(t *testing.T, fn func()) string {
	t.Helper()
	return capture(t, &os.Stderr, fn)
}

// CaptureStdout returns everything fn writes to os.Stdout.
func CaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	return capture(t, &os.Stdout, fn)
}

// capture starts the reader goroutine before fn() rather than after w.Close():
// fn() would otherwise deadlock once it wrote past the OS pipe buffer (~64KiB
// on Linux) with nothing draining it.
func capture(t *testing.T, target **os.File, fn func()) string {
	t.Helper()
	orig := *target
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	*target = w

	done := make(chan string)
	go func() {
		var buf strings.Builder
		tmp := make([]byte, 4096)
		for {
			n, rerr := r.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
			}
			if rerr != nil {
				break
			}
		}
		done <- buf.String()
	}()

	fn()

	w.Close()
	*target = orig

	out := <-done
	r.Close()
	return out
}
