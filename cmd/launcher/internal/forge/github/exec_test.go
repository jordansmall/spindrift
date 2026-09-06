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

// TestExecClient_DoesNotImplementLandingRecorder verifies the github adapter
// does not satisfy forge.LandingRecorder (ADR 0029): GitHub issues close
// through the forge's own auto-close mechanism, so there is no landing ref
// to persist. Only the local adapter implements this optional method.
func TestExecClient_DoesNotImplementLandingRecorder(t *testing.T) {
	var it forge.IssueTracker = NewExecClient("owner/repo", testLabels, "agent/issue-")
	if _, ok := it.(forge.LandingRecorder); ok {
		t.Error("ExecClient satisfies forge.LandingRecorder, want it hidden")
	}
}

// TestExecClient_ImplementsLabeledTracker verifies the github adapter
// satisfies forge.LabeledTracker — PickIssue's double-box guard (#1742)
// relies on this to skip a ListIssues round-trip for a state the tracker's
// label family leaves unmapped.
func TestExecClient_ImplementsLabeledTracker(t *testing.T) {
	var _ forge.LabeledTracker = NewExecClient("owner/repo", testLabels, "agent/issue-")
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
	out := testutil.CaptureStderr(t, func() {
		if _, err := c.DepsOf("10"); err != nil {
			t.Fatalf("DepsOf: %v", err)
		}
	})
	if !strings.Contains(out, "HTTP 404: Not Found") {
		t.Fatalf("fallback warning must contain gh's stderr; got: %q", out)
	}
}

