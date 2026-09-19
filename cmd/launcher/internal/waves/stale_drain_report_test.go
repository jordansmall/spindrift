package waves

import (
	"strings"
	"testing"
	"time"
)

// A zero-length drain must return exactly zero, not a near-zero duration.
func TestStaleDrainReport_Duration(t *testing.T) {
	stale := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	drained := stale.Add(5 * time.Second)

	r := StaleDrainReport{StaleAt: stale, DrainedAt: drained}
	if got, want := r.Duration(), 5*time.Second; got != want {
		t.Errorf("Duration() = %v, want %v", got, want)
	}

	zero := StaleDrainReport{StaleAt: stale, DrainedAt: stale}
	if got := zero.Duration(); got != 0 {
		t.Errorf("Duration() on zero-length drain = %v, want exactly 0", got)
	}
}

// This compares the whole line, because a substring check would still pass for
// a swapped field or a missing separator. The docs and loop scripts depend on
// the exact format.
func TestStaleDrainReport_Console(t *testing.T) {
	stale := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	r := StaleDrainReport{
		StaleAt:      stale,
		DrainedAt:    stale.Add(5 * time.Second),
		FreeSlotSecs: 8.4,
		HeldBack:     1,
	}

	got := r.Console()
	want := "==> stale-drain: 5s idle, 8.4 free-slot-s, 1 issue(s) held back\n"
	if got != want {
		t.Errorf("Console() = %q, want %q", got, want)
	}
}

// The prefix and key=value shape are what a loop script splits on, so they are
// part of the contract rather than formatting detail.
func TestStaleDrainReport_HostLog(t *testing.T) {
	stale := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	r := StaleDrainReport{
		StaleAt:      stale,
		DrainedAt:    stale.Add(5 * time.Second),
		FreeSlotSecs: 8.4,
		HeldBack:     1,
	}

	got := r.HostLog()
	if !strings.HasPrefix(got, "STALE_DRAIN ") {
		t.Errorf("HostLog() = %q, want prefix %q", got, "STALE_DRAIN ")
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

// A transient discover error (#2678) leaves the held-back count unconfirmed, so
// Console() must say "unknown" rather than fabricate a zero.
func TestStaleDrainReport_Console_HeldBackUnknown(t *testing.T) {
	stale := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	r := StaleDrainReport{
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

// A loop script totals stale-drain.log across iterations, so an unconfirmed
// count must log as unknown and never sum in as a fabricated zero (#2678).
func TestStaleDrainReport_HostLog_HeldBackUnknown(t *testing.T) {
	stale := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	r := StaleDrainReport{
		StaleAt:         stale,
		DrainedAt:       stale.Add(5 * time.Second),
		FreeSlotSecs:    8.4,
		HeldBack:        0,
		HeldBackUnknown: true,
	}

	got := r.HostLog()
	if !strings.HasPrefix(got, "STALE_DRAIN ") {
		t.Errorf("HostLog() = %q, want prefix %q", got, "STALE_DRAIN ")
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

func TestStaleDrainReport_ZeroLengthDrain(t *testing.T) {
	stale := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	r := StaleDrainReport{StaleAt: stale, DrainedAt: stale, FreeSlotSecs: 0, HeldBack: 0}

	console := r.Console()
	if !strings.Contains(console, "0s") {
		t.Errorf("Console() = %q, want substring %q", console, "0s")
	}

	hostLog := r.HostLog()
	if !strings.Contains(hostLog, "durationSeconds=0.000") {
		t.Errorf("HostLog() = %q, want substring %q", hostLog, "durationSeconds=0.000")
	}
}
