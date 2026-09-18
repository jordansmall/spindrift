package dispatch

import (
	"fmt"
	"os"
	"path/filepath"
)

// cache holds per-issue host directories, mounted writable into a Box so the
// claude Driver can resume its prior session on a fix pass (issue #427). The
// Factory owns the root for its whole lifetime; each Dispatch takes its
// per-issue dir at construction and evicts it via Close (issue #441).
type cache struct {
	root string
}

func newCache() (*cache, error) {
	root, err := os.MkdirTemp("", "spindrift-driver-cache-*")
	if err != nil {
		return nil, fmt.Errorf("driver cache: %w", err)
	}
	return &cache{root: root}, nil
}

// dirFor returns the per-issue cache directory, keyed strictly <cache>/<issue>
// so a resumed session cannot cross into another issue's trust domain. A nil
// receiver and a creation failure both return "", which the Dispatch treats as
// no mount: a fix box degrades to the cold-context flow rather than erroring.
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

// evict removes one issue's cache entry. The run's outcome is already decided
// by the time Dispatch.Close calls it, so a removal failure is not fatal.
func (c *cache) evict(num string) {
	if c == nil {
		return
	}
	_ = os.RemoveAll(filepath.Join(c.root, num))
}

// cleanup removes the whole cache root.
func (c *cache) cleanup() {
	if c == nil {
		return
	}
	_ = os.RemoveAll(c.root)
}
