//go:build unix

package dispatch

import (
	"os"
	"syscall"
	"testing"
)

// Issue #5387: under rootless podman the Box's uid 1000 is not the host user,
// so the per-issue dir must be world-writable whatever the host umask, while
// the root stays private. Umask is process-wide, so this test is not parallel.
func TestCache_DirForIsWorldWritableDespiteUmask(t *testing.T) {
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)

	c, err := newCache(os.RemoveAll)
	if err != nil {
		t.Fatalf("newCache: %v", err)
	}
	defer c.cleanup()

	dir := c.dirFor("42")
	if dir == "" {
		t.Fatal("dirFor returned empty path")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat per-issue dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o777 {
		t.Errorf("per-issue dir mode = %#o, want 0777", got)
	}
	rootInfo, err := os.Stat(c.root)
	if err != nil {
		t.Fatalf("stat cache root: %v", err)
	}
	if got := rootInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("cache root mode = %#o, want 0700", got)
	}
}
