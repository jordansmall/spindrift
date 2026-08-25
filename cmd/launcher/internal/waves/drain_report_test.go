package waves

import (
	"strings"
	"testing"
	"time"
)

// TestDrainReport_Duration verifies Duration() returns the wall-clock gap
// between StaleAt and DrainedAt, and returns exactly zero (not near-zero)
// when the two are the same time.Time value (the zero-length-drain case).
func TestDrainReport_Duration(t *testing.T) {
	stale := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	drained := stale.Add(5 * time.Second)

	r := DrainReport{StaleAt: stale, DrainedAt: drained}
	if got, want := r.Duration(), 5*time.Second; got != want {
		t.Errorf("Duration() = %v, want %v", got, want)
	}

	zero := DrainReport{StaleAt: stale, DrainedAt: stale}
	if got := zero.Duration(); got != 0 {
		t.Errorf("Duration() on zero-length drain = %v, want exactly 0", got)
	}
}

// TestDrainReport_Console verifies Console()'s exact rendered line -- a
// substring-only check (e.g. "1" alone) would pass for several wrong
// renderings (a swapped field, a missing separator), so this pins the whole
// format the docs and loop scripts depend on, not just its presence.
func TestDrainReport_Console(t *testing.T) {
	stale := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	r := DrainReport{
		StaleAt:      stale,
		DrainedAt:    stale.Add(5 * time.Second),
		FreeSlotSecs: 8.4,
		HeldBack:     1,
	}

	got := r.Console()
	want := "==> drain: 5s idle, 8.4 free-slot-s, 1 issue(s) held back\n"
	if got != want {
		t.Errorf("Console() = %q, want %q", got, want)
	}
}

// TestDrainReport_HostLog verifies HostLog() starts with "DRAIN " and
// contains correctly formatted key=value pairs, parseable by a simple
// strings.Split/regex a loop script would use.
func TestDrainReport_HostLog(t *testing.T) {
	stale := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	r := DrainReport{
		StaleAt:      stale,
		DrainedAt:    stale.Add(5 * time.Second),
		FreeSlotSecs: 8.4,
		HeldBack:     1,
	}

	got := r.HostLog()
	if !strings.HasPrefix(got, "DRAIN ") {
		t.Errorf("HostLog() = %q, want prefix %q", got, "DRAIN ")
	}
	for _, want := range []string{"durationSeconds=5.000", "freeSlotSeconds=8.400", "heldBack=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("HostLog() = %q, want substring %q", got, want)
		}
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("HostLog() = %q, want trailing newline", got)
	}
}

// TestDrainReport_Console_HeldBackUnknown verifies Console() renders an
// explicit "unknown" clause -- not a fabricated "0 issue(s) held back" --
// when HeldBackUnknown is true, since a transient discover error during the
// stale-drain report (#2678) means the held-back count was never actually
// confirmed.
func TestDrainReport_Console_HeldBackUnknown(t *testing.T) {
	stale := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	r := DrainReport{
		StaleAt:         stale,
		DrainedAt:       stale.Add(5 * time.Second),
		FreeSlotSecs:    8.4,
		HeldBack:        0,
		HeldBackUnknown: true,
	}

	got := r.Console()
	for _, want := range []string{"5s", "8.4", "unknown"} {
		if !strings.Contains(got, want) {
			t.Errorf("Console() = %q, want substring %q", got, want)
		}
	}
	if strings.Contains(got, "0 issue(s) held back") {
		t.Errorf("Console() = %q, must not render a fabricated zero-held-back count", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("Console() = %q, want trailing newline", got)
	}
}

// TestDrainReport_HostLog_HeldBackUnknown verifies HostLog() renders
// "heldBack=unknown" -- not "heldBack=0" -- when HeldBackUnknown is true, so
// an external loop script totaling drain.log across iterations never sums a
// fabricated zero into its total.
func TestDrainReport_HostLog_HeldBackUnknown(t *testing.T) {
	stale := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	r := DrainReport{
		StaleAt:         stale,
		DrainedAt:       stale.Add(5 * time.Second),
		FreeSlotSecs:    8.4,
		HeldBack:        0,
		HeldBackUnknown: true,
	}

	got := r.HostLog()
	if !strings.HasPrefix(got, "DRAIN ") {
		t.Errorf("HostLog() = %q, want prefix %q", got, "DRAIN ")
	}
	if !strings.Contains(got, "heldBack=unknown") {
		t.Errorf("HostLog() = %q, want substring %q", got, "heldBack=unknown")
	}
	if strings.Contains(got, "heldBack=0") {
		t.Errorf("HostLog() = %q, must not render a fabricated heldBack=0", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("HostLog() = %q, want trailing newline", got)
	}
}

// TestDrainReport_ZeroLengthDrain verifies Console() and HostLog() render a
// zero duration cleanly (not blank/garbage) when StaleAt == DrainedAt.
func TestDrainReport_ZeroLengthDrain(t *testing.T) {
	stale := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	r := DrainReport{StaleAt: stale, DrainedAt: stale, FreeSlotSecs: 0, HeldBack: 0}

	console := r.Console()
	if !strings.Contains(console, "0s") {
		t.Errorf("Console() = %q, want substring %q", console, "0s")
	}

	hostLog := r.HostLog()
	if !strings.Contains(hostLog, "durationSeconds=0.000") {
		t.Errorf("HostLog() = %q, want substring %q", hostLog, "durationSeconds=0.000")
	}
}
