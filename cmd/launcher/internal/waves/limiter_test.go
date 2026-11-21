package waves

import (
	"testing"
	"time"
)

func TestLimiter_TryAcquireGatedByCap(t *testing.T) {
	l := NewLimiter(2)

	if !l.TryAcquire() {
		t.Fatal("first TryAcquire: want true, got false")
	}
	if !l.TryAcquire() {
		t.Fatal("second TryAcquire: want true, got false")
	}
	if l.TryAcquire() {
		t.Fatal("third TryAcquire: want false (cap reached), got true")
	}
	if got := l.Live(); got != 2 {
		t.Fatalf("Live: got %d, want 2", got)
	}
}

func TestLimiter_ReleaseFreesASlot(t *testing.T) {
	l := NewLimiter(1)

	if !l.TryAcquire() {
		t.Fatal("TryAcquire: want true, got false")
	}
	if l.TryAcquire() {
		t.Fatal("TryAcquire while full: want false, got true")
	}

	l.Release()

	if !l.TryAcquire() {
		t.Fatal("TryAcquire after Release: want true, got false")
	}
	if got := l.Live(); got != 1 {
		t.Fatalf("Live: got %d, want 1", got)
	}
}

// Acquire replaced dispatchWave's buffered-channel semaphore, so at cap it
// must block until a concurrent Release rather than return immediately.
func TestLimiter_AcquireBlocksUntilReleased(t *testing.T) {
	l := NewLimiter(1)
	l.Acquire()

	acquired := make(chan struct{})
	go func() {
		l.Acquire()
		close(acquired)
	}()

	select {
	case <-acquired:
		t.Fatal("second Acquire returned before Release; want it blocked")
	case <-time.After(50 * time.Millisecond):
	}

	l.Release()

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second Acquire never returned after Release")
	}
}

// mustPanicUnmatchedRelease fails the test unless fn panics.
func mustPanicUnmatchedRelease(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("unmatched Release: want panic, got none")
		}
	}()
	fn()
}

func TestLimiter_ReleaseWithNothingHeldPanics(t *testing.T) {
	l := NewLimiter(2)

	mustPanicUnmatchedRelease(t, l.Release)

	if got := l.Live(); got != 0 {
		t.Fatalf("Live after unmatched Release: got %d, want 0", got)
	}
}

func TestLimiter_DoubleReleasePanicsAndKeepsBound(t *testing.T) {
	l := NewLimiter(3)
	l.Acquire()
	l.Acquire()
	l.Release()
	l.Release()

	mustPanicUnmatchedRelease(t, l.Release)

	if got := l.Live(); got != 0 {
		t.Fatalf("Live after unmatched Release: got %d, want 0", got)
	}
	for i := 0; i < 3; i++ {
		if !l.TryAcquire() {
			t.Fatalf("TryAcquire %d: want true, got false", i+1)
		}
	}
	if l.TryAcquire() {
		t.Fatal("TryAcquire past cap: want false (bound widened by unmatched Release), got true")
	}
}
