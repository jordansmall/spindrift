package local

import (
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// localHarness adapts LocalTracker to forgetest.Harness. Local has no native
// dependency-relationship concept — DepsOf is always body-sourced — so this
// harness does not implement forgetest.NativeCapable; the contract's non-
// native DepsOf branch covers it instead.
type localHarness struct {
	t    *testing.T
	dir  string
	lt   *LocalTracker
	next time.Time
}

func newLocalHarness(t *testing.T) *localHarness {
	dir := t.TempDir()
	return &localHarness{
		t:    t,
		dir:  dir,
		lt:   NewLocalTracker(dir, testLabels, forge.ResearchVerdictLabels()),
		next: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func (h *localHarness) Tracker() forge.IssueTracker { return h.lt }

// SeedIssue writes iss as a local issue file, stamping Created from a
// monotonically increasing counter so insertion order — the order
// forgetest's DispatchOrder scenario seeds in — matches the local adapter's
// canonical created-time sort.
func (h *localHarness) SeedIssue(iss forge.Issue) {
	created := h.next
	h.next = h.next.Add(time.Minute)
	writeLocalIssue(h.t, h.dir, iss.Number, localIssue{
		frontmatter: localFrontmatter{
			Title:   iss.Title,
			Created: created.Format(time.RFC3339),
		},
		body: iss.Body,
	})
}

// FailNativeDeps is a no-op: local has no native dependency lookup to fail.
func (h *localHarness) FailNativeDeps(num string) {}

func TestLocalTracker_TrackerContract(t *testing.T) {
	forgetest.RunTrackerContract(t, newLocalHarness(t))
}
