package dispatch

import (
	"fmt"
	"os"
	"path/filepath"
)

// cache holds per-issue host directories, mounted writable into a Box so the
// claude Driver can resume its prior session on a fix pass (issue #427). The
// Factory owns the root for its whole lifetime; each Dispatch takes its
// per-issue dir at construction and evicts it via Close (issue #441). Removal
// goes through remove, the runner's, because Box-created entries may be
// subuid-owned under rootless podman (issue #5387).
type cache struct {
	root   string
	remove func(string) error
}

func newCache(remove func(string) error) (*cache, error) {
	root, err := os.MkdirTemp("", "spindrift-driver-cache-*")
	if err != nil {
		return nil, fmt.Errorf("driver cache: %w", err)
	}
	return &cache{root: root, remove: remove}, nil
}

// dirFor returns the per-issue cache directory, keyed strictly <cache>/<issue>
// so a resumed session cannot cross into another issue's trust domain. A nil
// receiver and a creation failure both return "", which the Dispatch treats as
// no mount: a fix box degrades to the cold-context flow rather than erroring.
//
// The dir must be Box-writable (issue #5387): under rootless podman the host
// user maps to root in the Box, not uid 1000. The 0700 MkdirTemp root still
// keeps other host users out.
func (c *cache) dirFor(num string) string {
	if c == nil {
		return ""
	}
	dir := filepath.Join(c.root, num)
	if err := mkdirBoxWritable(dir); err != nil {
		return ""
	}
	return dir
}

// evict removes one issue's cache entry. The run's outcome is already decided
// by the time Dispatch.Close calls it, so a removal failure is only warned about.
func (c *cache) evict(num string) {
	if c == nil {
		return
	}
	if err := c.remove(filepath.Join(c.root, num)); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? #%s: evict driver session cache: %v\n", num, err)
	}
}

// cleanup removes the whole cache root.
func (c *cache) cleanup() {
	if c == nil {
		return
	}
	if err := c.remove(c.root); err != nil {
		fmt.Fprintf(os.Stderr, "    ?? driver session cache cleanup: %v\n", err)
	}
}
