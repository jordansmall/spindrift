package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCache_DirForCreatesPerIssueDir(t *testing.T) {
	c, err := newCache(os.RemoveAll)
	if err != nil {
		t.Fatalf("newCache: %v", err)
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

func TestCache_EvictRemovesOnlyThatIssue(t *testing.T) {
	c, err := newCache(os.RemoveAll)
	if err != nil {
		t.Fatalf("newCache: %v", err)
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

func TestCache_CleanupRemovesWholeRoot(t *testing.T) {
	c, err := newCache(os.RemoveAll)
	if err != nil {
		t.Fatalf("newCache: %v", err)
	}
	root := c.root
	c.dirFor("1")
	c.dirFor("2")

	c.cleanup()

	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("expected cache root removed on cleanup, stat err=%v", err)
	}
}

// A Factory whose cache creation failed holds a nil *cache, so every method
// must degrade to "no cache" instead of panicking.
func TestCache_NilReceiverIsNoop(t *testing.T) {
	var c *cache
	if got := c.dirFor("1"); got != "" {
		t.Errorf("nil cache.dirFor must return \"\", got %q", got)
	}
	c.evict("1") // must not panic
	c.cleanup()  // must not panic
}

// Issue #5387: Box-created entries may be owned by a remapped subuid under
// rootless podman, so a plain os.RemoveAll cannot delete them; both removals
// must go through the injected remover.
func TestCache_RemovalGoesThroughInjectedRemover(t *testing.T) {
	var got []string
	c, err := newCache(func(p string) error {
		got = append(got, p)
		return nil
	})
	if err != nil {
		t.Fatalf("newCache: %v", err)
	}
	defer os.RemoveAll(c.root)

	c.evict("42")
	c.cleanup()

	want := []string{filepath.Join(c.root, "42"), c.root}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("remover calls = %v, want %v", got, want)
	}
}
