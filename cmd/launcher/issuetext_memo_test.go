package main

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// The injected issue text must be resolved once and reused for every Box of
// a dispatch: it sits inside the assembled prompt's stable prefix (issue
// #3445), so text that shifted between two Boxes of the same run would move
// every byte after it and forfeit the prefix-cache hit the injection buys.
func TestMemoizedIssueTextResolvesOncePerIssue(t *testing.T) {
	fake := forge.NewFake()
	fake.SetIssue(forge.Issue{Number: "7", Title: "Seventh", Body: "the body"})
	fake.SetIssue(forge.Issue{Number: "8", Title: "Eighth", Body: "another body"})

	resolve := memoizedIssueText(fake)

	for range 3 {
		text, err := resolve("7")
		if err != nil {
			t.Fatalf("resolve(7): %v", err)
		}
		if text != "the body" {
			t.Fatalf("resolve(7) = %q, want %q", text, "the body")
		}
	}
	if _, err := resolve("8"); err != nil {
		t.Fatalf("resolve(8): %v", err)
	}

	if got, want := len(fake.IssueCalls), 2; got != want {
		t.Errorf("tracker Issue calls = %d (%v), want %d -- one per distinct issue", got, fake.IssueCalls, want)
	}
}

// A failed lookup is a transient tracker condition, not a fact about the
// issue, so it must not be cached: the next Box gets a fresh attempt rather
// than inheriting a permanent empty.
func TestMemoizedIssueTextDoesNotCacheFailures(t *testing.T) {
	fake := forge.NewFake()
	fake.IssueErr = errors.New("tracker unavailable")

	resolve := memoizedIssueText(fake)

	if _, err := resolve("7"); err == nil {
		t.Fatal("resolve(7) = nil error, want the tracker's error")
	}

	fake.IssueErr = nil
	fake.SetIssue(forge.Issue{Number: "7", Title: "Seventh", Body: "the body"})

	text, err := resolve("7")
	if err != nil {
		t.Fatalf("resolve(7) after recovery: %v", err)
	}
	if text != "the body" {
		t.Fatalf("resolve(7) after recovery = %q, want %q", text, "the body")
	}
}
