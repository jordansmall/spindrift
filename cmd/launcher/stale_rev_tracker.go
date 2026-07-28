package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
)

// staleRevTracker persists, across separate launcher process invocations,
// the base-tip rev the last continuous-dispatch run exited stale on. The
// non-convergence detector (issue #2113) reads it on the next run: a stale
// verdict at the same rev after dogfood.sh already rebuilt to that tip is a
// structural (host-tainted) divergence, not content staleness. The state is
// a single-line file under the run's logs dir.
type staleRevTracker struct {
	path string
}

// newStaleRevTracker returns a tracker backed by <pwd>/logs/freshness-stale-rev.
func newStaleRevTracker(pwd string) staleRevTracker {
	return staleRevTracker{path: filepath.Join(dispatch.HostLogDirFor(pwd), "freshness-stale-rev")}
}

// prior returns the rev recorded by the previous run, or "" if none is
// recorded or the file cannot be read (a missing/unreadable state file is
// treated as "no prior stale", never an error — detection must fail open).
func (t staleRevTracker) prior() string {
	b, err := os.ReadFile(t.path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// record writes rev as the last stale rev, creating the logs dir if needed.
func (t staleRevTracker) record(rev string) error {
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(t.path, []byte(rev+"\n"), 0o644)
}

// clear removes the state file; a missing file is not an error.
func (t staleRevTracker) clear() error {
	err := os.Remove(t.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
