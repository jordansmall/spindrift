package github

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/testutil"
)

// testLabels mirrors the lifecycle labels in lib/env-schema.nix (issue #460),
// so no test has to restate the four label strings.
var testLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
}

// The github forge opens PRs and watches CI, unlike the push-only git adapter.
func TestExecClient_ImplementsPRForge(t *testing.T) {
	var _ forge.PRForge = NewExecClient("owner/repo", testLabels, "agent/issue-")
}

// GitHub issues close through the forge's own auto-close mechanism (ADR 0029),
// so there is no landing ref to persist. Only the local adapter implements
// forge.LandingRecorder.
func TestExecClient_DoesNotImplementLandingRecorder(t *testing.T) {
	var it forge.IssueTracker = NewExecClient("owner/repo", testLabels, "agent/issue-")
	if _, ok := it.(forge.LandingRecorder); ok {
		t.Error("ExecClient satisfies forge.LandingRecorder, want it hidden")
	}
}

// PickIssue's double-box guard (#1742) relies on forge.LabeledTracker to skip a
// ListIssues round-trip for a state the tracker's label family leaves unmapped.
func TestExecClient_ImplementsLabeledTracker(t *testing.T) {
	var _ forge.LabeledTracker = NewExecClient("owner/repo", testLabels, "agent/issue-")
}

// prependFakeGH puts a counting-wrapper gh script on PATH and returns its dir.
// Each invocation records its argv to call-NN.txt (zero-indexed) in that dir.
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

// The fake gh script handles only the dependencies call, so a body-parsing
// fallback would exit 1 and DepsOf would return an error.
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

// A native lookup failure (unsupported GHES, a missing token scope) must
// degrade to body parsing rather than fail the dispatch.
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

// The fallback warning must carry gh's own stderr, not just "exit status 1".
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
	out := testutil.CaptureStderr(t, func() {
		if _, err := c.DepsOf("10"); err != nil {
			t.Fatalf("DepsOf: %v", err)
		}
	})
	if !strings.Contains(out, "HTTP 404: Not Found") {
		t.Fatalf("fallback warning must contain gh's stderr; got: %q", out)
	}
}

func TestExecClient_DepsOf_NativeErrorEmptyStderrNoTrailingColon(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*dependencies/blocked_by*)
	exit 1
	;;
*"issue view"*)
	printf '{"number":10,"title":"t","body":"blocked by #9","state":"OPEN","labels":[]}'
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	_, err := c.nativeDepsOf("10")
	if err == nil {
		t.Fatal("nativeDepsOf: want error, got nil")
	}
	if strings.HasSuffix(err.Error(), ": ") {
		t.Fatalf("nativeDepsOf error must not have a trailing colon-space; got: %q", err.Error())
	}
}

// The fallback warning goes to stderr so it cannot corrupt what programmatic
// consumers read from stdout.
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
	out := testutil.CaptureStderr(t, func() {
		if _, err := c.DepsOf("10"); err != nil {
			t.Fatalf("DepsOf: %v", err)
		}
	})
	if !strings.Contains(out, "WARNING") || !strings.Contains(out, "10") {
		t.Errorf("DepsOf fallback warning on stderr = %q, want it to mention WARNING and issue 10", out)
	}
}

// The fixture carries both a native dependency and a body-text blocker ref:
// DepsOf reports the native set only, ignoring body refs rather than merging.
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
	want := []forge.Dependency{{ID: "4", Source: forge.DepSourceNative}}
	if !reflect.DeepEqual(deps, want) {
		t.Fatalf("DepsOf = %v, want %v", deps, want)
	}
}

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
	if !reflect.DeepEqual(deps, want) {
		t.Fatalf("DepsOf = %v, want %v", deps, want)
	}
}

// GitHub's issue-dependencies API tracks blocked/blocking as a bidirectional
// relationship, so the reverse direction is one more native call rather than a
// whole-backlog scan (issue #1744).
func TestExecClient_ImplementsBlockersLister(t *testing.T) {
	var _ forge.BlockersLister = NewExecClient("owner/repo", testLabels, "agent/issue-")
}

// Every result is DepSourceNative because there is no body-text fallback here:
// no prose grammar declares a forward "blocks" relationship (issue #1744).
func TestExecClient_BlocksOf_ReturnsNativeBlocking(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*dependencies/blocking*)
	printf '42\n43\n'
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	blocks, err := c.BlocksOf("7")
	if err != nil {
		t.Fatalf("BlocksOf: %v", err)
	}
	want := []forge.Dependency{{ID: "42", Source: forge.DepSourceNative}, {ID: "43", Source: forge.DepSourceNative}}
	if !reflect.DeepEqual(blocks, want) {
		t.Fatalf("BlocksOf = %v, want %v", blocks, want)
	}
}

// BlocksOf has no fallback to degrade to, so a native lookup failure must
// reach the caller.
func TestExecClient_BlocksOf_PropagatesNativeError(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*dependencies/blocking*)
	printf 'HTTP 404: Not Found\n' >&2
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	_, err := c.BlocksOf("7")
	if err == nil {
		t.Fatal("BlocksOf: want error, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("BlocksOf error = %q, want it to mention the gh api failure", err.Error())
	}
}

// A terminal recover failure must not downgrade an already-successful issue.
// forge.PriorClaimStateReader is how recover learns the issue's terminal state
// immediately before its most recent claim (issue #2477).
func TestExecClient_ImplementsPriorClaimStateReader(t *testing.T) {
	var _ forge.PriorClaimStateReader = NewExecClient("owner/repo", testLabels, "agent/issue-")
}

// The fake gh script stands in for the already-filtered output of
// `gh api .../timeline --jq '... | .label.name'`, one label name per line.
func TestExecClient_PriorClaimState_FindsComplete(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*timeline*)
	printf 'ready-for-agent\nagent-complete\n'
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	state, ok, err := c.PriorClaimState("10")
	if err != nil {
		t.Fatalf("PriorClaimState: %v", err)
	}
	if !ok {
		t.Fatal("PriorClaimState: ok = false, want true")
	}
	if state != forge.Complete {
		t.Fatalf("PriorClaimState state = %v, want forge.Complete", state)
	}
}

// A Failed run later recovered into a Complete one leaves both terminal labels
// in the timeline. The winner is the last matching line in the chronological
// (oldest-first) stream, not the first match found.
func TestExecClient_PriorClaimState_MostRecentWins(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*timeline*)
	printf 'agent-failed\nagent-complete\n'
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	state, ok, err := c.PriorClaimState("10")
	if err != nil {
		t.Fatalf("PriorClaimState: %v", err)
	}
	if !ok {
		t.Fatal("PriorClaimState: ok = false, want true")
	}
	if state != forge.Complete {
		t.Fatalf("PriorClaimState state = %v, want forge.Complete (the most recent unlabeled event)", state)
	}
}

