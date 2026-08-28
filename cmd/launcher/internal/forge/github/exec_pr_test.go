package github

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestOpenPRForBranch_PRListFailureSurfacesStderr verifies that when `gh pr
// list` fails, the error OpenPRForBranch returns includes gh's actual
// stderr — not just "exit status 1" — so isTransientForgeError has a real
// marker to pattern-match against (issue #2323).
func TestOpenPRForBranch_PRListFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  printf 'HTTP 502: Bad Gateway\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, _, err := c.OpenPRForBranch("agent/issue-1")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 502: Bad Gateway") {
		t.Fatalf("error must surface gh's stderr; got: %v", err)
	}
}

// TestOpenPRForBranch_ErrorEmptyStderrNoTrailingColon verifies that when `gh
// pr list` exits non-zero without writing to stderr, the returned error has
// no dangling "exit status 1: " trailing colon-space. OpenPRForBranch used to
// wire its own bytes.Buffer to cmd.Stderr and unconditionally append it;
// switching to ghCommandErr (which relies on cmd.Output's automatic
// *exec.ExitError.Stderr population) must preserve the same clean-degrade
// behavior on an empty stderr (issue #2864).
func TestOpenPRForBranch_ErrorEmptyStderrNoTrailingColon(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, _, err := c.OpenPRForBranch("agent/issue-1")
	if err == nil {
		t.Fatal("OpenPRForBranch: want error, got nil")
	}
	if strings.HasSuffix(err.Error(), ": ") {
		t.Fatalf("OpenPRForBranch error must not have a trailing colon-space; got: %q", err.Error())
	}
}

// TestBranchExists_APIFailureSurfacesStderr verifies that when `gh api
// matching-refs` fails, the error BranchExists returns includes gh's actual
// stderr text, not just the bare exit status (issue #2864).
func TestBranchExists_APIFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "api" ]; then
  printf 'HTTP 404: Not Found\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, err := c.BranchExists("agent/issue-1")
	if err == nil {
		t.Fatal("BranchExists: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 404: Not Found") {
		t.Fatalf("BranchExists error must surface gh's stderr; got: %v", err)
	}
}

// TestPRForBranch_PRListFailureSurfacesStderr verifies that when `gh pr
// list` fails, the error PRForBranch returns includes gh's actual stderr
// text (issue #2864).
func TestPRForBranch_PRListFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  printf 'HTTP 502: Bad Gateway\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, _, err := c.PRForBranch("agent/issue-1")
	if err == nil {
		t.Fatal("PRForBranch: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 502: Bad Gateway") {
		t.Fatalf("PRForBranch error must surface gh's stderr; got: %v", err)
	}
}

// TestCheckState_GraphQLFailureSurfacesStderr verifies that when `gh api
// graphql` fails, the error CheckState returns includes gh's actual stderr
// text — covers the GraphQL-shaped call sites alongside the REST- and `gh
// pr`-shaped ones above (issue #2864).
func TestCheckState_GraphQLFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "api" ] && [ "$2" = "graphql" ]; then
  printf 'HTTP 403: Forbidden\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, err := c.CheckState("https://github.com/owner/repo/pull/42")
	if err == nil {
		t.Fatal("CheckState: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 403: Forbidden") {
		t.Fatalf("CheckState error must surface gh's stderr; got: %v", err)
	}
}

// TestMerge_TransientFailureSurfacesStderr verifies that when `gh pr merge`
// fails with a transient-looking stderr (not a genuine conflict), the
// returned error is still errors.Is-detectable as forge.ErrMergeTransient
// and includes gh's stderr text, now routed through ghCommandErrText rather
// than a hand-rolled format string (issue #2864).
func TestMerge_TransientFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "merge" ]; then
  printf 'HTTP 503: Service Unavailable\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
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

// TestMerge_GenericFailureSurfacesStderr verifies that when `gh pr merge`
// fails with a non-conflict, non-transient stderr, the returned error still
// includes gh's actual stderr text, routed through ghCommandErrText (issue
// #2864).
func TestMerge_GenericFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "merge" ]; then
  printf 'HTTP 403: permission denied\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
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

// TestProbe_AuthFailureSurfacesStderr verifies that when `gh auth status`
// fails, the error Probe returns is still errors.Is-detectable as
// forge.ErrAuthFailure and now also includes gh's actual stderr text — a
// real diagnostic gap before ghCommandErr's adoption here, since the old
// code path never captured `gh auth status`'s stderr at all (issue #2864).
func TestProbe_AuthFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  printf 'You are not logged into any GitHub hosts\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	_, err := c.Probe()
	if err == nil {
		t.Fatal("Probe: want error, got nil")
	}
	if !errors.Is(err, forge.ErrAuthFailure) {
		t.Fatalf("Probe error must be errors.Is forge.ErrAuthFailure; got: %v", err)
	}
	if !strings.Contains(err.Error(), "You are not logged into any GitHub hosts") {
		t.Fatalf("Probe error must surface gh's stderr; got: %v", err)
	}
}

