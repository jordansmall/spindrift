package waves

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// TestFakeQueue_RecordsCallsAndReturnsConfigured exercises all four Queue
// methods on a FakeQueue and asserts each records its call and round-trips
// its configured return value.
func TestFakeQueue_RecordsCallsAndReturnsConfigured(t *testing.T) {
	wantBatch := Batch{Issues: []Issue{{Number: "1"}}}
	wantDiscoverErr := errors.New("discover boom")
	wantClaimErr := errors.New("claim boom")
	wantReport := StaleDrainReport{StaleAt: time.Unix(1, 0), DrainedAt: time.Unix(2, 0)}

	f := NewFakeQueue()
	f.DiscoverReturn = wantBatch
	f.DiscoverErr = wantDiscoverErr
	f.ClaimErr = wantClaimErr
	f.PendingReturn = 7

	gotBatch, gotDiscoverErr := f.Discover()
	if !reflect.DeepEqual(gotBatch, wantBatch) {
		t.Errorf("Discover() batch = %+v, want %+v", gotBatch, wantBatch)
	}
	if !errors.Is(gotDiscoverErr, wantDiscoverErr) {
		t.Errorf("Discover() err = %v, want %v", gotDiscoverErr, wantDiscoverErr)
	}
	if f.DiscoverCalls != 1 {
		t.Errorf("DiscoverCalls = %d, want 1", f.DiscoverCalls)
	}

	gotClaimErr := f.Claim("42")
	if !errors.Is(gotClaimErr, wantClaimErr) {
		t.Errorf("Claim() err = %v, want %v", gotClaimErr, wantClaimErr)
	}
	if !reflect.DeepEqual(f.ClaimCalls, []string{"42"}) {
		t.Errorf("ClaimCalls = %v, want [42]", f.ClaimCalls)
	}

	gotPending, gotPendingErr := f.Pending(nil)
	if gotPending != 7 {
		t.Errorf("Pending() = %d, want 7", gotPending)
	}
	if gotPendingErr != nil {
		t.Errorf("Pending() err = %v, want nil", gotPendingErr)
	}
	if f.PendingCalls != 1 {
		t.Errorf("PendingCalls = %d, want 1", f.PendingCalls)
	}

	wantPendingErr := errors.New("pending boom")
	f.PendingErr = wantPendingErr
	if _, gotPendingErr := f.Pending(nil); !errors.Is(gotPendingErr, wantPendingErr) {
		t.Errorf("Pending() err = %v, want %v", gotPendingErr, wantPendingErr)
	}

	f.ReportStaleDrain(wantReport)
	if !reflect.DeepEqual(f.ReportStaleDrainCalls, []StaleDrainReport{wantReport}) {
		t.Errorf("ReportStaleDrainCalls = %+v, want [%+v]", f.ReportStaleDrainCalls, wantReport)
	}
}

// TestFakeQueue_DiscoverFunc_ScriptsPerCallResults exercises DiscoverFunc,
// which lets a test script different Discover results across successive
// calls (e.g. a rate-limited failure on the first call, success on the
// second) -- something the fixed DiscoverReturn/DiscoverErr fields can't
// express.
func TestFakeQueue_DiscoverFunc_ScriptsPerCallResults(t *testing.T) {
	wantErr := errors.New("rate limited")
	wantBatch := Batch{Issues: []Issue{{Number: "1"}}}

	f := NewFakeQueue()
	f.DiscoverFunc = func(callN int) (Batch, error) {
		if callN == 1 {
			return Batch{}, wantErr
		}
		return wantBatch, nil
	}

	gotBatch, gotErr := f.Discover()
	if !reflect.DeepEqual(gotBatch, Batch{}) {
		t.Errorf("Discover() call 1 batch = %+v, want empty Batch", gotBatch)
	}
	if !errors.Is(gotErr, wantErr) {
		t.Errorf("Discover() call 1 err = %v, want %v", gotErr, wantErr)
	}

	gotBatch, gotErr = f.Discover()
	if !reflect.DeepEqual(gotBatch, wantBatch) {
		t.Errorf("Discover() call 2 batch = %+v, want %+v", gotBatch, wantBatch)
	}
	if gotErr != nil {
		t.Errorf("Discover() call 2 err = %v, want nil", gotErr)
	}

	if f.DiscoverCalls != 2 {
		t.Errorf("DiscoverCalls = %d, want 2", f.DiscoverCalls)
	}
}

