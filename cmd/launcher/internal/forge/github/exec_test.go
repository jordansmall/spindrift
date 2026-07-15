package github

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// testLabels is the conventional lifecycle-label set, mirrored from
// lib/env-schema.nix (issue #460); this package's tests share it instead of
// each test restating the four label strings.
var testLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// TestExecClient_ImplementsPRForge verifies the github Code Forge satisfies
// forge.PRForge — it opens PRs and watches CI, unlike the push-only git adapter.
func TestExecClient_ImplementsPRForge(t *testing.T) {
	var _ forge.PRForge = NewExecClient("owner/repo", testLabels, "agent/issue-")
}

// prependFakeGH writes a counting-wrapper gh script to a temp dir, prepends
// that dir to PATH, and returns the dir. Each invocation of the fake gh
// records its argv to call-NN.txt (zero-indexed) inside the dir.
// The caller must use the returned dir to read recorded args.
func prependFakeGH(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
n=$(ls "%s"/call-*.txt 2>/dev/null | wc -l)
printf '%%s\n' "$@" > "%s/call-$(printf '%%02d' $n).txt"
%s`, dir, dir, body)
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", old) })
	os.Setenv("PATH", dir+":"+old)
	return dir
}

// TestExecClient_DepsOf_NativeWins verifies that when the native
// dependencies API returns entries, DepsOf uses them and does not fall
// back to body parsing at all — the fake gh script only handles the
// dependencies call; if DepsOf also called `gh issue view`, that call
// would fail and DepsOf would return an error.
func TestExecClient_DepsOf_NativeWins(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*dependencies/blocked_by*)
	printf '3\n5\n'
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	deps, err := c.DepsOf("10")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	want := []forge.Dependency{{ID: "3", Source: forge.DepSourceNative}, {ID: "5", Source: forge.DepSourceNative}}
	if len(deps) != 2 || deps[0] != want[0] || deps[1] != want[1] {
		t.Fatalf("want %v, got %v", want, deps)
	}
}

// TestExecClient_DepsOf_FallsBackOnEmptyNative verifies that when the
// native dependencies API succeeds but returns no relationships, DepsOf
// falls back to parsing the issue body for blocker refs.
func TestExecClient_DepsOf_FallsBackOnEmptyNative(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*dependencies/blocked_by*)
	printf ''
	;;
*"issue view"*)
	printf '{"number":10,"title":"t","body":"This depends on #7.","state":"OPEN","labels":[]}'
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	deps, err := c.DepsOf("10")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	if len(deps) != 1 || deps[0] != (forge.Dependency{ID: "7", Source: forge.DepSourceBody}) {
		t.Fatalf("want [7 (body)], got %v", deps)
	}
}

// TestExecClient_DepsOf_FallsBackOnNativeError verifies that when the
// native dependencies API call errors (e.g. unsupported GHES, missing
// scope), DepsOf degrades to body parsing rather than failing dispatch.
func TestExecClient_DepsOf_FallsBackOnNativeError(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*dependencies/blocked_by*)
	exit 1
	;;
*"issue view"*)
	printf '{"number":10,"title":"t","body":"blocked by #9","state":"OPEN","labels":[]}'
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	deps, err := c.DepsOf("10")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	if len(deps) != 1 || deps[0] != (forge.Dependency{ID: "9", Source: forge.DepSourceBody}) {
		t.Fatalf("want [9 (body)], got %v", deps)
	}
}

// TestExecClient_DepsOf_NativeErrorSurfacesStderr verifies that when the
// native dependencies API call fails, the fallback warning contains gh's
// actual stderr rather than just "exit status 1".
func TestExecClient_DepsOf_NativeErrorSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*dependencies/blocked_by*)
	printf 'HTTP 404: Not Found\n' >&2
	exit 1
	;;
*"issue view"*)
	printf '{"number":10,"title":"t","body":"blocked by #9","state":"OPEN","labels":[]}'
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	out := captureStderr(t, func() {
		if _, err := c.DepsOf("10"); err != nil {
			t.Fatalf("DepsOf: %v", err)
		}
	})
	if !strings.Contains(out, "HTTP 404: Not Found") {
		t.Fatalf("fallback warning must contain gh's stderr; got: %q", out)
	}
}

// captureStderr runs fn with os.Stderr redirected to a pipe and returns
// everything written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	fn()

	w.Close()
	os.Stderr = orig

	var buf strings.Builder
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err != nil {
			break
		}
	}
	return buf.String()
}

// TestExecClient_DepsOf_WarnsOnStderr verifies that when the native
// dependencies lookup fails, DepsOf's fallback warning goes to stderr, not
// stdout, so it doesn't interfere with programmatic stdout consumers.
func TestExecClient_DepsOf_WarnsOnStderr(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*dependencies/blocked_by*)
	exit 1
	;;
*"issue view"*)
	printf '{"number":10,"title":"t","body":"blocked by #9","state":"OPEN","labels":[]}'
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	out := captureStderr(t, func() {
		if _, err := c.DepsOf("10"); err != nil {
			t.Fatalf("DepsOf: %v", err)
		}
	})
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "10") {
		t.Errorf("DepsOf fallback warning on stderr = %q, want it to mention WARNING and issue 10", out)
	}
}

// TestExecClient_DepsOf_NativeIgnoresBody verifies that when an issue has
// both native dependencies and body-text blocker refs, DepsOf reports the
// native set only — body refs are ignored, not merged.
func TestExecClient_DepsOf_NativeIgnoresBody(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*dependencies/blocked_by*)
	printf '4\n'
	;;
*"issue view"*)
	printf '{"number":10,"title":"t","body":"blocked by #99","state":"OPEN","labels":[]}'
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	deps, err := c.DepsOf("10")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	if len(deps) != 1 || deps[0] != (forge.Dependency{ID: "4", Source: forge.DepSourceNative}) {
		t.Fatalf("want [4 (native)] (native only, body ignored), got %v", deps)
	}
}

// TestExecClient_DepsOf_NativeDeduplicates verifies that when the native
// dependencies API response repeats an issue number, DepsOf collapses the
// duplicate rather than returning it twice.
func TestExecClient_DepsOf_NativeDeduplicates(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*dependencies/blocked_by*)
	printf '3\n5\n3\n'
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	deps, err := c.DepsOf("10")
	if err != nil {
		t.Fatalf("DepsOf: %v", err)
	}
	want := []forge.Dependency{{ID: "3", Source: forge.DepSourceNative}, {ID: "5", Source: forge.DepSourceNative}}
	if len(deps) != 2 || deps[0] != want[0] || deps[1] != want[1] {
		t.Fatalf("want %v, got %v", want, deps)
	}
}

// TestExecClient_TouchesOf_FetchesFullIssueBody verifies that TouchesOf
// fetches the issue's full body via `gh issue view` (unlike ListIssues,
// whose --json number,title summary never includes body) and parses its
// "## Touches" section — the same shared body-grammar default DepsOf's
// body-parsing fallback already relies on.
func TestExecClient_TouchesOf_FetchesFullIssueBody(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*"issue view"*)
	printf '{"number":10,"title":"t","body":"## Touches\\n- lib/env-schema.nix","state":"OPEN","labels":[]}'
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	touches, err := c.TouchesOf("10")
	if err != nil {
		t.Fatalf("TouchesOf: %v", err)
	}
	if len(touches) != 1 || touches[0] != "lib/env-schema.nix" {
		t.Fatalf("want [lib/env-schema.nix], got %v", touches)
	}
}

// TestExecClient_ListOpenIssues_NoLabelFilterIncludesLabels verifies
// ListOpenIssues queries every open issue with no --label filter (unlike
// ListIssues, which scopes to one dispatch state's label) and returns each
// issue's labels, ascending by number.
func TestExecClient_ListOpenIssues_NoLabelFilterIncludesLabels(t *testing.T) {
	dir := prependFakeGH(t, `case "$*" in
*"issue list"*)
	printf '[{"number":3,"title":"third","labels":[{"name":"ready-for-agent"}]},{"number":1,"title":"first","labels":[]}]'
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	issues, err := c.ListOpenIssues()
	if err != nil {
		t.Fatalf("ListOpenIssues: %v", err)
	}
	if len(issues) != 2 || issues[0].Number != "1" || issues[1].Number != "3" {
		t.Fatalf("want ascending [1 3], got %+v", issues)
	}
	if len(issues[1].Labels) != 1 || issues[1].Labels[0] != "ready-for-agent" {
		t.Errorf("issue 3 labels = %v, want [ready-for-agent]", issues[1].Labels)
	}
	if len(issues[0].Labels) != 0 {
		t.Errorf("issue 1 labels = %v, want none", issues[0].Labels)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatalf("read call-00.txt: %v", err)
	}
	argv := string(raw)
	if !strings.Contains(argv, "--state\nopen") {
		t.Errorf("argv = %q, want --state open", argv)
	}
	if strings.Contains(argv, "--label") {
		t.Errorf("argv = %q, must not scope by --label", argv)
	}
}

