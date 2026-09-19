package github

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// The error must carry gh's stderr, not just "exit status 1", so that
// isTransientForgeError has a real marker to pattern-match against (issue
// #2323).
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

// An empty stderr must not leave a dangling "exit status 1: " colon-space.
// ghCommandErr relies on cmd.Output populating *exec.ExitError.Stderr, and it
// has to degrade as cleanly as the old hand-wired buffer did (issue #2864).
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

// The error must carry gh's stderr text, not just the bare exit status
// (issue #2864).
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

// The error must carry gh's stderr text (issue #2864).
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

// The error must carry gh's stderr text. This covers the GraphQL-shaped call
// sites alongside the REST- and `gh pr`-shaped ones above (issue #2864).
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

// A non-conflict, non-transient stderr must still reach the returned error
// through ghCommandErrText (issue #2864).
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

// The error must stay errors.Is-detectable as forge.ErrAuthFailure and also
// carry gh's stderr: before ghCommandErr, this path never captured `gh auth
// status`'s stderr at all (issue #2864).
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
	if errors.Is(err, forge.ErrRateLimit) {
		t.Fatalf("non-rate-limited auth failure must not classify as forge.ErrRateLimit, got: %v", err)
	}
	if !strings.Contains(err.Error(), "You are not logged into any GitHub hosts") {
		t.Fatalf("Probe error must surface gh's stderr; got: %v", err)
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

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
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

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
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

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	err := c.CreateLabel("bug", "a bug", "ff0000")
	if err == nil {
		t.Fatal("CreateLabel: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 422: label already exists") {
		t.Fatalf("CreateLabel error must surface gh's stderr; got: %v", err)
	}
}

// The error must carry gh's stderr text (issue #2864).
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

// The error must carry gh's stderr text. The old path called cmd.Run with no
// Stderr wired, so a clone failure degraded to a bare "exit status 1" (issue
// #2864). The fake gh must answer `pr view` first, because Rebase reads the
// branch names before it clones.
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

// OpenPRForBranch used to make a second `gh pr view --json isDraft` call
// solely to populate the now-removed draft field. That call is gone, so one
// `gh pr list` must be enough to report the PR as found.
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

// Rate-limit-shaped stderr from `gh repo view` must reach Probe's caller as
// forge.ErrRateLimit, proving the central ghCommandErrText classification
// (issue #2865) reaches a real adapter method, not just the exec.go helper.
func TestProbe_RateLimitedSurfacesErrRateLimit(t *testing.T) {
	// The fake only branches on `repo`, so `gh auth status` succeeds first and
	// Probe gets as far as the rate-limited call.
	prependFakeGH(t, `if [ "$1" = "repo" ]; then
  printf 'API rate limit exceeded for installation ID 12345678.\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	_, err := c.Probe()
	if err == nil {
		t.Fatal("Probe: want error, got nil")
	}
	if !errors.Is(err, forge.ErrRateLimit) {
		t.Fatalf("Probe error must be errors.Is forge.ErrRateLimit; got: %v", err)
	}
	if errors.Is(err, forge.ErrRepoNotFound) {
		t.Fatalf("Probe error must not also be errors.Is forge.ErrRepoNotFound; got: %v", err)
	}
}

// A throttled `gh auth status` must classify as forge.ErrRateLimit and not
// forge.ErrAuthFailure: the two sentinels stay mutually exclusive so a caller
// that checks ErrAuthFailure first (doctor, for one) does not misreport the
// cause. Issue #2865 round 1 fixed only the `gh repo view` branch and left
// the same bug here.
func TestProbe_AuthRateLimitedSurfacesErrRateLimit(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "auth" ]; then
  printf 'error validating token: HTTP 403: API rate limit exceeded for user ID 1.\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	_, err := c.Probe()
	if err == nil {
		t.Fatal("Probe: want error, got nil")
	}
	if !errors.Is(err, forge.ErrRateLimit) {
		t.Fatalf("Probe error must be errors.Is forge.ErrRateLimit; got: %v", err)
	}
	if errors.Is(err, forge.ErrAuthFailure) {
		t.Fatalf("Probe error must not also be errors.Is forge.ErrAuthFailure; got: %v", err)
	}
}
