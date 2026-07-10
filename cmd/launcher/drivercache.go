package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// driverCache manages the ephemeral, launcher-lifetime, per-issue host
// directories mounted writable into a Box so the claude Driver can resume
// its prior session on a fix pass (issue #427). The launcher only creates,
// mounts, and evicts these directories -- their contents are opaque Driver
// state it never reads, copies, parses, or chmods.
//
// A fresh `spindrift dispatch` (or a crash) always starts with an empty
// cache; reconcileStranded's cold fallback covers whatever session context
// that loses.
type driverCache struct {
	root string
}

// newDriverCache creates a fresh cache root under the OS temp dir.
func newDriverCache() (*driverCache, error) {
	root, err := os.MkdirTemp("", "spindrift-driver-cache-*")
	if err != nil {
		return nil, fmt.Errorf("driver cache: %w", err)
	}
	return &driverCache{root: root}, nil
}

// dirFor creates (if absent) and returns the writable per-issue cache
// directory for issue num, keyed strictly <cache>/<issue> so a resumed
// session can never cross into another issue's trust domain. A nil receiver
// (no cache root, e.g. a test that never calls newDriverCache) and a
// creation failure both return "", which the runner seam treats as "no
// mount" -- a fix box degrades to the cold-context flow, never an error.
func (d *driverCache) dirFor(num string) string {
	if d == nil {
		return ""
	}
	dir := filepath.Join(d.root, num)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	return dir
}

// evict removes one issue's cache entry. Called when the issue reaches a
// terminal dispatch state (Complete/Failed). Best-effort: the outcome it
// follows has already been decided, so a removal failure is not fatal.
func (d *driverCache) evict(num string) {
	if d == nil {
		return
	}
	_ = os.RemoveAll(filepath.Join(d.root, num))
}

// cleanup removes the whole cache root. Called once, on exit, by whichever
// entry point (run/selectiveListDispatch/recoverIssue) created it.
func (d *driverCache) cleanup() {
	if d == nil {
		return
	}
	_ = os.RemoveAll(d.root)
}
