package daemon

import (
	"testing"
	"time"
)

func TestIdleBackoffGrowsAndCaps(t *testing.T) {
	b := newIdleBackoff(1, 8)

	got := []time.Duration{}
	for i := 0; i < 6; i++ {
		got = append(got, b.next())
	}

	want := []time.Duration{1, 2, 4, 8, 8, 8}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("next() sequence = %v, want %v", got, want)
		}
	}
}

func TestIdleBackoffReset(t *testing.T) {
	b := newIdleBackoff(1, 8)

	b.next() // 1
	b.next() // 2
	b.next() // 4

	b.reset()

	if got := b.next(); got != 1 {
		t.Fatalf("next() after reset = %v, want floor (1)", got)
	}
}

// TestIdleBackoffCapsAtShippedDefaults pins the clamp branch (b.cur =
// b.cap) at the actual values the daemon ships: daemonIdleFloor (5m) and
// daemonIdleCap (30m) from cmd/launcher/daemon/main.go. Those aren't a
// power-of-two multiple of each other, so this is the only pair (among this
// file's tests) where doubling overshoots the cap and the clamp itself
// has to run: 20m doubles to 40m, which must clamp to 30m.
func TestIdleBackoffCapsAtShippedDefaults(t *testing.T) {
	floor := 5 * time.Minute
	cap := 30 * time.Minute
	b := newIdleBackoff(floor, cap)

	got := []time.Duration{}
	for i := 0; i < 5; i++ {
		got = append(got, b.next())
	}

	want := []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("next() sequence = %v, want %v", got, want)
		}
	}
}
