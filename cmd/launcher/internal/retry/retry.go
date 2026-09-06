// Package retry owns the launcher's linear-backoff schedule behind an
// injectable Clock seam, so the backoff formula, cap, jitter meaning, and the
// negative-input clamp are decided and tested in one place (issue #2154),
// plus Policy, the transient-retry tuning built once from the launcher's raw
// config knobs and carried into dispatch, settle, and waves Configs
// (issue #2928).
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

// Policy bundles the transient-retry tuning -- max attempts, backoff unit,
// hold jitter -- built once from the launcher's raw config knobs (by
// retryPolicy in cmd/launcher/main.go) and carried through the dispatch,
// settle, and waves Configs instead of each copying and converting the same
// three ints independently. (issue #2928)
type Policy struct {
	// Max caps retry attempts: a transient backoff retry count (dispatch,
	// waves) or a rate-limit hold-cycle count (dispatch), compared against a
	// counter that increments per attempt.
	Max int
	// Unit is the linear-backoff step; see LinearBackoff for how it combines
	// with an attempt number into a sleep duration.
	Unit time.Duration
	// Jitter is added to a rate-limit hold's wait, and is the whole wait
	// when the known reset time has already passed; settle also adds it to
	// its rebase-push backoff (issue #2095). A loop that neither holds nor
	// pushes (e.g. waves' re-discover) leaves it unused.
	Jitter time.Duration
}

// Backoff builds the linear backoff a retry loop sleeps on c. It steps on
// Unit alone: Jitter is a separate nudge only some callers want, so they
// layer it onto the result rather than get it by default (issue #3073).
func (p Policy) Backoff(c Clock) LinearBackoff {
	return LinearBackoff{Unit: p.Unit, Clock: c}
}
