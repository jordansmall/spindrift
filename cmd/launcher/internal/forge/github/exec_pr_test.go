package github

import (
	"errors"
	"os"
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

// gh label list caps at --limit, so a repo with more labels was silently
// truncated (issue #4607). The fake serves 150 labels only to a paginated
// REST call, 100 otherwise, and rejects any --limit cap.
func TestListLabels_PagesPastOneHundred(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "api" ] && [ "$2" = "repos/owner/repo/labels?per_page=100" ]; then
  n=100
  for a in "$@"; do
    [ "$a" = "--paginate" ] && n=150
    [ "$a" = "--limit" ] && { printf 'unexpected --limit\n' >&2; exit 1; }
  done
  i=1
  while [ "$i" -le "$n" ]; do printf 'label-%d\n' "$i"; i=$((i+1)); done
  exit 0
fi
printf 'unexpected argv: %s\n' "$*" >&2
exit 1
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	got, err := c.ListLabels()
	if err != nil {
		t.Fatalf("ListLabels: %v", err)
	}
	if len(got) != 150 {
		t.Fatalf("ListLabels returned %d labels, want 150", len(got))
	}
	if got[0] != "label-1" || got[149] != "label-150" {
		t.Fatalf("ListLabels order/content wrong: first %q last %q", got[0], got[149])
	}
}

// The error must carry gh's stderr text (issue #2864).
func TestListLabels_FailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "api" ]; then
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

const checkRunPRURL = "https://github.com/owner/repo/pull/42"

func graphQLRollupGH(rollupJSON string) string {
	return `if [ "$1" = "api" ] && [ "$2" = "graphql" ]; then
  printf '%s\n' '` + rollupJSON + `'
  exit 0
fi
`
}

// Every job of one Actions run must map to the same run URL, so the URL stays
// stable across polls as jobs register (issue #4962).
func TestCheckRun_ReturnsActionsRunURLWithJobSuffixTrimmed(t *testing.T) {
	prependFakeGH(t, graphQLRollupGH(`{"state":"PENDING","contexts":{"nodes":[
{"__typename":"StatusContext"},
{"__typename":"CheckRun","detailsUrl":"https://github.com/owner/repo/actions/runs/123/job/456"},
{"__typename":"CheckRun","detailsUrl":"https://github.com/owner/repo/actions/runs/123/job/789"}]}}`))

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	state, runURL, err := c.CheckRun(checkRunPRURL)
	if err != nil {
		t.Fatalf("CheckRun: %v", err)
	}
	if state != forge.StatePending {
		t.Errorf("state = %q, want %q", state, forge.StatePending)
	}
	if want := "https://github.com/owner/repo/actions/runs/123"; runURL != want {
		t.Errorf("runURL = %q, want %q", runURL, want)
	}
}

func TestCheckRun_NoChecksReturnsNoneAndNoURL(t *testing.T) {
	prependFakeGH(t, graphQLRollupGH(`{}`))

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	state, runURL, err := c.CheckRun(checkRunPRURL)
	if err != nil {
		t.Fatalf("CheckRun: %v", err)
	}
	if state != forge.StateNone || runURL != "" {
		t.Errorf("CheckRun = (%q, %q), want (%q, \"\")", state, runURL, forge.StateNone)
	}
}

func TestCheckRun_NonActionsDetailsURLYieldsNoURL(t *testing.T) {
	prependFakeGH(t, graphQLRollupGH(`{"state":"SUCCESS","contexts":{"nodes":[
{"__typename":"CheckRun","detailsUrl":"https://ci.example.com/builds/9"}]}}`))

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	state, runURL, err := c.CheckRun(checkRunPRURL)
	if err != nil {
		t.Fatalf("CheckRun: %v", err)
	}
	if state != forge.StateSuccess || runURL != "" {
		t.Errorf("CheckRun = (%q, %q), want (%q, \"\")", state, runURL, forge.StateSuccess)
	}
}

func TestCheckRun_InvalidPRURL(t *testing.T) {
	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	if _, _, err := c.CheckRun("not-a-pr-url"); err == nil {
		t.Fatal("CheckRun: want invalid PR URL error, got nil")
	}
}

func TestCheckRun_GraphQLFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "api" ] && [ "$2" = "graphql" ]; then
  printf 'HTTP 403: Forbidden\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, _, err := c.CheckRun(checkRunPRURL)
	if err == nil || !strings.Contains(err.Error(), "HTTP 403: Forbidden") {
		t.Fatalf("CheckRun error must surface gh's stderr; got: %v", err)
	}
}

