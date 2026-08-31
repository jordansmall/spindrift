package waves

import (
	"errors"
	"reflect"
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
