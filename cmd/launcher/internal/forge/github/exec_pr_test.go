package github

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// A transient-looking stderr (not a genuine conflict) must stay
// errors.Is-detectable as forge.ErrMergeTransient now that the error text
// comes from ghCommandErrText rather than a hand-rolled format string (issue
// #2864).
func TestMerge_TransientFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "merge" ]; then
  printf 'HTTP 503: Service Unavailable\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels)
	err := c.Merge("https://github.com/owner/repo/pull/42")
	if err == nil {
		t.Fatal("Merge: want error, got nil")
	}
	if !errors.Is(err, forge.ErrMergeTransient) {
		t.Fatalf("Merge error must be errors.Is forge.ErrMergeTransient; got: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 503: Service Unavailable") {
		t.Fatalf("Merge error must surface gh's stderr; got: %v", err)
	}
}

// A non-conflict, non-transient stderr must still reach the returned error
// through ghCommandErrText (issue #2864).
func TestMerge_GenericFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "merge" ]; then
  printf 'HTTP 403: permission denied\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels)
	err := c.Merge("https://github.com/owner/repo/pull/42")
	if err == nil {
		t.Fatal("Merge: want error, got nil")
	}
	if errors.Is(err, forge.ErrMergeTransient) {
		t.Fatalf("Merge error must not be forge.ErrMergeTransient; got: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 403: permission denied") {
		t.Fatalf("Merge error must surface gh's stderr; got: %v", err)
	}
}

// The error must carry gh's stderr text. The old path called cmd.Run with no
// Stderr wired, so it degraded to a bare "exit status 1" (issue #2864).
func TestEnqueueAutoMerge_FailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "merge" ]; then
  printf 'HTTP 422: Unprocessable Entity\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels)
	err := c.EnqueueAutoMerge("https://github.com/owner/repo/pull/42")
	if err == nil {
		t.Fatal("EnqueueAutoMerge: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 422: Unprocessable Entity") {
		t.Fatalf("EnqueueAutoMerge error must surface gh's stderr; got: %v", err)
	}
}

// runGHReadyToggle's error must carry gh's stderr text now that it routes
// through ghCommandErr instead of a hand-rolled suffix (issue #2864).
func TestMarkReady_FailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "ready" ]; then
  printf 'HTTP 404: Not Found\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels)
	err := c.MarkReady("https://github.com/owner/repo/pull/42")
	if err == nil {
		t.Fatal("MarkReady: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 404: Not Found") {
		t.Fatalf("MarkReady error must surface gh's stderr; got: %v", err)
	}
}

// CreateLabel used to merge stdout and stderr through CombinedOutput. It now
// leaves Stderr nil for cmd.Output to populate, like every other
// ghCommandErr site (issue #2864).
func TestCreateLabel_FailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "label" ] && [ "$2" = "create" ]; then
  printf 'HTTP 422: label already exists\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels)
	err := c.CreateLabel("bug", "a bug", "ff0000")
	if err == nil {
		t.Fatal("CreateLabel: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 422: label already exists") {
		t.Fatalf("CreateLabel error must surface gh's stderr; got: %v", err)
	}
}

// The error must carry git's stderr text. The old path called cmd.Run with no
// Stderr wired, so a clone failure degraded to a bare "exit status 1" (issue
// #2864). The server rejects the clone's token, so git fails with its own
// "Authentication failed" stderr; the PR is registered first because Rebase
// reads the branch names before it clones.
func TestRebase_CloneFailureSurfacesStderr(t *testing.T) {
	h := newCodeForgeHarness(t)
	h.srv.expectToken("some-other-token")
	url := h.SeedLandable("42")

	err := h.cf.Rebase(url)
	if err == nil {
		t.Fatal("Rebase: want error, got nil")
	}
	if !strings.Contains(err.Error(), "Authentication failed") {
		t.Fatalf("Rebase error must surface git's stderr; got: %v", err)
	}
}

func TestCommentPR_PostsBodyOnPR(t *testing.T) {
	dir := prependFakeGH(t, "exit 0\n")
	c := NewExecClient("owner/repo", testLabels)
	if err := c.CommentPR("https://github.com/owner/repo/pull/42", "held for review"); err != nil {
		t.Fatalf("CommentPR: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := "pr\ncomment\nhttps://github.com/owner/repo/pull/42\n--body\nheld for review\n"
	if string(got) != want {
		t.Fatalf("gh argv = %q, want %q", got, want)
	}
}

func TestCommentPR_FailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "comment" ]; then
  printf 'HTTP 404: Not Found\n' >&2
  exit 1
fi
`)
	c := NewExecClient("owner/repo", testLabels)
	err := c.CommentPR("https://github.com/owner/repo/pull/42", "x")
	if err == nil || !strings.Contains(err.Error(), "HTTP 404: Not Found") {
		t.Fatalf("CommentPR error must surface gh's stderr; got: %v", err)
	}
}

func TestMarkReady_TransientFailureWrapsErrMarkReadyTransient(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "ready" ]; then
  printf 'HTTP 502: Bad Gateway\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels)
	err := c.MarkReady("https://github.com/owner/repo/pull/42")
	if !errors.Is(err, forge.ErrMarkReadyTransient) {
		t.Fatalf("MarkReady error must be errors.Is forge.ErrMarkReadyTransient; got: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP 502: Bad Gateway") {
		t.Fatalf("MarkReady error must surface gh's stderr; got: %v", err)
	}
	if err := c.MarkDraft("https://github.com/owner/repo/pull/42"); errors.Is(err, forge.ErrMarkReadyTransient) {
		t.Fatalf("MarkDraft must not carry the mark-ready sentinel; got: %v", err)
	}
}

// The classifier is gitplumbing's, shared with Merge: it knows "unexpected EOF",
// which isTransientForgeError does not.
func TestMarkReady_UnexpectedEOFWrapsErrMarkReadyTransient(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "ready" ]; then
  printf 'Post "https://api.github.com/graphql": unexpected EOF\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels)
	err := c.MarkReady("https://github.com/owner/repo/pull/42")
	if !errors.Is(err, forge.ErrMarkReadyTransient) {
		t.Fatalf("MarkReady error must be errors.Is forge.ErrMarkReadyTransient; got: %v", err)
	}
}

func TestMarkReady_PersistentFailureIsNotTransient(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "ready" ]; then
  printf 'HTTP 403: Resource not accessible by integration\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels)
	err := c.MarkReady("https://github.com/owner/repo/pull/42")
	if err == nil || errors.Is(err, forge.ErrMarkReadyTransient) {
		t.Fatalf("a 403 must fail without the transient sentinel; got: %v", err)
	}
}
