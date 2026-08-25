package waves

import (
	"testing"
	"time"
)

// TestLimiter_TryAcquireGatedByCap verifies that TryAcquire succeeds only
// while live is below cap, and fails without side effects once cap is
// reached.
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

// TestLimiter_ReleaseFreesASlot verifies that Release lets a subsequent
// TryAcquire succeed once live drops back under cap.
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

// TestLimiter_ResizeUpAllowsMoreAcquires verifies that raising the cap lets
// a TryAcquire that would otherwise fail succeed immediately, and Cap
// reports the new value.
func TestLimiter_ResizeUpAllowsMoreAcquires(t *testing.T) {
	l := NewLimiter(1)
	if !l.TryAcquire() {
		t.Fatal("TryAcquire: want true, got false")
	}
	if l.TryAcquire() {
		t.Fatal("TryAcquire at cap: want false, got true")
	}

	l.ResizeDelta(1)

	if got := l.Cap(); got != 2 {
		t.Fatalf("Cap: got %d, want 2", got)
	}
	if !l.TryAcquire() {
		t.Fatal("TryAcquire after ResizeDelta(1): want true, got false")
	}
}

// TestLimiter_ResizeDownNeverRevokesLiveSlots verifies that lowering the cap
// below the current live count leaves already-claimed slots untouched — it
// only gates future TryAcquire calls until Release brings live back under
// the new cap (ADR 0023: lowering never terminates anything).
func TestLimiter_ResizeDownNeverRevokesLiveSlots(t *testing.T) {
	l := NewLimiter(2)
	if !l.TryAcquire() || !l.TryAcquire() {
		t.Fatal("both initial TryAcquire calls: want true")
	}

	l.ResizeDelta(-1)

	if got := l.Live(); got != 2 {
		t.Fatalf("Live after ResizeDelta(-1) with 2 already claimed: got %d, want 2 (lowering must not revoke live slots)", got)
	}
	if l.TryAcquire() {
		t.Fatal("TryAcquire over the lowered cap: want false, got true")
	}

	l.Release()

	if l.TryAcquire() {
		t.Fatal("TryAcquire with live==cap after one Release: want false, got true")
	}
	if got := l.Live(); got != 1 {
		t.Fatalf("Live after one Release: got %d, want 1", got)
	}

	l.Release()

	if !l.TryAcquire() {
		t.Fatal("TryAcquire once live sank under the lowered cap: want true, got false")
	}
}

// TestLimiter_ResizeDeltaAppliesRelativeToCurrentCap verifies that
// ResizeDelta computes the new cap from the current cap plus delta under a
// single lock acquisition, clamps to a floor of 1, and still signals
// Resized on a raise.
func TestLimiter_ResizeDeltaAppliesRelativeToCurrentCap(t *testing.T) {
	l := NewLimiter(2)

	l.ResizeDelta(1)
	if got := l.Cap(); got != 3 {
		t.Fatalf("Cap after ResizeDelta(1): got %d, want 3", got)
	}

	l.ResizeDelta(-5)
	if got := l.Cap(); got != 1 {
		t.Fatalf("Cap after ResizeDelta(-5): got %d, want 1 (clamped floor)", got)
	}

	select {
	case <-l.Resized():
	default:
		t.Fatal("Resized: want a signal from the earlier raise, got none")
	}
}

// TestLimiter_ResizeCoalescesResizedSignalUnderRapidRaises verifies that two
// back-to-back ResizeDelta raises, called through the real public API with
// no listener parked on Resized, coalesce into a single buffered signal —
// the second raise's non-blocking send hits signalResized's default branch
// and is dropped (issue #766's coalescing fix; issue #1134 exercises it
// through ResizeDelta itself instead of mutating Limiter's internal
// fields).
func TestLimiter_ResizeCoalescesResizedSignalUnderRapidRaises(t *testing.T) {
	l := NewLimiter(1)

	l.ResizeDelta(1)
	l.ResizeDelta(1)

	if got := l.Cap(); got != 3 {
		t.Fatalf("Cap after two ResizeDelta(1) raises: got %d, want 3", got)
	}

	select {
	case <-l.Resized():
	default:
		t.Fatal("Resized: want one signal from the two raises, got none")
	}

	select {
	case <-l.Resized():
		t.Fatal("Resized: want no second signal (coalesced), got one")
	default:
	}
}

// TestLimiter_ResizeDeltaNoopDoesNotSignalResized verifies that ResizeDelta
// only signals Resized when the clamped cap actually changes: calling it
// with a delta that keeps the cap pinned at the floor of 1 (e.g. two
// consecutive lowers past the floor) must not emit a Resized signal for the
// second, no-op call. This pins the `if newCap != oldCap` guard in
// ResizeDelta — without it, the second call's unconditional signal would
// make this test fail.
func TestLimiter_ResizeDeltaNoopDoesNotSignalResized(t *testing.T) {
	l := NewLimiter(2)

	l.ResizeDelta(-5)
	if got := l.Cap(); got != 1 {
		t.Fatalf("Cap after ResizeDelta(-5) from 2: got %d, want 1 (clamped floor)", got)
	}

	select {
	case <-l.Resized():
	default:
		t.Fatal("Resized: want a signal from the first (actual) lower to the floor, got none")
	}

	l.ResizeDelta(-5)
	if got := l.Cap(); got != 1 {
		t.Fatalf("Cap after second ResizeDelta(-5) at floor: got %d, want 1 (still clamped)", got)
	}

	select {
	case <-l.Resized():
		t.Fatal("Resized: want no signal from a no-op resize (cap already at floor), got one")
	default:
	}
}

// TestLimiter_AcquireBlocksUntilReleased verifies that a blocking Acquire at
// cap waits for a concurrent Release rather than returning immediately —
// the drop-in replacement for dispatchWave's buffered-channel semaphore.
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