// TestEnqueueAutoMerge_FailureSurfacesStderr verifies that when `gh pr merge
// --auto` fails, the returned error includes gh's actual stderr text — the
// old code path never captured stderr at all (cmd.Run with no Stderr wired),
// so it degraded to the bare "exit status 1" (issue #2864).
func TestEnqueueAutoMerge_FailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "merge" ]; then
  printf 'HTTP 422: Unprocessable Entity\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	err := c.EnqueueAutoMerge("https://github.com/owner/repo/pull/42")
	if err == nil {
		t.Fatal("EnqueueAutoMerge: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 422: Unprocessable Entity") {
		t.Fatalf("EnqueueAutoMerge error must surface gh's stderr; got: %v", err)
	}
}

// TestMarkReady_FailureSurfacesStderr verifies that when `gh pr ready` fails,
// the error runGHReadyToggle produces (via MarkReady) includes gh's actual
// stderr text, now routed through ghCommandErr instead of a hand-rolled
// suffix (issue #2864).
func TestMarkReady_FailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "ready" ]; then
  printf 'HTTP 404: Not Found\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	err := c.MarkReady("https://github.com/owner/repo/pull/42")
	if err == nil {
		t.Fatal("MarkReady: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 404: Not Found") {
		t.Fatalf("MarkReady error must surface gh's stderr; got: %v", err)
	}
}

// TestCreateLabel_FailureSurfacesStderr verifies that when `gh label create`
// fails, the returned error includes gh's actual stderr text — CreateLabel
// used to merge stdout+stderr via CombinedOutput; it now leaves Stderr nil
// for cmd.Output to auto-populate, same as every other ghCommandErr site
// (issue #2864).
func TestCreateLabel_FailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "label" ] && [ "$2" = "create" ]; then
  printf 'HTTP 422: label already exists\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	err := c.CreateLabel("bug", "a bug", "ff0000")
	if err == nil {
		t.Fatal("CreateLabel: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 422: label already exists") {
		t.Fatalf("CreateLabel error must surface gh's stderr; got: %v", err)
	}
}

// TestListLabels_FailureSurfacesStderr verifies that when `gh label list`
// fails, the returned error includes gh's actual stderr text (issue #2864).
func TestListLabels_FailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "label" ] && [ "$2" = "list" ]; then
  printf 'HTTP 502: Bad Gateway\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, err := c.ListLabels()
	if err == nil {
		t.Fatal("ListLabels: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 502: Bad Gateway") {
		t.Fatalf("ListLabels error must surface gh's stderr; got: %v", err)
	}
}

// TestRebase_CloneFailureSurfacesStderr verifies that when `gh repo clone`
// fails, the returned error includes gh's actual stderr text — the old code
// path never captured stderr at all (cmd.Run with no Stderr wired), so it
// degraded to the bare "exit status 1" (issue #2864).
func TestRebase_CloneFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  printf 'feature\tmain\n'
  exit 0
fi
if [ "$1" = "repo" ] && [ "$2" = "clone" ]; then
  printf 'HTTP 500: Internal Server Error\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	err := c.Rebase("https://github.com/owner/repo/pull/42")
	if err == nil {
		t.Fatal("Rebase: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 500: Internal Server Error") {
		t.Fatalf("Rebase error must surface gh's stderr; got: %v", err)
	}
}

// TestOpenPRForBranch_SingleGHCall verifies OpenPRForBranch resolves a found
// PR with exactly one `gh` invocation. It used to make a second `gh pr view
// --json isDraft` call solely to populate the (now-removed) draft field;
// that call is gone, so a single `gh pr list` must be enough to report the
// PR as found.
func TestOpenPRForBranch_SingleGHCall(t *testing.T) {
	dir := prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "list" ]; then
  echo "https://github.com/owner/repo/pull/42"
  exit 0
fi
exit 1
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	pr, ok, err := c.OpenPRForBranch("agent/issue-1")
	if err != nil {
		t.Fatalf("OpenPRForBranch: %v", err)
	}
	if !ok {
		t.Fatal("OpenPRForBranch: want found, got not found")
	}
	if pr.URL != "https://github.com/owner/repo/pull/42" {
		t.Fatalf("OpenPRForBranch URL = %q, want the listed PR URL", pr.URL)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "call-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("OpenPRForBranch made %d gh calls, want exactly 1", len(matches))
	}
}