// A first-ever dispatch has no prior claim to recall.
func TestExecClient_PriorClaimState_NoTerminalLabelReturnsNotFound(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*timeline*)
	printf 'ready-for-agent\n'
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, ok, err := c.PriorClaimState("10")
	if err != nil {
		t.Fatalf("PriorClaimState: %v", err)
	}
	if ok {
		t.Fatal("PriorClaimState: ok = true, want false")
	}
}

// A genuine gh api failure must not be reported as not-found.
func TestExecClient_PriorClaimState_GenuineFailureSurfaced(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*timeline*)
	printf 'HTTP 404: Not Found\n' >&2
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, _, err := c.PriorClaimState("10")
	if err == nil {
		t.Fatal("PriorClaimState: want error, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("PriorClaimState error = %q, want it to mention the gh api failure", err.Error())
	}
}

// Without --paginate, a long label history spanning several result pages would
// be scanned only as far as its first page.
func TestExecClient_PriorClaimState_UsesTimelineEndpointPaginated(t *testing.T) {
	dir := prependFakeGH(t, `case "$*" in
*timeline*)
	printf 'agent-complete\n'
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	if _, _, err := c.PriorClaimState("10"); err != nil {
		t.Fatalf("PriorClaimState: %v", err)
	}

	args := readCallArgs(t, dir, 0)
	if !strings.Contains(args, "repos/owner/repo/issues/10/timeline") {
		t.Fatalf("gh api call args = %q, want it to query the issue timeline endpoint", args)
	}
	if !strings.Contains(args, "--paginate") {
		t.Fatalf("gh api call args = %q, want --paginate", args)
	}
}

func TestExecClient_BranchExists_ExactMatch(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*matching-refs/heads/agent/issue-1*)
	printf 'refs/heads/agent/issue-1\n'
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	exists, err := c.BranchExists("agent/issue-1")
	if err != nil {
		t.Fatalf("BranchExists: %v", err)
	}
	if !exists {
		t.Error("BranchExists(agent/issue-1) = false, want true")
	}
}

// The matching-refs endpoint prefix-matches, so a query for "agent/issue-1"
// also returns the longer sibling "agent/issue-10". A naive non-empty-response
// check would wrongly report the shorter branch as existing.
func TestExecClient_BranchExists_RejectsPrefixMatch(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*matching-refs/heads/agent/issue-1*)
	printf 'refs/heads/agent/issue-10\n'
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	exists, err := c.BranchExists("agent/issue-1")
	if err != nil {
		t.Fatalf("BranchExists: %v", err)
	}
	if exists {
		t.Error("BranchExists(agent/issue-1) = true, want false — only the longer sibling branch matched")
	}
}

func TestExecClient_BranchExists_NoMatch(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*matching-refs/heads/agent/issue-1*)
	printf ''
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	exists, err := c.BranchExists("agent/issue-1")
	if err != nil {
		t.Fatalf("BranchExists: %v", err)
	}
	if exists {
		t.Error("BranchExists(agent/issue-1) = true, want false")
	}
}

// An empty branch would query every ref under heads/ instead of one branch, so
// BranchExists must refuse it without shelling out.
func TestExecClient_BranchExists_RejectsEmptyBranch(t *testing.T) {
	dir := prependFakeGH(t, `exit 1`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	if _, err := c.BranchExists(""); err == nil {
		t.Error("BranchExists(\"\"): want error, got nil")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "call-*.txt")); len(matches) != 0 {
		t.Errorf("want no gh invocation for an empty branch, got %d", len(matches))
	}
}

func TestExecClient_BranchProtected_Protected(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*branches/main/protection*)
	printf '{"required_status_checks":null}\n'
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	protected, err := c.BranchProtected("main")
	if err != nil {
		t.Fatalf("BranchProtected: %v", err)
	}
	if !protected {
		t.Error("BranchProtected(main) = false, want true")
	}
}

// A classic-endpoint "Branch not protected" 404 plus a ruleset probe reporting
// no applicable rules is a definitive answer: (false, nil), not an error.
func TestExecClient_BranchProtected_NotProtected(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*branches/main/protection*)
	echo 'gh: Branch not protected (HTTP 404)' >&2
	exit 1
	;;
*rules/branches/main*)
	printf '0\n'
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	protected, err := c.BranchProtected("main")
	if err != nil {
		t.Fatalf("BranchProtected: want nil error for the 'not protected' 404, got %v", err)
	}
	if protected {
		t.Error("BranchProtected(main) = true, want false")
	}
}

// A repository ruleset is the mechanism README.md and SECURITY.md tell
// operators to configure, and the classic branches/{branch}/protection
// endpoint never reports it: it 404s "Branch not protected" regardless.
func TestExecClient_BranchProtected_RulesetOnly(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*branches/main/protection*)
	echo 'gh: Branch not protected (HTTP 404)' >&2
	exit 1
	;;
*rules/branches/main*)
	printf '1\n'
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	protected, err := c.BranchProtected("main")
	if err != nil {
		t.Fatalf("BranchProtected: %v", err)
	}
	if !protected {
		t.Error("BranchProtected(main) = false, want true (ruleset-protected)")
	}
}

// This project's documented fine-grained PAT scope (Contents, Pull requests and
// Issues RW plus Metadata R, no Administration: read) makes 403, not the
// "Branch not protected" 404, what the classic endpoint really returns. It is
// the most common real configuration, so a ruleset-protected branch under that
// token must fall through to branchProtectedByRuleset and report protected.
func TestExecClient_BranchProtected_DocumentedTokenFallsThroughOn403(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*branches/main/protection*)
	echo 'gh: Resource not accessible by personal access token (HTTP 403)' >&2
	exit 1
	;;
*rules/branches/main*)
	printf '1\n'
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	protected, err := c.BranchProtected("main")
	if err != nil {
		t.Fatalf("BranchProtected: want nil error for the documented-token 403 fallthrough, got %v", err)
	}
	if !protected {
		t.Error("BranchProtected(main) = false, want true (ruleset-protected)")
	}
}

// A 403 means the classic mechanism was never read, so a zero ruleset count
// cannot rule out a classic-only rule the token can't see, the setup
// docs/reference.md prescribes. Reporting (false, nil) would be a false
// required-check failure under MERGE_MODE=immediate/auto. A rate-limited 403
// must also be errors.Is forge.ErrRateLimit, which ghCommandErrText classifies.
func TestExecClient_BranchProtected_HTTP403ZeroRuleset(t *testing.T) {
	cases := []struct {
		name          string
		classicStderr string
		wantRateLimit bool
		wantSubstring string
	}{
		{
			name:          "documented token scope",
			classicStderr: "gh: Resource not accessible by personal access token (HTTP 403)",
			wantRateLimit: false,
			wantSubstring: "no ruleset applies",
		},
		{
			name:          "rate limited",
			classicStderr: "API rate limit exceeded for installation ID 12345678. (HTTP 403)",
			wantRateLimit: true,
			wantSubstring: "API rate limit exceeded",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prependFakeGH(t, fmt.Sprintf(`case "$*" in
*branches/main/protection*)
	echo '%s' >&2
	exit 1
	;;
*rules/branches/main*)
	printf '0\n'
	;;
