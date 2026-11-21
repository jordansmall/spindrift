package main

import (
	"fmt"
	"io"
	"os"
	"sync"
)

// rawCopy is the scratch copy of the CLI's stdout a shadowed Driver's stream
// filter is replayed from: the Pass log holds the native filter's output, which
// cannot rebuild the filter's input. A nil *rawCopy is the unshadowed case.
//
// Writes are best-effort. An error is remembered and stops further writes, and
// path then reports "" so the summary says there was no copy rather than
// comparing against a truncated one.
type rawCopy struct {
	mu   sync.Mutex
	f    *os.File
	name string
	err  error
}

// newRawCopy returns a copy for the named Driver, or nil when no shadow replay
// will read it: no applicable shadow row, or a native Strategy with no stream
// filter. It lives in the temp dir, not beside the Pass log, whose directory
// CI uploads as an artifact.
func newRawCopy(driverName string, stderr io.Writer) *rawCopy {
	_, native, ok := shadowOf(driverName)
	if !ok || native.NewStreamFilter == nil {
		return nil
	}
	f, err := os.CreateTemp("", "spindrift-raw-stdout-*")
	if err != nil {
		// Stderr, not the summary: that says "no raw stdout copy" for a replay
		// too, so an unwritable temp dir would be indistinguishable.
		fmt.Fprintf(stderr, "driver-exec: raw stdout copy: %v\n", err)
		return nil
	}
	return &rawCopy{f: f, name: f.Name()}
}

// Write never fails: an error here must not reach the CLI's stdout pipe.
func (c *rawCopy) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f != nil && c.err == nil {
		_, c.err = c.f.Write(p)
	}
	return len(p), nil
}

// path closes the copy to further writes and returns where it is, or "" when
// there is none or a write to it failed.
func (c *rawCopy) path() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f != nil {
		if err := c.f.Close(); err != nil && c.err == nil {
			c.err = err
		}
		c.f = nil
	}
	if c.err != nil {
		return ""
	}
	return c.name
}

// remove deletes the copy. It is idempotent: the SIGTERM exit and mainRun's
// deferred cleanup may both reach it.
func (c *rawCopy) remove() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.f != nil {
		_ = c.f.Close()
		c.f = nil
	}
	_ = os.Remove(c.name)
}