// TestExecClient_DepsOf_NativeErrorEmptyStderrNoTrailingColon verifies that
// when the native dependencies API call fails without writing to stderr, the
// error has no dangling "exit status 1: " trailing colon-space.
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
	out := testutil.CaptureStderr(t, func() {
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
	want := []forge.Dependency{{ID: "4", Source: forge.DepSourceNative}}
	if !reflect.DeepEqual(deps, want) {
		t.Fatalf("DepsOf = %v, want %v", deps, want)
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
	if !reflect.DeepEqual(deps, want) {
		t.Fatalf("DepsOf = %v, want %v", deps, want)
	}
}

// TestExecClient_ImplementsBlockersLister verifies the github adapter
// satisfies forge.BlockersLister: GitHub's issue-dependencies API tracks
// blocked/blocking as a genuine bidirectional native relationship, so the
// reverse direction is one more native call, not a whole-backlog scan
// (issue #1744).
func TestExecClient_ImplementsBlockersLister(t *testing.T) {
	var _ forge.BlockersLister = NewExecClient("owner/repo", testLabels, "agent/issue-")
}

// TestExecClient_BlocksOf_ReturnsNativeBlocking verifies BlocksOf queries
// GitHub's native issue-dependencies "blocking" endpoint and reports every
// result as DepSourceNative — there is no body-text fallback, since no
// prose grammar declares a forward "blocks" relationship (issue #1744).
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

// TestExecClient_BlocksOf_PropagatesNativeError verifies BlocksOf surfaces
// a native lookup failure directly rather than degrading to some fallback
// — there is none to fall back to.
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

// TestExecClient_ImplementsPriorClaimStateReader verifies the github adapter
// satisfies forge.PriorClaimStateReader (issue #2477) — a terminal recover
// failure must not downgrade an already-successful issue, and this surface
// is how recover learns what the issue's terminal state was immediately
// before its most recent claim.
func TestExecClient_ImplementsPriorClaimStateReader(t *testing.T) {
	var _ forge.PriorClaimStateReader = NewExecClient("owner/repo", testLabels, "agent/issue-")
}

// TestExecClient_PriorClaimState_FindsComplete verifies PriorClaimState
// reads the issue timeline for the most recent "unlabeled" event naming a
// terminal label and reports it — here, the fake gh script stands in for
// `gh api .../timeline --jq '... | .label.name'`'s already-filtered output,
// one label name per line.
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

// TestExecClient_PriorClaimState_MostRecentWins verifies that when the
// timeline shows both terminal labels unlabeled (a Failed run later
// recovered into a Complete one, or vice versa), PriorClaimState reports
// the most recent one — the last matching line in the chronological
// (oldest-first) stream — not simply the first match found.
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

// TestExecClient_PriorClaimState_NoTerminalLabelReturnsNotFound verifies
// PriorClaimState reports ok=false when the timeline names no terminal
// label at all — e.g. a first-ever dispatch, with no prior claim to recall.
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

// TestExecClient_PriorClaimState_GenuineFailureSurfaced verifies
// PriorClaimState surfaces a genuine gh api failure rather than silently
// reporting not-found.
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

// TestExecClient_PriorClaimState_UsesTimelineEndpointPaginated verifies
// PriorClaimState queries the issue timeline endpoint with --paginate, so a
// long label history spanning multiple result pages is scanned in full
// rather than only its first page.
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

// TestExecClient_BranchExists_ExactMatch verifies BranchExists returns true
// when the matching-refs endpoint reports the exact ref.
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

// TestExecClient_BranchExists_RejectsPrefixMatch verifies BranchExists
// returns false when the matching-refs endpoint's prefix match only found a
// longer sibling branch ("agent/issue-10"), not the exact branch queried
// ("agent/issue-1") — matching-refs prefix-matches, so a naive
// non-empty-response check would wrongly report the shorter branch as
// existing too.
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

// TestExecClient_BranchExists_NoMatch verifies BranchExists returns false
// when the matching-refs endpoint reports no refs at all.
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

// TestExecClient_BranchExists_RejectsEmptyBranch verifies BranchExists
// refuses an empty branch without shelling out — an empty branch would
// otherwise query every ref under heads/ instead of a single branch.
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

// TestExecClient_BranchProtected_Protected verifies BranchProtected returns
// true when the branch-protection endpoint returns 200 with a JSON body.
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

// TestExecClient_BranchProtected_NotProtected verifies BranchProtected
// returns (false, nil) — a definitive, successful result, not an error —
// when both the classic endpoint 404s with GitHub's "Branch not protected"
// message and the ruleset endpoint reports no applicable rules.
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

// TestExecClient_BranchProtected_RulesetOnly verifies BranchProtected
// returns (true, nil) when the classic endpoint 404s "Branch not
// protected" but the branch is covered by a repository ruleset -- the
// mechanism README.md and SECURITY.md instruct operators to configure,
// which the classic branches/{branch}/protection endpoint never reports.
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

// TestExecClient_BranchProtected_DocumentedTokenFallsThroughOn403 verifies
// BranchProtected falls through to branchProtectedByRuleset when the
// classic endpoint fails with HTTP 403 rather than the "Branch not
// protected" 404 -- this project's own documented fine-grained PAT scope
// (Contents/Pull requests/Issues RW + Metadata R, no Administration: read)
// makes 403 the response the classic endpoint actually returns, so this is
// the single most common real configuration: a ruleset-protected branch
// under the documented token must be reported protected, not error out.
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

// TestExecClient_BranchProtected_HTTP403ZeroRuleset verifies BranchProtected
// returns a non-nil error -- never a definitive false -- when the classic
// endpoint 403s and the ruleset probe reports zero applicable rules,
// covering both reasons a 403 shows up here:
//
//   - The documented fine-grained PAT scope (no Administration: read), where
//     the classic mechanism was never actually read, so a zero ruleset count
//     does not rule out a classic-only protection rule this token simply
//     can't see -- exactly the configuration docs/reference.md instructs
//     operators to set up (a classic rule on main, no ruleset). Reporting
//     (false, nil) here would be a false required failure under
//     MERGE_MODE=immediate/auto.
//   - A rate-limited 403, where the error must additionally be errors.Is
//     forge.ErrRateLimit -- so a caller can back off and retry instead of
//     treating this as a definitive "can't determine" result. Routing this
//     path's error through ghCommandErrText (the same helper every other
//     gh-failure site uses) is what buys this classification.
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

// TestExecClient_BranchProtected_RulesetProbeFailure verifies BranchProtected
// surfaces a non-nil error when the classic endpoint's "Branch not
// protected" 404 falls through to the ruleset probe and that probe itself
// fails (network, insufficient token scope, etc.) -- never a false "not
// protected".
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

// TestExecClient_BranchProtected_ProbeFailure verifies BranchProtected
// surfaces a non-nil error — never a false "not protected" — when the
// classic endpoint's 403 falls through to the ruleset probe (as the
// documented token's scope makes it) and that ruleset probe itself is
// unstubbed here and fails: a genuine probe failure, not resolved to a
// false "not protected".
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

// TestExecClient_BranchProtected_GenericNotFound verifies BranchProtected
// surfaces a non-nil error -- not a false "not protected" -- for a 404 that
// isn't GitHub's "Branch not protected" body, e.g. a base branch that hasn't
// been pushed yet, a typo'd branch name, or a repo the token can't see: all
// return a bare 404 that must not be conflated with a definitive "no
// protection rule" answer. It also verifies the fallback error routes
// through ghCommandErrText (issue #2864): the classic endpoint's captured
// stderr text ends up in the returned error's message.
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

// TestExecClient_BranchProtected_GenericFailureEmptyStderr verifies
// BranchProtected's generic-failure fallback degrades cleanly -- no dangling
// ": " suffix -- when the classic endpoint fails without writing anything to
// stderr (issue #2864: the bug class this ticket targets).
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

// TestExecClient_BranchProtected_RejectsEmptyBranch verifies BranchProtected
// refuses an empty branch without shelling out, mirroring
// TestExecClient_BranchExists_RejectsEmptyBranch.
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

// TestExecClient_ImplementsBranchProtectionForge verifies the github adapter
// satisfies forge.BranchProtectionForge.
func TestExecClient_ImplementsBranchProtectionForge(t *testing.T) {
	var _ forge.BranchProtectionForge = NewExecClient("owner/repo", testLabels, "agent/issue-")
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

// TestExecClient_Issue_ErrorSurfacesStderr verifies that when `gh issue
// view` exits non-zero with a diagnostic on stderr, Issue's returned error
// includes that stderr text (issue #2864).
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

// TestExecClient_ListOpenIssues_ErrorSurfacesStderr verifies that when `gh
// issue list` exits non-zero with a diagnostic on stderr, ListOpenIssues's
// returned error includes that stderr text, matching ListIssues's own
// stderr-surfacing behavior (issue #2864).
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

// TestExecClient_ListIssues_ErrorSurfacesStderr verifies that when `gh issue
// list` exits non-zero with a diagnostic on stderr, ListIssues's returned
// error includes that stderr text — the re-discover loop's queryOpenIssues
// path (main.go) otherwise sees only a bare "exit status 1".
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

// TestExecClient_ListIssues_ErrorEmptyStderrNoTrailingColon verifies that
// when `gh issue list` exits non-zero without writing to stderr, the
// returned error degrades cleanly — no dangling "exit status 1: " trailing
// colon-space.
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

// TestExecClient_IssueLabels_ErrorSurfacesStderr verifies that when `gh
// issue view` exits non-zero with a diagnostic on stderr, issueLabels's
// returned error includes that stderr text (issue #2864).
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

// TestExecClient_CompleteVerdict_MissingInProgressErrorsWithoutEditing
// verifies that CompleteVerdict refuses to swap labels on an issue that does
// not currently carry InProgress — the double-dispatch guard from issue
// #701 — instead of silently leaving the issue multi-labeled.
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

// TestExecClient_CompleteVerdict_InProgressPresentEditsIssue verifies the
// happy path: when the issue does carry InProgress, CompleteVerdict shells
// out to swap it for the verdict's terminal label as before.
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

	// Exactly one edit call: view (call-00.txt) + edit (call-01.txt), no more.
	calls, _ := filepath.Glob(filepath.Join(dir, "call-*.txt"))
	if len(calls) != 2 {
		t.Errorf("gh call count = %d, want 2 (view + exactly one edit)", len(calls))
	}
}

// TestExecClient_TransitionState_GenuineFailureSurfaced verifies that when
// `gh issue edit` exits non-zero with a diagnostic on stderr, TransitionState's
// returned error includes that stderr text (issue #2864).
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

// TestExecClient_CompleteVerdict_GenuineEditFailureSurfaced verifies that
// when the InProgress precondition is satisfied but the subsequent `gh issue
// edit` call itself fails, CompleteVerdict's returned error includes gh's
// stderr text (issue #2864).
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

// TestExecClient_TransitionState_ClaimStripsStaleFailedLabel verifies a claim
// (Dispatchable -> InProgress) removes a stale agent-failed label left behind
// by a prior run, not just the from-state label — matching the dispatch
// workflow's claim-remove-labels set (#1985).
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

// TestExecClient_TransitionState_ClaimStripsStaleCompleteLabel verifies a
// claim (Dispatchable -> InProgress) also removes a stale agent-complete
// label — the re-research/re-trigger-after-complete case (#1985).
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

// TestExecClient_TransitionState_NonClaimTransitionUnchanged verifies a
// transition that does not land on InProgress (e.g. InProgress -> Complete)
// still emits exactly the prior one --add-label/--remove-label pair — the
// stale-terminal-label strip is a claim-only behavior (#1985).
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

// TestExecClient_TransitionState_NormalClaimUnchanged verifies criterion 4 of
// #1985 end to end: a claim on an issue with no stale terminal label present
// still ends up with exactly agent-in-progress — the stale-label strip must
// not perturb the ordinary claim path. Uses the stateful fakeGHState harness
// (contract_test.go) rather than prependFakeGH so it can assert the
// resulting label set, not just the argv of the edit call.
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

// TestExecClient_TransitionState_ClaimRemoveLabelsMatchDispatchWorkflow
// guards the parity a bare code comment can't enforce: every label a claim
// (Dispatchable -> InProgress) removes must be in agent-dispatch.yml's own
// claim-remove-labels list, read straight from the workflow file, so the two
// can never silently drift apart (#1985). The reverse isn't asserted:
// agent-trigger and agent-recover are pure GitHub Actions trigger gestures
// with no forge.DispatchState equivalent, so they're outside what the Go
// claim can strip.
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
	if errors.Is(err, forge.ErrRateLimit) {
		t.Fatalf("error must not be errors.Is forge.ErrRateLimit; got: %v", err)
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

// TestFailureDetail_GraphQLFailureSurfacesStderr verifies that when `gh api
// graphql` fails, the error FailureDetail returns includes gh's actual
// stderr text, routed through ghCommandErr (issue #2864).
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
// forge.MaxFailureDetailBytes truncation.
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

// TestClassifyMergeFailure_TransientStderrWrapsErrMergeTransient verifies that
// a non-conflict gh pr merge failure whose stderr indicates a transient
// failure (e.g. a 502 from GitHub) is wrapped so callers can detect it via
// errors.Is(err, forge.ErrMergeTransient), without needing to query the PR's
// mergeable state.
func TestClassifyMergeFailure_TransientStderrWrapsErrMergeTransient(t *testing.T) {
	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	mergeErr := errors.New("exit status 1")
	err := c.classifyMergeFailure("https://github.com/owner/repo/pull/42", mergeErr, "HTTP 502: Bad Gateway (https://api.github.com/graphql)\n")
	if !errors.Is(err, forge.ErrMergeTransient) {
		t.Fatalf("want forge.ErrMergeTransient, got: %v", err)
	}
}

// TestClassifyMergeFailure_NonTransientNonConflictStderrDoesNotWrapErrMergeTransient
// verifies that a genuine non-retryable, non-conflict gh pr merge failure
// (e.g. an auth error) is not misclassified as forge.ErrMergeTransient.
func TestClassifyMergeFailure_NonTransientNonConflictStderrDoesNotWrapErrMergeTransient(t *testing.T) {
	c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-")
	mergeErr := errors.New("exit status 1")
	err := c.classifyMergeFailure("https://github.com/owner/repo/pull/42", mergeErr, "HTTP 401: Bad credentials (https://api.github.com/graphql)\n")
	if errors.Is(err, forge.ErrMergeTransient) {
		t.Fatalf("non-transient auth failure must not classify as forge.ErrMergeTransient, got: %v", err)
	}
}

// TestMarkReady_AlreadyReadyIsIdempotentNoOp verifies that MarkReady on a PR
// gh already reports as ready for review is treated as success (issue
// #1651). This mirrors gh's actual behavior: `gh pr ready` on an
// already-ready PR prints a notice to stderr but exits 0 — MarkReady must
// not turn that stderr notice into a spurious error.
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

// TestRateLimitMarkers_AreLowercase verifies the MatchesAnyMarker
// precondition — markers must already be lowercase — since rateLimitMarkers
// is fed to that shared helper and a mixed-case marker would silently never
// match.
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

// TestIsRateLimited verifies that isRateLimited recognizes both GitHub's
// primary hourly-quota phrasing and its secondary/abuse-detection phrasings
// (issue #2865), and that it does not misclassify unrelated gh failures
// (auth, not-found, network) as rate limiting.
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

// TestMarkReady_GenuineFailureSurfaced verifies that a real gh pr ready
// failure is returned as an error rather than swallowed.
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

// TestMarkDraft_AlreadyDraftIsIdempotentNoOp verifies that MarkDraft on a PR
// gh already reports as a draft is treated as success — the inverse of
// TestMarkReady_AlreadyReadyIsIdempotentNoOp, mirroring gh's own idempotent
// `gh pr ready --undo` behavior.
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

// TestMarkDraft_GenuineFailureSurfaced verifies that a real gh pr ready
// --undo failure is returned as an error rather than swallowed.
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

// TestExecClient_CloseMergedIssue_AlreadyClosedIsNoOp verifies that
// CloseMergedIssue is a no-op — and never shells out to `gh issue close` —
// when the issue is already closed (issue #1892: GitHub's own merged-PR
// auto-close already ran, e.g. the PR body carried a Closes #<N> keyword). A
// `gh issue close` call here would be a bug: the fake script exits 1 if it
// ever sees one.
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

// TestExecClient_CloseMergedIssue_ClosesOpenIssue verifies that
// CloseMergedIssue shells out to `gh issue close` when the issue is still
// open — the case GitHub's own auto-close missed because the PR body omitted
// (or reworded) the Closes #<N> keyword.
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

// TestExecClient_CloseMergedIssue_GenuineFailureSurfaced verifies that a
// real gh issue close failure on an open issue is returned as an error
// rather than swallowed as if it were the idempotent already-closed case.
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

// TestExecClient_CloseMergedIssue_EmptyStderrNoTrailingColon verifies that
// when `gh issue close` exits non-zero without writing to stderr, the
// returned error has no dangling "exit status 1: " trailing colon-space
// (issue #2864).
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

// TestExecClient_Comment_GenuineFailureSurfaced verifies that when `gh issue
// comment` exits non-zero with a diagnostic on stderr, Comment's returned
// error includes that stderr text (issue #2864).
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

// TestExecClient_ImplementsHostPostedIssueFiler verifies the github adapter
// satisfies forge.HostPostedIssueFiler (issue #2028) — the read-only
// capability gate's issue-filing axis, closed by this adapter method.
func TestExecClient_ImplementsHostPostedIssueFiler(t *testing.T) {
	var _ forge.HostPostedIssueFiler = NewExecClient("owner/repo", testLabels, "agent/issue-")
}

// TestExecClient_ImplementsGithubTracker verifies the github adapter
// satisfies forge.GithubTracker (issue #2341) — the positive marker
// settle's ensureClosesReference uses to scope its "Closes #N" injection to
// GitHub-hosted PRs only, never forgejo (a foreign issue-number namespace).
func TestExecClient_ImplementsGithubTracker(t *testing.T) {
	var _ forge.GithubTracker = NewExecClient("owner/repo", testLabels, "agent/issue-")
}

// TestExecClient_ImplementsMergeCloser verifies the github adapter satisfies
// forge.MergeCloser (issue #1892) — settle's deterministic post-merge close
// backstop. It must NOT satisfy forge.IssueCloser (that surface is reserved
// for the local adapter's reconcile-owned closed: axis; a github adapter
// implementing it too would let an ISSUE_TRACKER=local + CODE_FORGE=github
// pairing close a local issue through the wrong path).
func TestExecClient_ImplementsMergeCloser(t *testing.T) {
	var _ forge.MergeCloser = NewExecClient("owner/repo", testLabels, "agent/issue-")
	if _, ok := any(NewExecClient("owner/repo", testLabels, "agent/issue-")).(forge.IssueCloser); ok {
		t.Error("ExecClient satisfies forge.IssueCloser, want it hidden")
	}
}

// TestExecClient_PostIssue_ReturnsURL verifies PostIssue shells out to `gh
// issue create` and returns the created issue's URL parsed from stdout.
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

// TestExecClient_PostIssue_ArgsCarryTitleBodyAndOneLabelFlagPerLabel
// verifies PostIssue passes title, body, and one --label per label to `gh
// issue create` against the adapter's own repo — labels are exactly what the
// caller passed, and there is no repo argument for a payload to redirect
// (issue #1949's do-not-trust-the-agent-target invariant).
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

// TestExecClient_PostIssue_GenuineFailureSurfaced verifies a non-nil `gh
// issue create` failure is returned as a wrapped error naming the operation,
// parity with Comment.
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

// TestExecClient_PostIssue_ErrorEmptyStderrNoTrailingColon verifies that
// when `gh issue create` exits non-zero without writing to stderr, the
// returned error has no dangling "exit status 1: " trailing colon-space —
// the bug TestGhCommandErr_EmptyStderrDegradesCleanly's doc comment calls
// out, previously present because PostIssue unconditionally appended
// stderr.String() even when empty (issue #2864).
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

// TestGhCommandErr_StderrSurfaced verifies ghCommandErr folds a genuine
// *exec.ExitError's captured Stderr (as exec.Cmd.Output populates it whenever
// Stderr was left nil, e.g. ListIssues/Issue/issueLabels today) into the
// returned error's message, alongside the exit status from %w.
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

// TestGhCommandErr_EmptyStderrDegradesCleanly verifies ghCommandErr never
// appends a dangling ": " or empty suffix when the ExitError's Stderr is
// empty or whitespace-only — the exact bug PostIssue exhibits today
// (exec_issues.go), where the stderr suffix is appended unconditionally.
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

// TestGhCommandErr_NonExitError verifies ghCommandErr handles a non-ExitError
// failure (e.g. *exec.Error when the gh binary is missing from PATH)
// gracefully — no panic, and the description is still wrapped in front of
// the original error.
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

// TestGhCommandErr_StderrTruncated verifies ghCommandErr bounds how much of
// gh's captured stderr it folds into the error message: a pathological
// stderr dump is truncated to a reasonable cap, with the truncation made
// visible in the message rather than silently swallowed or left unbounded.
func TestGhCommandErr_StderrTruncated(t *testing.T) {
	_, err := exec.Command("sh", "-c", "yes x | head -c 100000 1>&2; exit 1").Output()
	if err == nil {
		t.Fatal("want subprocess error, got nil")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("want *exec.ExitError, got %T: %v", err, err)
	}
	// exec.Cmd itself caps captured stderr around 64KiB (prefixSuffixSaver),
	// well above ghCommandErr's own cap — plenty to exercise truncation.
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

// TestGhCommandErrText_StderrSurfaced verifies ghCommandErrText folds a
// caller-supplied stderr string (captured by a call site that wired
// cmd.Stderr to its own buffer, e.g. BranchProtected/classifyMergeFailure)
// into the returned error's message, alongside the exit status from %w.
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

// TestGhCommandErrText_EmptyStderrDegradesCleanly verifies ghCommandErrText
// never appends a dangling ": " or empty suffix when the supplied stderr
// text is empty or whitespace-only, mirroring
// TestGhCommandErr_EmptyStderrDegradesCleanly.
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

// TestGhCommandErrText_StderrTruncated verifies ghCommandErrText bounds how
// much of the supplied stderr text it folds into the error message, the same
// cap ghCommandErr applies, with the truncation made visible in the message
// rather than silently swallowed or left unbounded.
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

// TestGhCommandErrText_RateLimitedStderrWrapsErrRateLimit verifies that when
// the supplied stderr text classifies as rate limiting (isRateLimited),
// ghCommandErrText's returned error additionally wraps forge.ErrRateLimit —
// centralizing the classification here means every call site routed through
// ghCommandErrText/ghCommandErr picks this up for free (issue #2865).
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

// TestGhCommandErr_RateLimitedStderrWrapsErrRateLimit verifies the
// *exec.ExitError-based sibling ghCommandErr inherits the same
// forge.ErrRateLimit wrapping, since it delegates to ghCommandErrText.
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
