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

// DiscoverFunc scripts a different result per call, such as a rate-limited
// failure first and success second, which the fixed
// DiscoverReturn/DiscoverErr fields cannot express.
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

// Discover must not hold f.mu while it invokes DiscoverFunc, or a callback
// that calls back into the same FakeQueue self-deadlocks.
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

// PendingFunc recomputes its count from the caller-supplied claimed set and
// takes priority over PendingReturn/PendingErr, which this test seeds to
// values it must ignore. claimed comes from the call site, not Claim/Claimed.
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

// FakeQueue's mu-guarded bookkeeping must stay race-free under a concurrent
// Claim and Pending. Run with -race to catch a regression.
func TestFakeQueue_Pending_ConcurrentWithClaim(t *testing.T) {
	f := NewFakeQueue()

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
			_, _ = f.Pending(nil)
		}
	}()
	wg.Wait()

	if len(f.ClaimCalls) != 100 {
		t.Errorf("ClaimCalls = %d, want 100", len(f.ClaimCalls))
	}
	if f.PendingCalls != 100 {
		t.Errorf("PendingCalls = %d, want 100", f.PendingCalls)
	}
}

// Claiming the same issue number twice must stay safe, mirroring how
// headlessQueue.Claim tracks its own claimed map.
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

// EnsureLogDirExists must be a no-op that never touches the filesystem
// (issue #3036), like every other in-memory FakeQueue method.
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
