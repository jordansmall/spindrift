package daemon

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func TestBreakerUnderThresholdDoesNotTrip(t *testing.T) {
	clk := &fakeClock{}
	b := newBreaker(3, time.Minute)

	if _, crossed := b.recordAndCheck(clk.Now()); crossed {
		t.Fatalf("crossed after 1 failure, want threshold 3 not yet reached")
	}
	count, crossed := b.recordAndCheck(clk.Now())
	if crossed {
		t.Fatalf("crossed after 2 failures, want threshold 3 not yet reached")
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
}

func TestBreakerAtThresholdTrips(t *testing.T) {
	clk := &fakeClock{}
	b := newBreaker(3, time.Minute)

	b.recordAndCheck(clk.Now())
	b.recordAndCheck(clk.Now())
	count, crossed := b.recordAndCheck(clk.Now())

	if !crossed {
		t.Fatalf("not crossed after 3 failures, want threshold 3 reached")
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
}

func TestBreakerCrossesExactlyOnce(t *testing.T) {
	clk := &fakeClock{}
	b := newBreaker(3, time.Minute)

	b.recordAndCheck(clk.Now())
	b.recordAndCheck(clk.Now())
	_, first := b.recordAndCheck(clk.Now())
	_, second := b.recordAndCheck(clk.Now())

	if !first {
		t.Fatalf("3rd call crossed = false, want true (this is the call that reaches threshold)")
	}
	if second {
		t.Fatalf("4th call crossed = true, want false (threshold was already reached)")
	}
}

func TestBreakerAgedOutFailuresDoNotCount(t *testing.T) {
	clk := &fakeClock{}
	b := newBreaker(3, time.Minute)

	b.recordAndCheck(clk.Now())
	b.recordAndCheck(clk.Now())
	clk.Sleep(context.Background(), 2*time.Minute) // both failures above age out of the 1m window
	_, crossed := b.recordAndCheck(clk.Now())

	if crossed {
		t.Fatalf("crossed after only 1 failure within the window, want the 2 stale failures excluded")
	}
}

// TestBreakerDefaults_TripReachableAtOneSlot pins the reachability the
// breaker exists for at the smallest supported pool: at MAX_PARALLEL=1 a
// systemic fault's failures are one slot's own retries, spaced
// DAEMON_FAILURE_BACKOFF apart, so DAEMON_BREAKER_THRESHOLD of them must
// still fit inside DAEMON_BREAKER_WINDOW. Values that push that span past
// the window leave a 1-slot daemon burning its only slot until morning with
// no breaker_trip ever emitted. The shipped defaults come from
// lib/env-schema.nix, via shippedKnobDefaults (shippeddefaults_gen_test.go);
// cmd/launcher/daemon/main.go no longer holds them as constants.
func TestBreakerDefaults_TripReachableAtOneSlot(t *testing.T) {
	threshold, err := strconv.Atoi(shippedKnobDefaults["DAEMON_BREAKER_THRESHOLD"])
	if err != nil {
		t.Fatalf("parse DAEMON_BREAKER_THRESHOLD: %v", err)
	}
	backoff, err := time.ParseDuration(shippedKnobDefaults["DAEMON_FAILURE_BACKOFF"])
	if err != nil {
		t.Fatalf("parse DAEMON_FAILURE_BACKOFF: %v", err)
	}
	window, err := time.ParseDuration(shippedKnobDefaults["DAEMON_BREAKER_WINDOW"])
	if err != nil {
		t.Fatalf("parse DAEMON_BREAKER_WINDOW: %v", err)
	}
	span := time.Duration(threshold-1) * backoff
	if span >= window {
		t.Fatalf("a single slot can never trip the breaker: %d failures at %s apart span %s, outside the %s window",
			threshold, backoff, span, window)
	}
}
