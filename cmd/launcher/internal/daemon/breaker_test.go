package daemon

import (
	"context"
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
