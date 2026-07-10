package dispatch

import (
	"fmt"
	"os"
	"path/filepath"
)

// cache manages the ephemeral, launcher-lifetime, per-issue host directories
// mounted writable into a Box so the claude Driver can resume its prior
// session on a fix pass (issue #427). The Factory owns one cache root for its
// whole lifetime; each Dispatch it produces takes its per-issue dir at
// construction and evicts it via Close (issue #441).
type cache struct {
	root string
}

// newCache creates a fresh cache root under the OS temp dir.
func newCache() (*cache, error) {
	root, err := os.MkdirTemp("", "spindrift-driver-cache-*")
	if err != nil {
		return nil, fmt.Errorf("driver cache: %w", err)
	}
	return &cache{root: root}, nil
}

// dirFor creates (if absent) and returns the writable per-issue cache
// directory for issue num, keyed strictly <cache>/<issue> so a resumed
// session can never cross into another issue's trust domain. A nil receiver
// and a creation failure both return "", which the Dispatch treats as "no
// mount" -- a fix box degrades to the cold-context flow, never an error.
func (c *cache) dirFor(num string) string {
	if c == nil {
		return ""
	}
	dir := filepath.Join(c.root, num)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	return dir
}

// evict removes one issue's cache entry. Called by Dispatch.Close.
// Best-effort: the outcome it follows has already been decided, so a
// removal failure is not fatal.
func (c *cache) evict(num string) {
	if c == nil {
		return
	}
	_ = os.RemoveAll(filepath.Join(c.root, num))
}

// cleanup removes the whole cache root. Called once by Factory.Cleanup.
func (c *cache) cleanup() {
	if c == nil {
		return
	}
	_ = os.RemoveAll(c.root)
}
