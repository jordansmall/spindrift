package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDriverCache_DirForCreatesPerIssueDir verifies dirFor creates and
// returns a writable directory keyed strictly <cache>/<issue>.
func TestDriverCache_DirForCreatesPerIssueDir(t *testing.T) {
	c, err := newDriverCache()
	if err != nil {
		t.Fatalf("newDriverCache: %v", err)
	}
	defer c.cleanup()

	dir := c.dirFor("42")
	if dir == "" {
		t.Fatal("dirFor returned empty path")
	}
	if filepath.Base(dir) != "42" {
		t.Errorf("dirFor must be keyed by issue number; got %q", dir)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("dirFor must create the directory: stat err=%v", err)
	}
}

// TestDriverCache_EvictRemovesOnlyThatIssue verifies evict removes just the
// named issue's entry, leaving sibling issue directories under the same
// cache root untouched.
func TestDriverCache_EvictRemovesOnlyThatIssue(t *testing.T) {
	c, err := newDriverCache()
	if err != nil {
		t.Fatalf("newDriverCache: %v", err)
	}
	defer c.cleanup()

	dir42 := c.dirFor("42")
	dir43 := c.dirFor("43")

	c.evict("42")

	if _, err := os.Stat(dir42); !os.IsNotExist(err) {
		t.Errorf("expected #42 cache dir removed, stat err=%v", err)
	}
	if _, err := os.Stat(dir43); err != nil {
		t.Errorf("expected #43 cache dir to survive #42's eviction: %v", err)
	}
}

// TestDriverCache_CleanupRemovesWholeRoot verifies cleanup removes every
// issue's entry along with the cache root itself.
func TestDriverCache_CleanupRemovesWholeRoot(t *testing.T) {
	c, err := newDriverCache()
	if err != nil {
		t.Fatalf("newDriverCache: %v", err)
	}
	root := c.root
	c.dirFor("1")
	c.dirFor("2")

	c.cleanup()

	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("expected cache root removed on cleanup, stat err=%v", err)
	}
}

// TestDriverCache_NilReceiverIsNoop verifies a nil *driverCache (e.g. tests
// that never call newDriverCache) degrades to "no cache" without panicking.
func TestDriverCache_NilReceiverIsNoop(t *testing.T) {
	var c *driverCache
	if got := c.dirFor("1"); got != "" {
		t.Errorf("nil driverCache.dirFor must return \"\", got %q", got)
	}
	c.evict("1") // must not panic
	c.cleanup()  // must not panic
}
