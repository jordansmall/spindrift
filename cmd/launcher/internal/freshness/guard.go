// guard.go holds the host-taint halt decision as a single Guard type, so the
// record/clear discipline that keeps a non-converging divergence from looping
// forever (issues #2113, #2128) lives beside the Result it classifies.
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
	// Rebuild is content staleness: a new base tip that a rebuild fixes.
	Rebuild Disposition = iota
	// HostTainted is a non-converging divergence: the same base tip a caller
	// already rebuilt against is still stale, which is the signature of a
	// host-system derivation reaching the image graph.
	HostTainted
)

// Guard classifies a stale Probe Result against the persisted prior-stale-rev
// memory. Record/clear discipline stays internal so a later edit cannot
// reintroduce the perpetual-rebuild loop by clearing state at the wrong moment
// (issues #2113, #2128).
type Guard struct {
	path string
}

// NewGuard returns a Guard backed by <pwd>/.spindrift/logs/freshness-stale-rev.
func NewGuard(pwd string) Guard {
	return Guard{path: filepath.Join(dispatch.HostLogDirFor(pwd), "freshness-stale-rev")}
}

// Classify decides whether a stale Result is content staleness (Rebuild) or a
// non-converging host-tainted divergence (HostTainted), recording the rev on
// Rebuild and clearing the memory on HostTainted. A stuck eval or a
// launcher-only-stale verdict leaves TipTag empty and repeats at the same rev
// without being host taint, so the empty-TipTag check keeps those on Rebuild.
func (g Guard) Classify(res Result) Disposition {
	if NonConverging(res.Rev, g.prior()) && res.TipTag != "" {
		_ = g.clear()
		return HostTainted
	}
	_ = g.record(res.Rev)
	return Rebuild
}

// Reset forgets any armed divergence. Callers reset once the queue drains.
func (g Guard) Reset() error {
	return g.clear()
}

// Prior returns the rev recorded by the previous run, or "". It is read-only
// so that record and clear stay internal; tests use it to assert the
// armed/cleared state Classify manages.
func (g Guard) Prior() string {
	return g.prior()
}

// A missing or unreadable state file means "no prior stale", never an error:
// detection must fail open.
func (g Guard) prior() string {
	b, err := os.ReadFile(g.path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (g Guard) record(rev string) error {
	if err := os.MkdirAll(filepath.Dir(g.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(g.path, []byte(rev+"\n"), 0o644)
}

func (g Guard) clear() error {
	err := os.Remove(g.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
