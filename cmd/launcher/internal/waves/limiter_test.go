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

	l.Resize(2)

	if got := l.Cap(); got != 2 {
		t.Fatalf("Cap: got %d, want 2", got)
	}
	if !l.TryAcquire() {
		t.Fatal("TryAcquire after Resize(2): want true, got false")
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

	l.Resize(1)

	if got := l.Live(); got != 2 {
		t.Fatalf("Live after Resize(1) with 2 already claimed: got %d, want 2 (lowering must not revoke live slots)", got)
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