*)
	exit 1
	;;
esac`, tc.classicStderr))

			c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
			protected, err := c.BranchProtected("main")
			if err == nil {
				t.Fatal("BranchProtected: want error for a 403 with zero applicable rulesets, got nil")
			}
			if protected {
				t.Error("BranchProtected(main) = true, want false alongside the error")
			}
			if got := errors.Is(err, forge.ErrRateLimit); got != tc.wantRateLimit {
				t.Errorf("errors.Is(err, forge.ErrRateLimit) = %v, want %v; err: %v", got, tc.wantRateLimit, err)
			}
			if !strings.Contains(err.Error(), tc.wantSubstring) {
				t.Errorf("BranchProtected(main) error = %q, want it to contain %q", err.Error(), tc.wantSubstring)
			}
		})
	}
}

// A ruleset probe that itself fails (network, insufficient token scope) must
// surface an error, never a false "not protected".
func TestExecClient_BranchProtected_RulesetProbeFailure(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*branches/main/protection*)
	echo 'gh: Branch not protected (HTTP 404)' >&2
	exit 1
	;;
*rules/branches/main*)
	echo 'gh: Resource not accessible by personal access token (HTTP 403)' >&2
	exit 1
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	protected, err := c.BranchProtected("main")
	if err == nil {
		t.Fatal("BranchProtected: want error for a ruleset probe failure, got nil")
	}
	if protected {
		t.Error("BranchProtected(main) = true, want false alongside the error")
	}
}

// The fake script leaves the ruleset probe unstubbed, so the 403 fallthrough
// hits a genuine probe failure, which must not resolve to "not protected".
func TestExecClient_BranchProtected_ProbeFailure(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*branches/main/protection*)
	echo 'gh: Resource not accessible by personal access token (HTTP 403)' >&2
	exit 1
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	protected, err := c.BranchProtected("main")
	if err == nil {
		t.Fatal("BranchProtected: want error for a non-404 probe failure, got nil")
	}
	if protected {
		t.Error("BranchProtected(main) = true, want false alongside the error")
	}
}

// An unpushed base branch, a typo'd branch name and a repo the token can't see
// all return a bare 404, which must not be conflated with GitHub's definitive
// "Branch not protected" answer. The error also routes through
// ghCommandErrText, so the classic endpoint's stderr reaches the message
// (issue #2864).
func TestExecClient_BranchProtected_GenericNotFound(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*branches/main/protection*)
	echo 'gh: Not Found (HTTP 404)' >&2
	exit 1
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	protected, err := c.BranchProtected("main")
	if err == nil {
		t.Fatal("BranchProtected: want error for a generic 404, got nil")
	}
	if protected {
		t.Error("BranchProtected(main) = true, want false alongside the error")
	}
	if !strings.Contains(err.Error(), "Not Found (HTTP 404)") {
		t.Errorf("BranchProtected: error should surface gh's stderr, got: %v", err)
	}
}

// A classic endpoint that fails while writing nothing to stderr must not leave
// a dangling ": " suffix on the error (issue #2864).
func TestExecClient_BranchProtected_GenericFailureEmptyStderr(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*branches/main/protection*)
	exit 1
	;;
*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	protected, err := c.BranchProtected("main")
	if err == nil {
		t.Fatal("BranchProtected: want error for a generic failure, got nil")
	}
	if protected {
		t.Error("BranchProtected(main) = true, want false alongside the error")
	}
	if strings.Contains(err.Error(), ": \n") || strings.HasSuffix(err.Error(), ": ") {
		t.Errorf("BranchProtected: error should not have a dangling \": \" suffix for empty stderr, got: %q", err.Error())
	}
}

func TestExecClient_BranchProtected_RejectsEmptyBranch(t *testing.T) {
	dir := prependFakeGH(t, `exit 1`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	if _, err := c.BranchProtected(""); err == nil {
		t.Error("BranchProtected(\"\"): want error, got nil")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "call-*.txt")); len(matches) != 0 {
		t.Errorf("want no gh invocation for an empty branch, got %d", len(matches))
	}
}

func TestExecClient_ImplementsBranchProtectionForge(t *testing.T) {
	var _ forge.BranchProtectionForge = NewExecClient("owner/repo", testLabels, "agent/issue-")
}

// TouchesOf needs the full body via `gh issue view`, since ListIssues' --json
// number,title summary never includes one. It parses the "## Touches" section
// with the same body grammar DepsOf's fallback uses.
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

// Issue's error must carry gh's stderr text (issue #2864).
func TestExecClient_Issue_ErrorSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*"issue view"*)
	printf 'HTTP 404: Not Found\n' >&2
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	_, err := c.Issue("10")
	if err == nil {
		t.Fatal("Issue: want error, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("Issue error must contain gh's stderr; got: %q", err.Error())
	}
}

// ListOpenIssues queries every open issue with no --label filter, unlike
// ListIssues, which scopes to one dispatch state's label. The fixture is out of
// order because the result must come back ascending by number.
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

// ListOpenIssues surfaces gh's stderr the way ListIssues does (issue #2864).
func TestExecClient_ListOpenIssues_ErrorSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*"issue list"*)
	echo 'GraphQL: Could not resolve to a Repository with the name '"'"'owner/repo'"'"'. (repository)' >&2
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	_, err := c.ListOpenIssues()
	if err == nil {
		t.Fatal("ListOpenIssues: want error, got nil")
	}
	if !strings.Contains(err.Error(), "Could not resolve to a Repository") {
		t.Fatalf("ListOpenIssues error must contain gh's stderr; got: %q", err.Error())
	}
}

// Without the stderr text, the re-discover loop's queryOpenIssues path
// (main.go) sees only a bare "exit status 1".
func TestExecClient_ListIssues_ErrorSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*"issue list"*)
	echo 'GraphQL: Could not resolve to a Repository with the name '"'"'owner/repo'"'"'. (repository)' >&2
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, err := c.ListIssues(forge.Dispatchable)
	if err == nil {
		t.Fatal("ListIssues: want error, got nil")
	}
	if !strings.Contains(err.Error(), "Could not resolve to a Repository") {
		t.Fatalf("ListIssues error must contain gh's stderr; got: %q", err.Error())
	}
}

func TestExecClient_ListIssues_ErrorEmptyStderrNoTrailingColon(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*"issue list"*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	_, err := c.ListIssues(forge.Dispatchable)
	if err == nil {
		t.Fatal("ListIssues: want error, got nil")
	}
	if strings.HasSuffix(err.Error(), ": ") {
		t.Fatalf("ListIssues error must not have a trailing colon-space; got: %q", err.Error())
	}
}

// issueLabels' error must carry gh's stderr text (issue #2864).
func TestExecClient_IssueLabels_ErrorSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*"issue view"*)
	printf 'HTTP 404: Not Found\n' >&2
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	_, err := c.issueLabels("10")
	if err == nil {
		t.Fatal("issueLabels: want error, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("issueLabels error must contain gh's stderr; got: %q", err.Error())
	}
}

// A client built with no VerdictLabels (the work-kind path) must error rather
// than shell out `gh issue edit --add-label ""`, which would silently corrupt
// the issue's label set.
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

// Swapping labels on an issue that no longer carries InProgress would leave it
// multi-labeled. This is the double-dispatch guard from issue #701.
func TestExecClient_CompleteVerdict_MissingInProgressErrorsWithoutEditing(t *testing.T) {
	dir := prependFakeGH(t, `case "$*" in
