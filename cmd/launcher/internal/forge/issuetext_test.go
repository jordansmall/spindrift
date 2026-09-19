package forge_test

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// issueOnlyTracker hides every optional interface the wrapped value also
// implements, CommentLister included: Go promotes only the embedded
// interface's own method set, not the dynamic value's extra methods. That
// makes it a real "tracker that is not a CommentLister" built from the
// Fake rather than a second hand-rolled tracker.
type issueOnlyTracker struct {
	forge.IssueTracker
}

func TestIssueText(t *testing.T) {
	t.Run("body only when tracker is not a CommentLister", func(t *testing.T) {
		f := forge.NewFake()
		f.SetIssue(forge.Issue{Number: "1", Body: "the body"})
		tracker := issueOnlyTracker{IssueTracker: f}

		got, err := forge.IssueText(tracker, "1")
		if err != nil {
			t.Fatalf("IssueText: %v", err)
		}
		if got != "the body" {
			t.Errorf("IssueText = %q, want %q", got, "the body")
		}
	})

	t.Run("body plus comments", func(t *testing.T) {
		f := forge.NewFake()
		f.SetIssue(forge.Issue{Number: "1", Body: "the body"})
		f.CommentsFor = map[string][]forge.Comment{
			"1": {
				{Author: "alice", CreatedAt: "2024-01-01T00:00:00Z", Body: "first"},
				{Author: "bob", CreatedAt: "2024-01-02T00:00:00Z", Body: "second"},
			},
		}

		got, err := forge.IssueText(f, "1")
		if err != nil {
			t.Fatalf("IssueText: %v", err)
		}
		want := "the body\n\n## Comments\n\n" +
			"alice (2024-01-01T00:00:00Z): first\n" +
			"bob (2024-01-02T00:00:00Z): second\n"
		if got != want {
			t.Errorf("IssueText = %q, want %q", got, want)
		}
	})

	t.Run("more than 10 comments keeps only the last 10, in order", func(t *testing.T) {
		f := forge.NewFake()
		f.SetIssue(forge.Issue{Number: "1", Body: "body"})
		var comments []forge.Comment
		for i := 0; i < 15; i++ {
			comments = append(comments, forge.Comment{
				Author:    "author",
				CreatedAt: "t",
				Body:      "comment-" + string(rune('a'+i)),
			})
		}
		f.CommentsFor = map[string][]forge.Comment{"1": comments}

		got, err := forge.IssueText(f, "1")
		if err != nil {
			t.Fatalf("IssueText: %v", err)
		}
		for i := 0; i < 5; i++ {
			body := "comment-" + string(rune('a'+i))
			if strings.Contains(got, body) {
				t.Errorf("IssueText contains dropped early comment %q", body)
			}
		}
		for i := 5; i < 15; i++ {
			body := "comment-" + string(rune('a'+i))
			if !strings.Contains(got, body) {
				t.Errorf("IssueText missing expected retained comment %q", body)
			}
		}
		// The retained window stays oldest-first: comment-f (index 5)
		// comes before comment-o (index 14).
		if strings.Index(got, "comment-f") > strings.Index(got, "comment-o") {
			t.Errorf("IssueText = %q, comments out of order", got)
		}
	})

	t.Run("comments-fetch error degrades to body-only", func(t *testing.T) {
		f := forge.NewFake()
		f.SetIssue(forge.Issue{Number: "1", Body: "the body"})
		f.CommentsErr = map[string]error{"1": errors.New("comments unavailable")}

		got, err := forge.IssueText(f, "1")
		if err != nil {
			t.Fatalf("IssueText: %v", err)
		}
		if got != "the body" {
			t.Errorf("IssueText = %q, want %q", got, "the body")
		}
	})

	t.Run("issue fetch error is returned", func(t *testing.T) {
		f := forge.NewFake()
		f.IssueErr = errors.New("issue unavailable")

		_, err := forge.IssueText(f, "1")
		if err == nil {
			t.Fatal("IssueText: want error, got nil")
		}
	})

	t.Run("truncation", func(t *testing.T) {
		f := forge.NewFake()
		f.SetIssue(forge.Issue{Number: "1", Body: strings.Repeat("x", 100*1024)})

		got, err := forge.IssueText(f, "1")
		if err != nil {
			t.Fatalf("IssueText: %v", err)
		}
		if !strings.Contains(got, "[truncated: issue text exceeded 64KB]") {
			t.Errorf("IssueText missing truncation marker, got tail %q", got[len(got)-80:])
		}
		if len(got) >= 100*1024 {
			t.Errorf("IssueText len = %d, want well under original 100KB", len(got))
		}
	})

	t.Run("byte-identical when tracker is not a LinkedIssueLister", func(t *testing.T) {
		// Linked-issue rendering (slice 2) only activates when the tracker
		// also implements LinkedIssueLister, which a remote tracker (github,
		// jira) never does. Neither issueOnlyTracker nor the Fake implements
		// it (only LocalTracker does), so both must still render the
		// pre-existing text unchanged.
		f := forge.NewFake()
		f.SetIssue(forge.Issue{Number: "1", Body: "the body"})
		f.CommentsFor = map[string][]forge.Comment{
			"1": {{Author: "alice", CreatedAt: "2024-01-01T00:00:00Z", Body: "first"}},
		}
		tracker := issueOnlyTracker{IssueTracker: f}

		got, err := forge.IssueText(tracker, "1")
		if err != nil {
			t.Fatalf("IssueText(issueOnlyTracker): %v", err)
		}
		// issueOnlyTracker hides CommentLister too, so its output stays
		// body-only even though the Fake has a comment set.
		if got != "the body" {
			t.Errorf("IssueText(issueOnlyTracker) = %q, want %q", got, "the body")
		}

		want := "the body\n\n## Comments\n\nalice (2024-01-01T00:00:00Z): first\n"
		got, err = forge.IssueText(f, "1")
		if err != nil {
			t.Fatalf("IssueText(Fake): %v", err)
		}
		if got != want {
			t.Errorf("IssueText(Fake) = %q, want %q", got, want)
		}
	})
}