// TestFakeQueue_Discover_DiscoverFuncCanCallBackIntoFakeQueue pins that a
// DiscoverFunc callback may call another FakeQueue method (e.g. Pending) on
// the same FakeQueue without self-deadlocking. Discover must not hold f.mu
// while invoking DiscoverFunc.
func TestFakeQueue_Discover_DiscoverFuncCanCallBackIntoFakeQueue(t *testing.T) {
	f := NewFakeQueue()
	f.PendingReturn = 3
	f.DiscoverFunc = func(callN int) (Batch, error) {
		got, err := f.Pending(nil)
		if err != nil {
			t.Fatalf("Pending() inside DiscoverFunc: %v", err)
		}
		if got != 3 {
			t.Errorf("Pending() inside DiscoverFunc = %d, want 3", got)
		}
		return Batch{}, nil
	}

	if _, err := f.Discover(); err != nil {
		t.Fatalf("Discover() err = %v", err)
	}
	if f.PendingCalls != 1 {
		t.Errorf("PendingCalls = %d, want 1", f.PendingCalls)
	}
}

// TestFakeQueue_PendingFunc exercises PendingFunc, which lets a test wire a
// real exclusion-computing closure (e.g. fakePending) through
// FakeQueue.Pending(claimed) instead of hardcoding an expected constant --
// the closure recomputes its count from the caller-supplied claimed set
// rather than returning a fixed value, and it takes priority over
// PendingReturn/PendingErr. claimed is seeded at the call site (issue
// #3035), not via Claim/Claimed -- Pending forwards whatever map its caller
// hands it, verbatim.
func TestFakeQueue_PendingFunc(t *testing.T) {
	f := NewFakeQueue()

	wantClaimed := map[string]bool{"1": true}
	f.PendingFunc = func(claimed map[string]bool) (int, error) {
		if !reflect.DeepEqual(claimed, wantClaimed) {
			t.Errorf("PendingFunc claimed = %v, want %v", claimed, wantClaimed)
		}
		return len(claimed), nil
	}
	f.PendingReturn = 99
	f.PendingErr = errors.New("should be ignored")

	got, err := f.Pending(wantClaimed)
	if err != nil {
		t.Fatalf("Pending() err = %v, want nil", err)
	}
	if got != 1 {
		t.Errorf("Pending() = %d, want 1", got)
	}
}

// TestFakeQueue_Pending_ConcurrentWithClaim pins that FakeQueue's own
// mu-guarded bookkeeping (Claimed, ClaimCalls, PendingCalls) stays race-free
// under a concurrent Claim and Pending -- Pending(claimed) no longer touches
// Claimed at all (issue #3035: claimed is the caller's own map, forwarded
// verbatim), so this fixed map is never shared with the concurrent Claim's
// writes; what's still worth pinning under -race is FakeQueue itself, not
// the caller-owned map. Run with -race to catch a regression.
func TestFakeQueue_Pending_ConcurrentWithClaim(t *testing.T) {
	f := NewFakeQueue()
	claimed := map[string]bool{"1": true}
	f.PendingFunc = func(claimed map[string]bool) (int, error) {
		n := 0
		for range claimed {
			n++
		}
		return n, nil
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = f.Claim("1")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_, _ = f.Pending(claimed)
		}
	}()
	wg.Wait()
}

// TestFakeQueue_Claim_IsIdempotent pins that claiming the same issue number
// twice is safe: both calls record a ClaimCalls entry and return the same
// ClaimErr, and Claimed reports the issue claimed after either call --
// mirroring how headlessQueue.Claim tracks its own claimed map.
func TestFakeQueue_Claim_IsIdempotent(t *testing.T) {
	f := NewFakeQueue()

	firstErr := f.Claim("1")
	if firstErr != nil {
		t.Fatalf("Claim() first call err = %v, want nil", firstErr)
	}
	if !f.Claimed["1"] {
		t.Errorf(`Claimed["1"] = false after first Claim, want true`)
	}

	secondErr := f.Claim("1")
	if secondErr != firstErr {
		t.Errorf("Claim() second call err = %v, want %v (same as first call)", secondErr, firstErr)
	}
	if !f.Claimed["1"] {
		t.Errorf(`Claimed["1"] = false after second Claim, want true`)
	}

	if want := []string{"1", "1"}; !reflect.DeepEqual(f.ClaimCalls, want) {
		t.Errorf("ClaimCalls = %v, want %v", f.ClaimCalls, want)
	}
}

// TestFakeQueue_EnsureLogDirExists_ReturnsNil verifies FakeQueue's
// EnsureLogDirExists is a plain no-op: it returns nil and never touches the
// filesystem (issue #3036), same as every other in-memory FakeQueue method.
func TestFakeQueue_EnsureLogDirExists_ReturnsNil(t *testing.T) {
	f := NewFakeQueue()

	dir := t.TempDir()
	logDir := filepath.Join(dir, ".spindrift", "logs")

	if err := f.EnsureLogDirExists(); err != nil {
		t.Fatalf("EnsureLogDirExists() = %v, want nil", err)
	}
	if _, err := os.Stat(logDir); !os.IsNotExist(err) {
		t.Fatalf("Stat(%s): got err=%v, want a not-exist error (FakeQueue must not touch the filesystem)", logDir, err)
	}
}