// With several workflows on one commit the link must follow the run still in
// flight, not whichever node GraphQL happens to list first.
func TestCheckRun_PrefersRunNotYetCompleted(t *testing.T) {
	prependFakeGH(t, graphQLRollupGH(`{"state":"PENDING","contexts":{"nodes":[
{"__typename":"CheckRun","status":"COMPLETED","detailsUrl":"https://github.com/owner/repo/actions/runs/1/job/10"},
{"__typename":"CheckRun","status":"IN_PROGRESS","detailsUrl":"https://github.com/owner/repo/actions/runs/2/job/20"}]}}`))

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, runURL, err := c.CheckRun(checkRunPRURL)
	if err != nil {
		t.Fatalf("CheckRun: %v", err)
	}
	if want := "https://github.com/owner/repo/actions/runs/2"; runURL != want {
		t.Errorf("runURL = %q, want %q", runURL, want)
	}
}

func TestCheckRun_FallsBackToFirstRunWhenAllCompleted(t *testing.T) {
	prependFakeGH(t, graphQLRollupGH(`{"state":"SUCCESS","contexts":{"nodes":[
{"__typename":"CheckRun","status":"COMPLETED","detailsUrl":"https://github.com/owner/repo/actions/runs/1/job/10"},
{"__typename":"CheckRun","status":"COMPLETED","detailsUrl":"https://github.com/owner/repo/actions/runs/2/job/20"}]}}`))

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, runURL, err := c.CheckRun(checkRunPRURL)
	if err != nil {
		t.Fatalf("CheckRun: %v", err)
	}
	if want := "https://github.com/owner/repo/actions/runs/1"; runURL != want {
		t.Errorf("runURL = %q, want %q", runURL, want)
	}
}

// A run URL on another host or repo must never reach the dashboard link.
func TestCheckRun_IgnoresRunsOutsideThePRsRepo(t *testing.T) {
	prependFakeGH(t, graphQLRollupGH(`{"state":"PENDING","contexts":{"nodes":[
{"__typename":"CheckRun","status":"IN_PROGRESS","detailsUrl":"https://evil.example.com/owner/repo/actions/runs/1"},
{"__typename":"CheckRun","status":"IN_PROGRESS","detailsUrl":"https://github.com/other/repo/actions/runs/2"},
{"__typename":"CheckRun","status":"IN_PROGRESS","detailsUrl":"https://github.com/owner/other/actions/runs/3"}]}}`))

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, runURL, err := c.CheckRun(checkRunPRURL)
	if err != nil {
		t.Fatalf("CheckRun: %v", err)
	}
	if runURL != "" {
		t.Errorf("runURL = %q, want \"\"", runURL)
	}
}

// Driven through the shared stateful gh fake rather than an inline stub, so the
// query text the adapter sends is what selects the fake's response.
func TestExecClient_CheckRun_ViaPRForgeFake(t *testing.T) {
	h := newPRForgeHarness(t)
	url := h.SeedOpenPR("7")
	h.SeedCheckStates(url, []forge.RollupState{forge.StatePending})
	h.SeedRunChecks(url,
		"COMPLETED https://github.com/owner/repo/actions/runs/5/job/1",
		"QUEUED https://github.com/owner/repo/actions/runs/6/job/2")

	state, runURL, err := h.cf.(forge.CIRunReporter).CheckRun(url)
	if err != nil {
		t.Fatalf("CheckRun: %v", err)
	}
	if state != forge.StatePending {
		t.Errorf("state = %q, want %q", state, forge.StatePending)
	}
	if want := "https://github.com/owner/repo/actions/runs/6"; runURL != want {
		t.Errorf("runURL = %q, want %q", runURL, want)
	}
}

func TestMergeCommit_ReadsOidViaGhPrView(t *testing.T) {
	dir := prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  printf 'abc123\n'
  exit 0
fi
exit 1
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	got, err := c.MergeCommit("https://github.com/owner/repo/pull/7")
	if err != nil {
		t.Fatalf("MergeCommit: %v", err)
	}
	if got != "abc123" {
		t.Fatalf("MergeCommit = %q, want abc123", got)
	}
	args, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "mergeCommit") {
		t.Fatalf("gh args = %q, want a mergeCommit query", args)
	}
}

func TestMergeCommit_FailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `printf 'HTTP 502: Bad Gateway\n' >&2
exit 1
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, err := c.MergeCommit("https://github.com/owner/repo/pull/7")
	if err == nil || !strings.Contains(err.Error(), "HTTP 502: Bad Gateway") {
		t.Fatalf("MergeCommit error = %v, want gh's stderr", err)
	}
}
