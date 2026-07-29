// guard.go seals the host-taint halt decision — previously split across the
// main package's classifyStaleOutcome and staleRevTracker — inside the
// freshness package as a single Guard type, so the record/clear discipline
// that keeps a non-converging divergence from looping forever (issues
// #2113, #2128) lives beside the Result it classifies.
package freshness

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/dispatch"
)

// Disposition classifies a stale Probe Result under continuous dispatch.
type Disposition int

const (
	// Rebuild indicates content staleness — a new base tip a rebuild will
	// fix.
	Rebuild Disposition = iota
	// HostTainted indicates a non-converging divergence — the same base tip
	// a caller already rebuilt against is still stale (the signature of a
	// host-system derivation reaching the image graph).
	HostTainted
)

// Guard wraps the persisted prior-stale-rev memory and classifies a stale
// Probe Result, keeping record/clear discipline internal so a future edit
// cannot reintroduce the perpetual-rebuild loop by clearing state at the
// wrong moment (issues #2113, #2128).
type Guard struct {
	path string
}

// NewGuard returns a Guard backed by <pwd>/.spindrift/logs/freshness-stale-rev.
func NewGuard(pwd string) Guard {
	return Guard{path: filepath.Join(dispatch.HostLogDirFor(pwd), "freshness-stale-rev")}
}

// Classify decides whether a stale Probe Result is content staleness
// (Rebuild) or a non-converging host-tainted divergence (HostTainted),
// updating the persisted prior-stale-rev memory as a side effect: it
// records the rev on Rebuild and clears the memory on HostTainted. The
// empty-tip-tag guard lives here beside the post-condition it depends on —
// a stuck eval/derive failure (Applicable, !Fresh, Rev set, TipTag=="")
// repeats at the same rev but is NOT host taint, so it stays Rebuild.
func (g Guard) Classify(res Result) Disposition {
	if NonConverging(res.Rev, g.prior()) && res.TipTag != "" {
		_ = g.clear()
		return HostTainted
	}
	_ = g.record(res.Rev)
	return Rebuild
}

// Reset forgets any armed divergence — the queue-drained reset point.
func (g Guard) Reset() error {
	return g.clear()
}

// Prior returns the rev recorded by the previous run, or "" — read-only
// observation; mutation stays internal via record/clear. Exported so tests
// can assert the armed/cleared state that Classify manages internally.
func (g Guard) Prior() string {
	return g.prior()
}

// prior returns the rev recorded by the previous run, or "" if none is
// recorded or the file cannot be read (a missing/unreadable state file is
// treated as "no prior stale", never an error — detection must fail open).
func (g Guard) prior() string {
	b, err := os.ReadFile(g.path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// record writes rev as the last stale rev, creating the logs dir if needed.
func (g Guard) record(rev string) error {
	if err := os.MkdirAll(filepath.Dir(g.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(g.path, []byte(rev+"\n"), 0o644)
}

// clear removes the state file; a missing file is not an error.
func (g Guard) clear() error {
	err := os.Remove(g.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
