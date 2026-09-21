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
// b.cap) at the actual values the daemon ships: DAEMON_IDLE_FLOOR (5m) and
// DAEMON_IDLE_CAP (30m) from lib/env-schema.nix. Those aren't a
// power-of-two multiple of each other, so this is the only pair (among this
// file's tests) where doubling overshoots the cap and the clamp itself
// has to run: 20m doubles to 40m, which must clamp to 30m.
func TestIdleBackoffCapsAtShippedDefaults(t *testing.T) {
	floor, err := time.ParseDuration(shippedKnobDefaults["DAEMON_IDLE_FLOOR"])
	if err != nil {
		t.Fatalf("parse DAEMON_IDLE_FLOOR default: %v", err)
	}
	cap, err := time.ParseDuration(shippedKnobDefaults["DAEMON_IDLE_CAP"])
	if err != nil {
		t.Fatalf("parse DAEMON_IDLE_CAP default: %v", err)
	}
	b := newIdleBackoff(floor, cap)

	got := []time.Duration{}
	for i := 0; i < 5; i++ {
		got = append(got, b.next())
	}

	// 4*floor (20m) is still under cap; doubling that (40m) is what
	// overshoots and must clamp -- that's the branch this test exists to pin.
	want := []time.Duration{floor, 2 * floor, 4 * floor, cap, cap}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("next() sequence = %v, want %v", got, want)
		}
	}
}

func TestKindBackoffFreshIsRunnable(t *testing.T) {
	k := newKindBackoff(1, 8)
	if !k.runnable(time.Now()) {
		t.Fatalf("fresh kindBackoff: want runnable, got gated")
	}
	if until, gated := k.readyAt(time.Now()); gated || !until.IsZero() {
		t.Fatalf("fresh kindBackoff: readyAt() = (%v, %v), want (zero, false)", until, gated)
	}
	if k.jammedNow() {
		t.Fatalf("fresh kindBackoff: want jammed=false")
	}
}

func TestKindBackoffGatesUntilDeadline(t *testing.T) {
	k := newKindBackoff(time.Second, 8*time.Second)
	now := time.Now()

	wait := k.markNoWork(now, false)
	if wait != time.Second {
		t.Fatalf("markNoWork() = %v, want %v", wait, time.Second)
	}

	if k.runnable(now) {
		t.Fatalf("runnable(now) right after markNoWork: want gated")
	}
	if k.runnable(now.Add(wait - 1)) {
		t.Fatalf("runnable(now+wait-1ns): want still gated")
	}
	// Landing exactly on the deadline must be runnable: a fake clock that
	// sleeps exactly `wait` should not be gated an extra tick.
	if !k.runnable(now.Add(wait)) {
		t.Fatalf("runnable(now+wait): want runnable")
	}

	until, gated := k.readyAt(now)
	if !gated || !until.Equal(now.Add(wait)) {
		t.Fatalf("readyAt() = (%v, %v), want (%v, true)", until, gated, now.Add(wait))
	}
}

// TestKindBackoffReadyAtElapsedDeadline pins the fix for the finding at
// backoff.go:98: once the deadline itself has passed, readyAt must report
// not-gated (matching runnable), never a stale `until` a caller could go on
// to publish as a future nextCheck.
func TestKindBackoffReadyAtElapsedDeadline(t *testing.T) {
	k := newKindBackoff(time.Second, 8*time.Second)
	now := time.Now()

	wait := k.markNoWork(now, false)
	deadline := now.Add(wait)

	if until, gated := k.readyAt(deadline.Add(-1)); !gated || !until.Equal(deadline) {
		t.Fatalf("readyAt(deadline-1ns) = (%v, %v), want (%v, true)", until, gated, deadline)
	}
	if _, gated := k.readyAt(deadline); gated {
		t.Fatalf("readyAt(deadline): want not gated, the deadline itself is runnable")
	}
	if _, gated := k.readyAt(deadline.Add(1)); gated {
		t.Fatalf("readyAt(deadline+1ns): want not gated")
	}
}

func TestKindBackoffMarkNoWorkGrowsLikeIdleBackoff(t *testing.T) {
	k := newKindBackoff(time.Second, 8*time.Second)
	now := time.Now()

	got := []time.Duration{}
	for i := 0; i < 4; i++ {
		got = append(got, k.markNoWork(now, false))
	}

	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("markNoWork() sequence = %v, want %v", got, want)
		}
	}
}

func TestKindBackoffJammedFlag(t *testing.T) {
	k := newKindBackoff(time.Second, 8*time.Second)
	now := time.Now()

	k.markNoWork(now, true)
	if !k.jammedNow() {
		t.Fatalf("jammedNow() after markNoWork(jammed=true): want true")
	}

	k.markNoWork(now, false)
	if k.jammedNow() {
		t.Fatalf("jammedNow() after markNoWork(jammed=false): want false")
	}
}

func TestKindBackoffReset(t *testing.T) {
	k := newKindBackoff(time.Second, 8*time.Second)
	now := time.Now()

	k.markNoWork(now, true)
	k.markNoWork(now, true)

	k.reset()

	if !k.runnable(now) {
		t.Fatalf("runnable(now) after reset: want runnable")
	}
	if k.jammedNow() {
		t.Fatalf("jammedNow() after reset: want false")
	}
	if until, gated := k.readyAt(now); gated || !until.IsZero() {
		t.Fatalf("readyAt() after reset = (%v, %v), want (zero, false)", until, gated)
	}
	if got := k.markNoWork(now, false); got != time.Second {
		t.Fatalf("markNoWork() after reset = %v, want floor (%v)", got, time.Second)
	}
}
