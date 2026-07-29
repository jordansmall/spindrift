// Package retry owns the launcher's single linear-backoff schedule behind an
// injectable Clock seam, so the backoff formula, cap, jitter meaning, and the
// negative-input clamp are decided and tested in one place. (issue #2154)
package retry

import "time"

// Clock is injectable for tests; RealClock() gives production behaviour.
type Clock struct {
	Now   func() time.Time
	Sleep func(time.Duration)
}

// RealClock returns a Clock backed by the real time.Now / time.Sleep.
func RealClock() Clock { return Clock{Now: time.Now, Sleep: time.Sleep} }

// LinearBackoff is a linear backoff schedule: attempt N (1-based) waits
// Unit*N + Jitter, sleeping through the Clock. Cap (when > 0) is the ceiling.
// A negative Unit or Jitter clamps to zero, so a misconfigured knob degrades
// to immediate retry instead of a negative sleep; the final duration is
// likewise never negative.
type LinearBackoff struct {
	Unit   time.Duration
	Jitter time.Duration
	Cap    time.Duration // 0 == unbounded
	Clock  Clock
}

// Duration computes the backoff duration for attempt N without sleeping.
func (b LinearBackoff) Duration(attempt int) time.Duration {
	unit := b.Unit
	if unit < 0 {
		unit = 0
	}
	jitter := b.Jitter
	if jitter < 0 {
		jitter = 0
	}

	d := unit*time.Duration(attempt) + jitter
	if d < 0 {
		d = 0
	}
	if b.Cap > 0 && d > b.Cap {
		d = b.Cap
	}
	return d
}

// Do sleeps through the Clock for the duration computed by Duration.
func (b LinearBackoff) Do(attempt int) {
	b.Clock.Sleep(b.Duration(attempt))
}