// TestExecClient_CompleteVerdict_UnconfiguredErrorsWithoutShellingOut
// verifies that CompleteVerdict on a client constructed with no
// VerdictLabels (the work-kind construction path) errors instead of
// shelling out `gh issue edit --add-label ""` — an empty label would
// silently corrupt the issue's label set.
func TestExecClient_CompleteVerdict_UnconfiguredErrorsWithoutShellingOut(t *testing.T) {
	dir := prependFakeGH(t, "")

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	if err := c.CompleteVerdict("10", forge.Recommend); err == nil {
		t.Fatal("want error for unconfigured VerdictLabels, got nil")
	}

	if entries, _ := os.ReadDir(dir); len(entries) > 1 {
		t.Errorf("CompleteVerdict must not shell out to gh when no verdict label is configured; recorded calls: %v", entries)
	}
}

// TestProbe_PositionalSlug verifies that Probe passes the slug as a positional
// argument to `gh repo view` with no --repo/-R flag.
func TestProbe_PositionalSlug(t *testing.T) {
	// Both gh calls exit 0. Probe may error on empty output — that's fine.
	dir := prependFakeGH(t, "")

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	c.Probe() //nolint:errcheck

	// call-01.txt is the `gh repo view …` invocation.
	raw, err := os.ReadFile(filepath.Join(dir, "call-01.txt"))
	if err != nil {
		t.Fatalf("call-01.txt not written: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")

	found := false
	for _, a := range args {
		if a == "owner/repo" {
			found = true
		}
		if a == "--repo" || a == "-R" {
			t.Fatalf("Probe passed %q flag to gh repo view; args: %q", a, args)
		}
	}
	if !found {
		t.Fatalf("slug not found as positional arg in gh repo view; args: %q", args)
	}
}

// TestProbe_StderrSurfaced verifies that when gh repo view fails, the returned
// error contains gh's actual stderr rather than just the configured slug.
func TestProbe_StderrSurfaced(t *testing.T) {
	// Call 0: gh auth status — succeed.
	// Call 1: gh repo view — fail with a distinctive stderr.
	prependFakeGH(t, `if [ "$1" = "repo" ]; then
  printf 'unknown flag: --repo\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	_, err := c.Probe()
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, forge.ErrRepoNotFound) {
		t.Fatalf("want forge.ErrRepoNotFound, got: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("error must contain gh's stderr; got: %v", err)
	}
}

// TestFailureDetail_GraphQLArgShape verifies that FailureDetail queries via
// `gh api graphql` (fine-grained-PAT-safe) rather than `gh pr checks` (REST
// check-runs, 403s under a fine-grained PAT), passing the PR number as a
// GraphQL variable, and renders the failing check's name and summary.
func TestFailureDetail_GraphQLArgShape(t *testing.T) {
	dir := prependFakeGH(t, `if [ "$1" = "api" ]; then
  printf '[{"__typename":"CheckRun","name":"test","conclusion":"FAILURE","summary":"boom"}]\n'
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	detail, err := c.FailureDetail("https://github.com/owner/repo/pull/42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(detail, "test: FAILURE") || !strings.Contains(detail, "boom") {
		t.Fatalf("detail missing failing check content: %q", detail)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatalf("call-00.txt not written: %v", err)
	}
	args := strings.Split(strings.TrimSpace(string(raw)), "\n")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "graphql") {
		t.Fatalf("FailureDetail must use gh api graphql, not REST; args: %q", args)
	}
	if strings.Contains(joined, "checks") {
		t.Fatalf("FailureDetail must not use `gh pr checks`; args: %q", args)
	}
	found42 := false
	for _, a := range args {
		if a == "number=42" {
			found42 = true
		}
	}
	if !found42 {
		t.Fatalf("PR number not passed as a GraphQL variable; args: %q", args)
	}
}

// TestNeedsUpdate_BehindByPositiveReturnsTrue verifies NeedsUpdate compares
// the PR's branch against its base via the compare API (`behind_by`) — a
// pure git-ancestry fact, unlike GraphQL's mergeStateStatus BEHIND, which
// GitHub only reports when branch protection requires branches to be up to
// date before merging (a setting this project's fine-grained PAT cannot
// even read, let alone guarantee is enabled — issue #936).
func TestNeedsUpdate_BehindByPositiveReturnsTrue(t *testing.T) {
	dir := prependFakeGH(t, `if [ "$1" = "pr" ]; then
  printf 'agent/issue-42\tmain\n'
elif [ "$1" = "api" ]; then
  printf '3\n'
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	stale, err := c.NeedsUpdate("https://github.com/owner/repo/pull/42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !stale {
		t.Fatal("NeedsUpdate: want true when behind_by > 0, got false")
	}

	viewArgs := readCallArgs(t, dir, 0)
	if !strings.Contains(viewArgs, "headRefName") || !strings.Contains(viewArgs, "baseRefName") {
		t.Fatalf("first call must read headRefName/baseRefName; args: %q", viewArgs)
	}

	cmpArgs := readCallArgs(t, dir, 1)
	if !strings.Contains(cmpArgs, "compare/main...agent%2Fissue-42") {
		t.Fatalf("compare call must diff base...head (base branch first, PR branch's slash escaped); args: %q", cmpArgs)
	}
	if !strings.Contains(cmpArgs, "behind_by") {
		t.Fatalf("compare call must read behind_by; args: %q", cmpArgs)
	}
}

// TestNeedsUpdate_BehindByZeroReturnsFalse verifies NeedsUpdate reports
// false when the PR branch already contains its base's current tip
// (behind_by == 0).
func TestNeedsUpdate_BehindByZeroReturnsFalse(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ]; then
  printf 'feature\tmain\n'
elif [ "$1" = "api" ]; then
  printf '0\n'
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	stale, err := c.NeedsUpdate("https://github.com/owner/repo/pull/42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stale {
		t.Fatal("NeedsUpdate: want false when behind_by == 0, got true")
	}
}

// readCallArgs reads the n-th recorded fake-gh invocation's args as a single
// space-joined string.
func readCallArgs(t *testing.T, dir string, n int) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("call-%02d.txt", n)))
	if err != nil {
		t.Fatalf("call-%02d.txt not written: %v", n, err)
	}
	return strings.Join(strings.Split(strings.TrimSpace(string(raw)), "\n"), " ")
}

// TestRenderFailureDetail verifies the failing-context filter and the
// maxFailureDetailBytes truncation.
func TestRenderFailureDetail(t *testing.T) {
	t.Run("filters out passing and non-failing conclusions", func(t *testing.T) {
		contexts := []failureDetailContext{
			{TypeName: "CheckRun", Name: "unit-tests", Conclusion: "SUCCESS", Summary: "all good"},
			{TypeName: "CheckRun", Name: "lint", Conclusion: "FAILURE", Summary: "2 errors"},
			{TypeName: "StatusContext", Context: "legacy-ci", State: "SUCCESS"},
			{TypeName: "StatusContext", Context: "legacy-status", State: "ERROR", Description: "build broke"},
		}
		got := renderFailureDetail(contexts)
		if strings.Contains(got, "unit-tests") || strings.Contains(got, "legacy-ci") {
			t.Fatalf("passing contexts must be filtered out: %q", got)
		}
		if !strings.Contains(got, "lint: FAILURE") || !strings.Contains(got, "2 errors") {
			t.Fatalf("failing CheckRun missing: %q", got)
		}
		if !strings.Contains(got, "legacy-status: ERROR") || !strings.Contains(got, "build broke") {
			t.Fatalf("failing StatusContext missing: %q", got)
		}
	})

	t.Run("no failing contexts returns empty string", func(t *testing.T) {
		contexts := []failureDetailContext{
			{TypeName: "CheckRun", Name: "unit-tests", Conclusion: "SUCCESS"},
		}
		if got := renderFailureDetail(contexts); got != "" {
			t.Fatalf("want empty string, got %q", got)
		}
	})

	t.Run("truncates to maxFailureDetailBytes", func(t *testing.T) {
		contexts := []failureDetailContext{
			{TypeName: "CheckRun", Name: "huge", Conclusion: "FAILURE", Summary: strings.Repeat("x", maxFailureDetailBytes*2)},
		}
		got := renderFailureDetail(contexts)
		if len(got) > maxFailureDetailBytes {
			t.Fatalf("detail not bounded: got %d bytes, want <= %d", len(got), maxFailureDetailBytes)
		}
	})
}

// TestMerge_BlockedByChecksNotClassifiedAsConflict verifies that when gh pr
// merge refuses with "not mergeable" wording but the PR's queried mergeable
// state is MERGEABLE (not CONFLICTING), Merge returns forge.ErrMergeBlockedByChecks
// rather than forge.ErrMergeConflict — the two refusals share the same stderr
// wording, so substring-matching alone cannot tell them apart (issue #566).
func TestMerge_BlockedByChecksNotClassifiedAsConflict(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "merge" ]; then
  printf 'GraphQL: Pull Request is not mergeable (mergePullRequest)\n' >&2
  exit 1
fi
if [ "$1" = "api" ]; then
  printf 'MERGEABLE\n'
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	err := c.Merge("https://github.com/owner/repo/pull/42")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("blocked-by-checks refusal must not classify as forge.ErrMergeConflict, got: %v", err)
	}
	if !errors.Is(err, forge.ErrMergeBlockedByChecks) {
		t.Fatalf("want forge.ErrMergeBlockedByChecks, got: %v", err)
	}
}

// TestMerge_GenuineConflictStillClassifiedAsConflict verifies that a "not
// mergeable" refusal on a PR whose queried mergeable state is CONFLICTING
// still returns forge.ErrMergeConflict, so the rebase-retry path keeps engaging
// for real conflicts.
func TestMerge_GenuineConflictStillClassifiedAsConflict(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "merge" ]; then
  printf 'GraphQL: Pull Request is not mergeable (mergePullRequest)\n' >&2
  exit 1
fi
if [ "$1" = "api" ]; then
  printf 'CONFLICTING\n'
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	err := c.Merge("https://github.com/owner/repo/pull/42")
	if !errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("want forge.ErrMergeConflict, got: %v", err)
	}
}

// TestMerge_UndeterminedMergeableStateIsItsOwnError verifies that a "not
// mergeable" refusal whose queried mergeable state is neither CONFLICTING nor
// MERGEABLE (e.g. UNKNOWN — GitHub hasn't finished computing it) is surfaced
// as its own error rather than silently folded into forge.ErrMergeConflict or
// forge.ErrMergeBlockedByChecks.
func TestMerge_UndeterminedMergeableStateIsItsOwnError(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "merge" ]; then
  printf 'GraphQL: Pull Request is not mergeable (mergePullRequest)\n' >&2
  exit 1
fi
if [ "$1" = "api" ]; then
  printf 'UNKNOWN\n'
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	err := c.Merge("https://github.com/owner/repo/pull/42")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if errors.Is(err, forge.ErrMergeConflict) {
		t.Fatalf("undetermined mergeable state must not classify as forge.ErrMergeConflict, got: %v", err)
	}
	if errors.Is(err, forge.ErrMergeBlockedByChecks) {
		t.Fatalf("undetermined mergeable state must not classify as forge.ErrMergeBlockedByChecks, got: %v", err)
	}
}