*"issue view"*)
	printf '{"number":10,"title":"t","body":"b","state":"OPEN","labels":[{"name":"agent-research-recommend"},{"name":"agent-review-finding"}]}\n'
	;;
esac
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-", WithVerdictLabels(forge.ResearchVerdictLabels()))
	err := c.CompleteVerdict("10", forge.Recommend)
	if err == nil {
		t.Fatal("want error when issue lacks InProgress label, got nil")
	}

	const want = `gh issue edit 10: expected "agent-in-progress" label, issue has [agent-research-recommend, agent-review-finding]`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() == "call-01.txt" {
			t.Fatalf("CompleteVerdict must not shell out to gh issue edit when InProgress is missing; found %s", e.Name())
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatalf("call-00.txt (gh issue view) not written: %v", err)
	}
	argv := string(raw)
	if !strings.HasSuffix(argv, "--json\nlabels\n") {
		t.Errorf("argv = %q, want labels-only --json call as the final args", argv)
	}
}

func TestExecClient_CompleteVerdict_InProgressPresentEditsIssue(t *testing.T) {
	dir := prependFakeGH(t, `case "$*" in
*"issue view"*)
	printf '{"number":10,"title":"t","body":"b","state":"OPEN","labels":[{"name":"agent-in-progress"}]}\n'
	;;
esac
`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-", WithVerdictLabels(forge.ResearchVerdictLabels()))
	if err := c.CompleteVerdict("10", forge.Recommend); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	viewRaw, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatalf("call-00.txt (gh issue view) not written: %v", err)
	}
	viewArgv := string(viewRaw)
	if !strings.HasSuffix(viewArgv, "--json\nlabels\n") {
		t.Errorf("view argv = %q, want labels-only --json call as the final args", viewArgv)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "call-01.txt"))
	if err != nil {
		t.Fatalf("call-01.txt (gh issue edit) not written: %v", err)
	}
	argv := string(raw)
	if !strings.Contains(argv, "--add-label\nagent-research-recommend") {
		t.Errorf("argv = %q, want --add-label agent-research-recommend", argv)
	}
	if !strings.Contains(argv, "--remove-label\nagent-in-progress") {
		t.Errorf("argv = %q, want --remove-label agent-in-progress", argv)
	}

	calls, _ := filepath.Glob(filepath.Join(dir, "call-*.txt"))
	if len(calls) != 2 {
		t.Errorf("gh call count = %d, want 2 (view + exactly one edit)", len(calls))
	}
}

// TransitionState's error must carry gh's stderr text (issue #2864).
func TestExecClient_TransitionState_GenuineFailureSurfaced(t *testing.T) {
	prependFakeGH(t, `printf 'HTTP 403: Resource not accessible by integration\n' >&2
exit 1`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	err := c.TransitionState("10", forge.Dispatchable, forge.InProgress)
	if err == nil {
		t.Fatal("TransitionState: want error, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("TransitionState error must contain gh's stderr; got: %q", err.Error())
	}
}

// The InProgress precondition passes here, so the failure comes from the `gh
// issue edit` call itself and its stderr must reach the error (issue #2864).
func TestExecClient_CompleteVerdict_GenuineEditFailureSurfaced(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*"issue view"*)
	printf '{"number":10,"title":"t","body":"b","state":"OPEN","labels":[{"name":"agent-in-progress"}]}\n'
	;;
*"issue edit"*)
	printf 'HTTP 403: Resource not accessible by integration\n' >&2
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", testLabels, "agent/issue-", WithVerdictLabels(forge.ResearchVerdictLabels()))
	err := c.CompleteVerdict("10", forge.Recommend)
	if err == nil {
		t.Fatal("CompleteVerdict: want error, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("CompleteVerdict error must contain gh's stderr; got: %q", err.Error())
	}
}

// A claim removes the stale agent-failed label a prior run left behind, not
// just the from-state label, matching the dispatch workflow's
// claim-remove-labels set (#1985).
func TestExecClient_TransitionState_ClaimStripsStaleFailedLabel(t *testing.T) {
	dir := prependFakeGH(t, "")

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	if err := c.TransitionState("10", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatalf("call-00.txt (gh issue edit) not written: %v", err)
	}
	argv := string(raw)
	if !strings.Contains(argv, "--remove-label\nagent-failed") {
		t.Errorf("argv = %q, want --remove-label agent-failed", argv)
	}
}

// A claim also strips a stale agent-complete label, which is the
// re-research or re-trigger-after-complete case (#1985).
func TestExecClient_TransitionState_ClaimStripsStaleCompleteLabel(t *testing.T) {
	dir := prependFakeGH(t, "")

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	if err := c.TransitionState("10", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatalf("call-00.txt (gh issue edit) not written: %v", err)
	}
	argv := string(raw)
	if !strings.Contains(argv, "--remove-label\nagent-complete") {
		t.Errorf("argv = %q, want --remove-label agent-complete", argv)
	}
}

// The stale-terminal-label strip is claim-only, so a transition that does not
// land on InProgress still emits exactly one --add-label/--remove-label pair
// (#1985).
func TestExecClient_TransitionState_NonClaimTransitionUnchanged(t *testing.T) {
	dir := prependFakeGH(t, "")

	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	if err := c.TransitionState("10", forge.InProgress, forge.Complete); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatalf("call-00.txt (gh issue edit) not written: %v", err)
	}
	argv := string(raw)
	if strings.Count(argv, "--remove-label") != 1 {
		t.Errorf("argv = %q, want exactly one --remove-label", argv)
	}
	if !strings.Contains(argv, "--remove-label\nagent-in-progress") {
		t.Errorf("argv = %q, want --remove-label agent-in-progress", argv)
	}
}

