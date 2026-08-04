package forge

import (
	"errors"
	"testing"
)

// TestWalkPages_CallsFetchUntilDone verifies WalkPages invokes fetch for
// pages 1, 2, 3, ... in order, stopping exactly once fetch reports done,
// with no call beyond that page.
func TestWalkPages_CallsFetchUntilDone(t *testing.T) {
	const wantCalls = 3
	var gotPages []int

	err := WalkPages(func(page int) (bool, error) {
		gotPages = append(gotPages, page)
		return len(gotPages) >= wantCalls, nil
	})
	if err != nil {
		t.Fatalf("WalkPages returned unexpected error: %v", err)
	}

	if len(gotPages) != wantCalls {
		t.Fatalf("fetch called %d times, want %d", len(gotPages), wantCalls)
	}
	for i, page := range gotPages {
		if page != i+1 {
			t.Fatalf("gotPages = %v, want sequential pages starting at 1", gotPages)
		}
	}
}

// TestWalkPages_PropagatesError verifies WalkPages stops and returns the
// first error fetch produces, without calling fetch again afterward.
func TestWalkPages_PropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	calls := 0

	err := WalkPages(func(page int) (bool, error) {
		calls++
		return false, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WalkPages error = %v, want %v", err, wantErr)
	}
	if calls != 1 {
		t.Fatalf("fetch called %d times after error, want exactly 1", calls)
	}
}
