package waves

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

// TestFake_RecordsCallsAndReturnsConfigured exercises all four Queue
// methods on a Fake and asserts each records its call and round-trips its
// configured return value.
func TestFake_RecordsCallsAndReturnsConfigured(t *testing.T) {
	wantBatch := Batch{Issues: []Issue{{Number: "1"}}}
	wantDiscoverErr := errors.New("discover boom")
	wantClaimErr := errors.New("claim boom")
	wantReport := StaleDrainReport{StaleAt: time.Unix(1, 0), DrainedAt: time.Unix(2, 0)}

	f := NewFake()
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

	gotPending, gotPendingErr := f.Pending()
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
	if _, gotPendingErr := f.Pending(); !errors.Is(gotPendingErr, wantPendingErr) {
		t.Errorf("Pending() err = %v, want %v", gotPendingErr, wantPendingErr)
	}

	f.ReportStaleDrain(wantReport)
	if !reflect.DeepEqual(f.ReportStaleDrainCalls, []StaleDrainReport{wantReport}) {
		t.Errorf("ReportStaleDrainCalls = %+v, want [%+v]", f.ReportStaleDrainCalls, wantReport)
	}
}

// TestFake_DiscoverFunc_ScriptsPerCallResults exercises DiscoverFunc, which
// lets a test script different Discover results across successive calls
// (e.g. a rate-limited failure on the first call, success on the second) --
// something the fixed DiscoverReturn/DiscoverErr fields can't express.
func TestFake_DiscoverFunc_ScriptsPerCallResults(t *testing.T) {
	wantErr := errors.New("rate limited")
	wantBatch := Batch{Issues: []Issue{{Number: "1"}}}

	f := NewFake()
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

// TestFake_Discover_DiscoverFuncCanCallBackIntoFake pins that a DiscoverFunc
// callback may call another Fake method (e.g. Pending) on the same Fake
// without self-deadlocking. Discover must not hold f.mu while invoking
// DiscoverFunc.
func TestFake_Discover_DiscoverFuncCanCallBackIntoFake(t *testing.T) {
	f := NewFake()
	f.PendingReturn = 3
	f.DiscoverFunc = func(callN int) (Batch, error) {
		got, err := f.Pending()
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

// TestFake_PendingFunc exercises PendingFunc, which lets a test wire a real
// exclusion-computing closure (e.g. fakePending) through Fake.Pending()
// instead of hardcoding an expected constant -- the closure recomputes its
// count from the current Claimed set rather than returning a fixed value,
// and it takes priority over PendingReturn/PendingErr.
func TestFake_PendingFunc(t *testing.T) {
	f := NewFake()
	if err := f.Claim("1"); err != nil {
		t.Fatalf("Claim() err = %v, want nil", err)
	}

	wantClaimed := map[string]bool{"1": true}
	f.PendingFunc = func(claimed map[string]bool) (int, error) {
		if !reflect.DeepEqual(claimed, wantClaimed) {
			t.Errorf("PendingFunc claimed = %v, want %v", claimed, wantClaimed)
		}
		return len(claimed), nil
	}
	f.PendingReturn = 99
	f.PendingErr = errors.New("should be ignored")

	got, err := f.Pending()
	if err != nil {
		t.Fatalf("Pending() err = %v, want nil", err)
	}
	if got != 1 {
		t.Errorf("Pending() = %d, want 1", got)
	}
}

// TestFake_Pending_ConcurrentWithClaim pins that a PendingFunc ranging over
// its claimed argument never races with a concurrent Claim writing to
// Fake.Claimed -- Pending must hand PendingFunc a snapshot, not the live
// map. Run with -race to catch a regression back to the live reference.
func TestFake_Pending_ConcurrentWithClaim(t *testing.T) {
	f := NewFake()
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
			_, _ = f.Pending()
		}
	}()
	wg.Wait()
}

// TestFake_Claim_IsIdempotent pins that claiming the same issue number
// twice is safe: both calls record a ClaimCalls entry and return the same
// ClaimErr, and Claimed reports the issue claimed after either call --
// mirroring how headlessQueue.Claim tracks its own claimed map.
func TestFake_Claim_IsIdempotent(t *testing.T) {
	f := NewFake()

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