// Criterion 4 of #1985: the stale-label strip must not perturb an ordinary
// claim. This uses the stateful fakeGHState harness (contract_test.go) instead
// of prependFakeGH so it can assert the resulting label set rather than the
// argv of the edit call.
func TestExecClient_TransitionState_NormalClaimUnchanged(t *testing.T) {
	h := newGithubHarness(t)
	h.SeedIssue(forge.Issue{Number: "55", Title: "normal claim", Labels: []string{"ready-for-agent"}})

	if err := h.Tracker().TransitionState("55", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}

	iss, err := h.Tracker().Issue("55")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(iss.Labels) != 1 || iss.Labels[0] != "agent-in-progress" {
		t.Errorf("labels = %v, want exactly [agent-in-progress]", iss.Labels)
	}
}

// Every label a claim removes must appear in agent-dispatch.yml's own
// claim-remove-labels list, read straight from the workflow file, so the two
// cannot drift apart (#1985). The reverse is not asserted: agent-trigger and
// agent-recover are GitHub Actions trigger gestures with no forge.DispatchState
// equivalent, so the Go claim cannot strip them.
func TestExecClient_TransitionState_ClaimRemoveLabelsMatchDispatchWorkflow(t *testing.T) {
	workflowSet, rawValue := forgetest.ParseWorkflowRemoveLabelSet(t,
		filepath.Join("..", "..", "..", "..", "..", ".github", "workflows", "agent-dispatch.yml"),
		"claim-remove-labels")

	dir := prependFakeGH(t, "")
	c := NewExecClient("owner/repo", testLabels, "agent/issue-")
	if err := c.TransitionState("10", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	callRaw, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatalf("call-00.txt (gh issue edit) not written: %v", err)
	}
	argv := strings.Split(strings.TrimRight(string(callRaw), "\n"), "\n")
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] != "--remove-label" {
			continue
		}
		label := argv[i+1]
		if !workflowSet[label] {
			t.Errorf("claim removes label %q, not in agent-dispatch.yml's claim-remove-labels %q", label, rawValue)
		}
	}
}

