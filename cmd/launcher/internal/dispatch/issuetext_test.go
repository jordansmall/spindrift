package dispatch

import (
	"errors"
	"testing"
)

// The injected issue text must be resolved once and reused for every Box of
// a dispatch: it sits inside the assembled prompt's stable prefix (issue
// #3445), so text that shifted between two Boxes of the same run would move
// every byte after it and forfeit the prefix-cache hit the injection buys.
func TestMemoizeIssueText_ResolvesOnce(t *testing.T) {
	calls := 0
	resolve := memoizeIssueText(func(number string) (string, error) {
		calls++
		return "body " + number, nil
	})

	for range 3 {
		text, err := resolve("7")
		if err != nil || text != "body 7" {
			t.Fatalf("resolve(7) = %q, %v; want %q, nil", text, err, "body 7")
		}
	}
	if calls != 1 {
		t.Errorf("resolver calls = %d, want 1", calls)
	}
}

// An error must not be cached: a later call for the same number gets a fresh
// attempt rather than inheriting the earlier failure.
func TestMemoizeIssueText_DoesNotCacheFailures(t *testing.T) {
	failure := errors.New("tracker unavailable")
	resolve := memoizeIssueText(func(string) (string, error) {
		if failure != nil {
			return "", failure
		}
		return "the body", nil
	})

	if _, err := resolve("7"); err == nil {
		t.Fatal("resolve(7) = nil error, want the resolver's error")
	}
	failure = nil
	text, err := resolve("7")
	if err != nil || text != "the body" {
		t.Fatalf("resolve(7) after recovery = %q, %v; want %q, nil", text, err, "the body")
	}
}