func TestProbe_PositionalSlug(t *testing.T) {
	// Both gh calls exit 0. Probe may still error on the empty output, which
	// this test does not care about.
	dir := prependFakeGH(t, "")

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	c.Probe() //nolint:errcheck

	// call-01.txt is the `gh repo view` invocation; call-00.txt is gh auth status.
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

func TestProbe_StderrSurfaced(t *testing.T) {
	// Call 0 is gh auth status and succeeds; call 1 is gh repo view and fails
	// with a distinctive stderr.
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
	if errors.Is(err, forge.ErrRateLimit) {
		t.Fatalf("error must not be errors.Is forge.ErrRateLimit; got: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("error must contain gh's stderr; got: %v", err)
	}
}

// FailureDetail must query `gh api graphql`, not `gh pr checks`: the latter
// hits REST check-runs, which 403s under a fine-grained PAT.
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

// FailureDetail routes its error through ghCommandErr, so gh's stderr text
// reaches the caller (issue #2864).
func TestFailureDetail_GraphQLFailureSurfacesStderr(t *testing.T) {
	prependFakeGH(t, `printf 'HTTP 403: Forbidden\n' >&2
exit 1
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	_, err := c.FailureDetail("https://github.com/owner/repo/pull/42")
	if err == nil {
		t.Fatal("FailureDetail: want error, got nil")
	}
	if !strings.Contains(err.Error(), "HTTP 403: Forbidden") {
		t.Fatalf("FailureDetail error must surface gh's stderr; got: %v", err)
	}
}

// NeedsUpdate uses the compare API's behind_by, a pure git-ancestry fact.
// GraphQL's mergeStateStatus BEHIND would not do: GitHub reports it only when
// branch protection requires branches to be up to date before merging, a
// setting this project's fine-grained PAT cannot even read (issue #936).
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

func readCallArgs(t *testing.T, dir string, n int) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("call-%02d.txt", n)))
	if err != nil {
		t.Fatalf("call-%02d.txt not written: %v", n, err)
	}
	return strings.Join(strings.Split(strings.TrimSpace(string(raw)), "\n"), " ")
}

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
			{TypeName: "CheckRun", Name: "huge", Conclusion: "FAILURE", Summary: strings.Repeat("x", forge.MaxFailureDetailBytes*2)},
		}
		got := renderFailureDetail(contexts)
		if len(got) > forge.MaxFailureDetailBytes {
			t.Fatalf("detail not bounded: got %d bytes, want <= %d", len(got), forge.MaxFailureDetailBytes)
		}
	})
}

// A blocked-by-checks refusal and a real conflict share the same "not
// mergeable" stderr wording, so substring matching alone cannot tell them
// apart: the queried mergeable state decides (issue #566).
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

// A CONFLICTING mergeable state must still classify as forge.ErrMergeConflict,
// so the rebase-retry path keeps engaging for real conflicts.
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

// UNKNOWN means GitHub has not finished computing mergeability, so the refusal
// gets its own error rather than being folded into forge.ErrMergeConflict or
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

// A transient stderr (a 502 from GitHub) is classified from the text alone, so
// a caller can detect it without querying the PR's mergeable state.
func TestClassifyMergeFailure_TransientStderrWrapsErrMergeTransient(t *testing.T) {
	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	mergeErr := errors.New("exit status 1")
	err := c.classifyMergeFailure("https://github.com/owner/repo/pull/42", mergeErr, "HTTP 502: Bad Gateway (https://api.github.com/graphql)\n")
	if !errors.Is(err, forge.ErrMergeTransient) {
		t.Fatalf("want forge.ErrMergeTransient, got: %v", err)
	}
}

// An auth failure is not retryable and must not classify as transient.
func TestClassifyMergeFailure_NonTransientNonConflictStderrDoesNotWrapErrMergeTransient(t *testing.T) {
	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	mergeErr := errors.New("exit status 1")
	err := c.classifyMergeFailure("https://github.com/owner/repo/pull/42", mergeErr, "HTTP 401: Bad credentials (https://api.github.com/graphql)\n")
	if errors.Is(err, forge.ErrMergeTransient) {
		t.Fatalf("non-transient auth failure must not classify as forge.ErrMergeTransient, got: %v", err)
	}
}

// `gh pr ready` on an already-ready PR prints a notice to stderr but exits 0.
// MarkReady must not turn that notice into a spurious error (issue #1651).
func TestMarkReady_AlreadyReadyIsIdempotentNoOp(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "ready" ]; then
  printf '! Pull request owner/repo#42 is already "ready for review"\n' >&2
  exit 0
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	if err := c.MarkReady("https://github.com/owner/repo/pull/42"); err != nil {
		t.Fatalf("MarkReady on an already-ready PR must be a no-op, got: %v", err)
	}
}

// MatchesAnyMarker requires lowercase markers, and rateLimitMarkers feeds that
// shared helper, so a mixed-case marker would silently never match.
func TestRateLimitMarkers_AreLowercase(t *testing.T) {
	for _, marker := range rateLimitMarkers {
		if marker == "" {
			t.Error("rateLimitMarkers: contains an empty marker")
		}
		if marker != strings.ToLower(marker) {
			t.Errorf("rateLimitMarkers: marker %q is not lowercase", marker)
		}
	}
}

// isRateLimited must recognize GitHub's primary hourly-quota phrasing and its
// secondary and abuse-detection phrasings, without pulling in unrelated gh
// failures such as auth, not-found and network errors (issue #2865).
func TestIsRateLimited(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{
			name:   "primary quota exceeded",
			stderr: "API rate limit exceeded for installation ID 12345678.",
			want:   true,
		},
		{
			name:   "secondary abuse detection",
			stderr: "You have triggered an abuse detection mechanism and have been temporarily blocked from content creation. Please retry your request again later.",
			want:   true,
		},
		{
			name:   "secondary generic rate limit",
			stderr: "You have exceeded a secondary rate limit and have been temporarily blocked from content creation. Please retry your request again later.",
			want:   true,
		},
		{
			name:   "already exceeded phrasing",
			stderr: "You have already exceeded your GraphQL points budget for this hour. Please wait a few minutes before you try again.",
			want:   true,
		},
		{
			name:   "unauthorized is not rate limiting",
			stderr: "HTTP 401: Bad credentials (https://api.github.com/graphql)",
			want:   false,
		},
		{
			name:   "not found is not rate limiting",
			stderr: "GraphQL: Could not resolve to a Repository with the name 'owner/repo'. (repository)",
			want:   false,
		},
		{
			name:   "network failure is not rate limiting",
			stderr: "dial tcp: lookup api.github.com: no such host",
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRateLimited(tt.stderr); got != tt.want {
				t.Errorf("isRateLimited(%q) = %v, want %v", tt.stderr, got, tt.want)
			}
		})
	}
}

func TestMarkReady_GenuineFailureSurfaced(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "ready" ]; then
  printf 'HTTP 403: Resource not accessible by integration\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	err := c.MarkReady("https://github.com/owner/repo/pull/42")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error must surface gh's stderr, got: %v", err)
	}
}

// `gh pr ready --undo` on an already-draft PR is idempotent the way `gh pr
// ready` is, so its stderr notice must not become an error.
func TestMarkDraft_AlreadyDraftIsIdempotentNoOp(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "ready" ] && [ "$3" = "--undo" ]; then
  printf '! Pull request owner/repo#42 is already a "draft" pull request\n' >&2
  exit 0
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	if err := c.MarkDraft("https://github.com/owner/repo/pull/42"); err != nil {
		t.Fatalf("MarkDraft on an already-draft PR must be a no-op, got: %v", err)
	}
}

func TestMarkDraft_GenuineFailureSurfaced(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "pr" ] && [ "$2" = "ready" ] && [ "$3" = "--undo" ]; then
  printf 'HTTP 403: Resource not accessible by integration\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	err := c.MarkDraft("https://github.com/owner/repo/pull/42")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error must surface gh's stderr, got: %v", err)
	}
}

// GitHub's own merged-PR auto-close has already run here, so CloseMergedIssue
// must not shell out again (issue #1892). The fake script exits 1 if it ever
// sees a `gh issue close`.
func TestExecClient_CloseMergedIssue_AlreadyClosedIsNoOp(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "issue" ] && [ "$2" = "view" ]; then
  echo '{"number":42,"title":"t","body":"","state":"CLOSED","labels":[]}'
  exit 0
fi
if [ "$1" = "issue" ] && [ "$2" = "close" ]; then
  echo "must not be called" >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	if err := c.CloseMergedIssue("42"); err != nil {
		t.Fatalf("CloseMergedIssue on an already-closed issue must be a no-op, got: %v", err)
	}
}

// A still-open issue is the case GitHub's auto-close missed because the PR body
// omitted or reworded the Closes #<N> keyword.
func TestExecClient_CloseMergedIssue_ClosesOpenIssue(t *testing.T) {
	dir := prependFakeGH(t, `if [ "$1" = "issue" ] && [ "$2" = "view" ]; then
  echo '{"number":42,"title":"t","body":"","state":"OPEN","labels":[]}'
  exit 0
fi
if [ "$1" = "issue" ] && [ "$2" = "close" ]; then
  exit 0
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	if err := c.CloseMergedIssue("42"); err != nil {
		t.Fatalf("CloseMergedIssue on an open issue: %v", err)
	}

	calls, err := filepath.Glob(filepath.Join(dir, "call-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	closeCall, err := os.ReadFile(calls[len(calls)-1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(closeCall), "issue\nclose\n42") {
		t.Errorf("last gh call = %q, want `gh issue close 42 ...`", closeCall)
	}
}

// A real close failure must not be swallowed as if it were the idempotent
// already-closed case.
func TestExecClient_CloseMergedIssue_GenuineFailureSurfaced(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "issue" ] && [ "$2" = "view" ]; then
  echo '{"number":42,"title":"t","body":"","state":"OPEN","labels":[]}'
  exit 0
fi
if [ "$1" = "issue" ] && [ "$2" = "close" ]; then
  printf 'HTTP 403: Resource not accessible by integration\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	err := c.CloseMergedIssue("42")
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error must surface gh's stderr, got: %v", err)
	}
}

// A close that fails while writing nothing to stderr must not leave a dangling
// "exit status 1: " suffix on the error (issue #2864).
func TestExecClient_CloseMergedIssue_EmptyStderrNoTrailingColon(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "issue" ] && [ "$2" = "view" ]; then
  echo '{"number":42,"title":"t","body":"","state":"OPEN","labels":[]}'
  exit 0
fi
if [ "$1" = "issue" ] && [ "$2" = "close" ]; then
  exit 1
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	err := c.CloseMergedIssue("42")
	if err == nil {
		t.Fatal("CloseMergedIssue: want error, got nil")
	}
	if strings.HasSuffix(err.Error(), ": ") {
		t.Fatalf("CloseMergedIssue error must not have a trailing colon-space; got: %q", err.Error())
	}
}

// Comment's error must carry gh's stderr text (issue #2864).
func TestExecClient_Comment_GenuineFailureSurfaced(t *testing.T) {
	prependFakeGH(t, `printf 'HTTP 403: Resource not accessible by integration\n' >&2
exit 1`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	err := c.Comment("10", "hello")
	if err == nil {
		t.Fatal("Comment: want error, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("Comment error must contain gh's stderr; got: %q", err.Error())
	}
}

// forge.HostPostedIssueFiler closes the read-only capability gate's
// issue-filing axis (issue #2028).
func TestExecClient_ImplementsHostPostedIssueFiler(t *testing.T) {
	var _ forge.HostPostedIssueFiler = NewExecClient("owner/repo", testLabels, "agent/issue-")
}

// forge.GithubTracker is the positive marker settle's ensureClosesReference
// uses to scope its "Closes #N" injection to GitHub-hosted PRs, never forgejo,
// whose issue numbers live in a foreign namespace (issue #2341).
func TestExecClient_ImplementsGithubTracker(t *testing.T) {
	var _ forge.GithubTracker = NewExecClient("owner/repo", testLabels, "agent/issue-")
}

// forge.MergeCloser is settle's deterministic post-merge close backstop (issue
// #1892). forge.IssueCloser stays reserved for the local adapter's
// reconcile-owned "closed:" axis. A github adapter implementing it too would
// let an ISSUE_TRACKER=local plus CODE_FORGE=github pairing close a local
// issue through the wrong path.
func TestExecClient_ImplementsMergeCloser(t *testing.T) {
	var _ forge.MergeCloser = NewExecClient("owner/repo", testLabels, "agent/issue-")
	if _, ok := any(NewExecClient("owner/repo", testLabels, "agent/issue-")).(forge.IssueCloser); ok {
		t.Error("ExecClient satisfies forge.IssueCloser, want it hidden")
	}
}

func TestExecClient_PostIssue_ReturnsURL(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "issue" ] && [ "$2" = "create" ]; then
  echo "https://github.com/owner/repo/issues/99"
  exit 0
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	url, err := c.PostIssue("a title", "a body", []string{"ready-for-agent"})
	if err != nil {
		t.Fatalf("PostIssue: %v", err)
	}
	if url != "https://github.com/owner/repo/issues/99" {
		t.Errorf("PostIssue url = %q, want %q", url, "https://github.com/owner/repo/issues/99")
	}
}

// PostIssue files against the adapter's own repo, so no payload can redirect
// the issue elsewhere (issue #1949's do-not-trust-the-agent-target invariant).
// The exact argv is asserted because a stray flag is how that leaks.
func TestExecClient_PostIssue_ArgsCarryTitleBodyAndOneLabelFlagPerLabel(t *testing.T) {
	dir := prependFakeGH(t, `if [ "$1" = "issue" ] && [ "$2" = "create" ]; then
  echo "https://github.com/owner/repo/issues/1"
  exit 0
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	if _, err := c.PostIssue("a title", "a body", []string{"ready-for-agent", "agent-review-finding"}); err != nil {
		t.Fatalf("PostIssue: %v", err)
	}

	calls, err := filepath.Glob(filepath.Join(dir, "call-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("gh call count = %d, want 1", len(calls))
	}
	got, err := os.ReadFile(calls[0])
	if err != nil {
		t.Fatal(err)
	}
	want := "issue\ncreate\n--repo\nowner/repo\n--title\na title\n--body\na body\n--label\nready-for-agent\n--label\nagent-review-finding\n"
	if string(got) != want {
		t.Errorf("gh call = %q, want %q", got, want)
	}
}

func TestExecClient_PostIssue_GenuineFailureSurfaced(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "issue" ] && [ "$2" = "create" ]; then
  printf 'HTTP 403: Resource not accessible by integration\n' >&2
  exit 1
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	_, err := c.PostIssue("a title", "a body", nil)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "gh issue create") {
		t.Errorf("error should name the operation, got: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error should surface gh's stderr, got: %v", err)
	}
}

// PostIssue once appended stderr.String() unconditionally, leaving a dangling
// "exit status 1: " suffix when stderr was empty (issue #2864).
func TestExecClient_PostIssue_ErrorEmptyStderrNoTrailingColon(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "issue" ] && [ "$2" = "create" ]; then
  exit 1
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	_, err := c.PostIssue("a title", "a body", nil)
	if err == nil {
		t.Fatal("PostIssue: want error, got nil")
	}
	if strings.HasSuffix(err.Error(), ": ") {
		t.Fatalf("PostIssue error must not have a trailing colon-space; got: %q", err.Error())
	}
}

// ghCommandErr folds an *exec.ExitError's captured Stderr into the message.
// exec.Cmd.Output populates that field only when Stderr was left nil, which is
// how ListIssues, Issue and issueLabels call gh.
func TestGhCommandErr_StderrSurfaced(t *testing.T) {
	_, err := exec.Command("sh", "-c", "printf hello 1>&2; exit 1").Output()
	if err == nil {
		t.Fatal("want subprocess error, got nil")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("want *exec.ExitError, got %T: %v", err, err)
	}

	got := ghCommandErr("gh issue list", err)
	if got == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(got.Error(), "gh issue list") {
		t.Errorf("error should name the operation, got: %v", got)
	}
	if !strings.Contains(got.Error(), "hello") {
		t.Errorf("error should surface gh's stderr, got: %v", got)
	}
	if !strings.Contains(got.Error(), "exit status") {
		t.Errorf("error should still carry the exit status, got: %v", got)
	}
	if !errors.As(got, &exitErr) {
		t.Errorf("error should still wrap the original *exec.ExitError, got: %v", got)
	}
}

// Empty or whitespace-only stderr must not produce a dangling ": " suffix.
// That is the bug PostIssue had in exec_issues.go.
func TestGhCommandErr_EmptyStderrDegradesCleanly(t *testing.T) {
	cases := []struct {
		name   string
		script string
	}{
		{"empty stderr", "exit 1"},
		{"whitespace-only stderr", "printf '   \\n\\t ' 1>&2; exit 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := exec.Command("sh", "-c", tc.script).Output()
			if err == nil {
				t.Fatal("want subprocess error, got nil")
			}

			got := ghCommandErr("gh issue list", err)
			if got == nil {
				t.Fatal("want error, got nil")
			}
			want := fmt.Sprintf("gh issue list: %s", err)
			if got.Error() != want {
				t.Errorf("error = %q, want %q (no dangling separator)", got.Error(), want)
			}
		})
	}
}

// A missing gh binary yields *exec.Error rather than *exec.ExitError, which
// must not panic the stderr-folding path.
func TestGhCommandErr_NonExitError(t *testing.T) {
	_, err := exec.Command("this-binary-does-not-exist-xyz").Output()
	if err == nil {
		t.Fatal("want error, got nil")
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		t.Fatalf("want a non-ExitError failure, got *exec.ExitError: %v", err)
	}

	got := ghCommandErr("gh issue list", err)
	if got == nil {
		t.Fatal("want error, got nil")
	}
	want := fmt.Sprintf("gh issue list: %s", err)
	if got.Error() != want {
		t.Errorf("error = %q, want %q", got.Error(), want)
	}
	if !errors.Is(got, err) {
		t.Errorf("error should still wrap the original error, got: %v", got)
	}
}

// A pathological stderr dump is truncated to a cap, and the truncation shows in
// the message rather than being silently swallowed.
func TestGhCommandErr_StderrTruncated(t *testing.T) {
	_, err := exec.Command("sh", "-c", "yes x | head -c 100000 1>&2; exit 1").Output()
	if err == nil {
		t.Fatal("want subprocess error, got nil")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("want *exec.ExitError, got %T: %v", err, err)
	}
	// exec.Cmd caps captured stderr around 64KiB (prefixSuffixSaver), well above
	// ghCommandErr's own cap, so this still exercises truncation.
	if len(exitErr.Stderr) < 8192 {
		t.Fatalf("test setup: want a large captured stderr, got %d bytes", len(exitErr.Stderr))
	}

	got := ghCommandErr("gh issue list", err)
	if got == nil {
		t.Fatal("want error, got nil")
	}
	if len(got.Error()) >= len(exitErr.Stderr) {
		t.Errorf("error message should be bounded well below the untruncated stderr size, got %d bytes", len(got.Error()))
	}
	if !strings.Contains(got.Error(), "truncated") {
		t.Errorf("truncation should be visible in the message, got a %d-byte message", len(got.Error()))
	}
}

// ghCommandErrText takes stderr from the caller, for call sites like
// BranchProtected and classifyMergeFailure that wired cmd.Stderr to their own
// buffer and so leave *exec.ExitError.Stderr empty.
func TestGhCommandErrText_StderrSurfaced(t *testing.T) {
	_, err := exec.Command("sh", "-c", "exit 1").Output()
	if err == nil {
		t.Fatal("want subprocess error, got nil")
	}

	got := ghCommandErrText("gh api branch protection", err, "hello")
	if got == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(got.Error(), "gh api branch protection") {
		t.Errorf("error should name the operation, got: %v", got)
	}
	if !strings.Contains(got.Error(), "hello") {
		t.Errorf("error should surface the supplied stderr, got: %v", got)
	}
	if !strings.Contains(got.Error(), "exit status") {
		t.Errorf("error should still carry the exit status, got: %v", got)
	}
	if !errors.Is(got, err) {
		t.Errorf("error should still wrap the original error, got: %v", got)
	}
}

func TestGhCommandErrText_EmptyStderrDegradesCleanly(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
	}{
		{"empty stderr", ""},
		{"whitespace-only stderr", "   \n\t "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := exec.Command("sh", "-c", "exit 1").Output()
			if err == nil {
				t.Fatal("want subprocess error, got nil")
			}

			got := ghCommandErrText("gh api branch protection", err, tc.stderr)
			if got == nil {
				t.Fatal("want error, got nil")
			}
			want := fmt.Sprintf("gh api branch protection: %s", err)
			if got.Error() != want {
				t.Errorf("error = %q, want %q (no dangling separator)", got.Error(), want)
			}
		})
	}
}

func TestGhCommandErrText_StderrTruncated(t *testing.T) {
	_, err := exec.Command("sh", "-c", "exit 1").Output()
	if err == nil {
		t.Fatal("want subprocess error, got nil")
	}

	stderr := strings.Repeat("x", 100000)
	got := ghCommandErrText("gh api branch protection", err, stderr)
	if got == nil {
		t.Fatal("want error, got nil")
	}
	if len(got.Error()) >= len(stderr) {
		t.Errorf("error message should be bounded well below the untruncated stderr size, got %d bytes", len(got.Error()))
	}
	if !strings.Contains(got.Error(), "truncated") {
		t.Errorf("truncation should be visible in the message, got a %d-byte message", len(got.Error()))
	}
}

// ghCommandErrText classifies rate limiting itself, so every call site routed
// through it, or through ghCommandErr, wraps forge.ErrRateLimit without doing
// any work of its own (issue #2865).
func TestGhCommandErrText_RateLimitedStderrWrapsErrRateLimit(t *testing.T) {
	_, err := exec.Command("sh", "-c", "exit 1").Output()
	if err == nil {
		t.Fatal("want subprocess error, got nil")
	}

	stderr := "API rate limit exceeded for installation ID 12345678."
	got := ghCommandErrText("gh issue list", err, stderr)
	if got == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(got, forge.ErrRateLimit) {
		t.Errorf("error should wrap forge.ErrRateLimit, got: %v", got)
	}
	if !strings.Contains(strings.ToLower(got.Error()), "rate limit") {
		t.Errorf("error message should name rate limiting, got: %q", got.Error())
	}
	if !strings.Contains(got.Error(), stderr) {
		t.Errorf("error should still surface gh's stderr, got: %q", got.Error())
	}
}

// ghCommandErr delegates to ghCommandErrText, so it inherits the same
// forge.ErrRateLimit wrapping.
func TestGhCommandErr_RateLimitedStderrWrapsErrRateLimit(t *testing.T) {
	_, err := exec.Command("sh", "-c", "printf 'You have exceeded a secondary rate limit and have been temporarily blocked from content creation.' 1>&2; exit 1").Output()
	if err == nil {
		t.Fatal("want subprocess error, got nil")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("want *exec.ExitError, got %T: %v", err, err)
	}

	got := ghCommandErr("gh issue list", err)
	if got == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(got, forge.ErrRateLimit) {
		t.Errorf("error should wrap forge.ErrRateLimit, got: %v", got)
	}
	if !strings.Contains(strings.ToLower(got.Error()), "rate limit") {
		t.Errorf("error message should name rate limiting, got: %q", got.Error())
	}
}
