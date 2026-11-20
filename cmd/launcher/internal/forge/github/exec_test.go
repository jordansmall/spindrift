package github

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// testLabels mirrors the lifecycle labels in lib/env-schema.nix (issue #460),
// so no test has to restate the label strings.
var testLabels = forge.DispatchLabels{
	Dispatchable: "ready-for-agent",
	InProgress:   "agent-in-progress",
	Complete:     "agent-complete",
	Failed:       "agent-failed",
	Ambiguous:    "agent-ambiguous-spec",
}

// The github forge opens PRs and watches CI, unlike the push-only git adapter.
func TestExecClient_ImplementsPRForge(t *testing.T) {
	var _ forge.PRForge = NewExecClient("owner/repo", testLabels)
}

// GitHub issues close through the forge's own auto-close mechanism (ADR 0029),
// so there is no landing ref to persist. Only the local adapter implements
// forge.LandingRecorder.
func TestExecClient_DoesNotImplementLandingRecorder(t *testing.T) {
	var it forge.IssueTracker = NewExecClient("owner/repo", testLabels)
	if _, ok := it.(forge.LandingRecorder); ok {
		t.Error("ExecClient satisfies forge.LandingRecorder, want it hidden")
	}
}

// PickIssue's double-box guard (#1742) relies on forge.LabeledTracker to skip a
// ListIssues round-trip for a state the tracker's label family leaves unmapped.
func TestExecClient_ImplementsLabeledTracker(t *testing.T) {
	var _ forge.LabeledTracker = NewExecClient("owner/repo", testLabels)
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

// fakeGHClaimPrecondition scripts the `gh issue view --json labels` call a
// claim (to == InProgress) now issues before its edit, the already-claimed
// precondition check (#3887). Tests here only care about the edit call's
// argv, so this always reports no labels, i.e. never already claimed.
const fakeGHClaimPrecondition = `case "$*" in
*"issue view"*)
	printf '{"labels":[]}'
	;;
esac`

func TestExecClient_DepsOf_NativeErrorEmptyStderrNoTrailingColon(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*dependencies/blocked_by*)
	exit 1
	;;
*"issue view"*)
	printf '{"number":10,"title":"t","body":"blocked by #9","state":"OPEN","labels":[]}'
	;;
esac`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
	_, err := c.NativeDepsOf("10")
	if err == nil {
		t.Fatal("NativeDepsOf: want error, got nil")
	}
	if strings.HasSuffix(err.Error(), ": ") {
		t.Fatalf("NativeDepsOf error must not have a trailing colon-space; got: %q", err.Error())
	}
}

// GitHub's issue-dependencies API tracks blocked/blocking as a bidirectional
// relationship, so the reverse direction is one more native call rather than a
// whole-backlog scan (issue #1744).
func TestExecClient_ImplementsBlockersLister(t *testing.T) {
	var _ forge.BlockersLister = NewExecClient("owner/repo", testLabels)
}

// A terminal recover failure must not downgrade an already-successful issue.
// forge.PriorClaimStateReader is how recover learns the issue's terminal state
// immediately before its most recent claim (issue #2477).
func TestExecClient_ImplementsPriorClaimStateReader(t *testing.T) {
	var _ forge.PriorClaimStateReader = NewExecClient("owner/repo", testLabels)
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

			c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
	if _, err := c.BranchProtected(""); err == nil {
		t.Error("BranchProtected(\"\"): want error, got nil")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "call-*.txt")); len(matches) != 0 {
		t.Errorf("want no gh invocation for an empty branch, got %d", len(matches))
	}
}

func TestExecClient_ImplementsBranchProtectionForge(t *testing.T) {
	var _ forge.BranchProtectionForge = NewExecClient("owner/repo", testLabels)
}

func TestExecClient_ListIssues_ErrorEmptyStderrNoTrailingColon(t *testing.T) {
	prependFakeGH(t, `case "$*" in
*"issue list"*)
	exit 1
	;;
esac`)

	c := NewExecClient("owner/repo", testLabels)
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", testLabels, WithVerdictLabels(forge.ResearchVerdictLabels()))
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

	c := NewExecClient("owner/repo", testLabels, WithVerdictLabels(forge.ResearchVerdictLabels()))
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

	c := NewExecClient("owner/repo", testLabels)
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

	c := NewExecClient("owner/repo", testLabels, WithVerdictLabels(forge.ResearchVerdictLabels()))
	err := c.CompleteVerdict("10", forge.Recommend)
	if err == nil {
		t.Fatal("CompleteVerdict: want error, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("CompleteVerdict error must contain gh's stderr; got: %q", err.Error())
	}
}

// A claim removes each stale terminal label a prior run left behind, not just
// the from-state label, matching the dispatch workflow's claim-remove-labels
// set (#1985): agent-failed, agent-complete (the re-research or
// re-trigger-after-complete case), and agent-ambiguous-spec.
func TestExecClient_TransitionState_ClaimStripsStaleLabels(t *testing.T) {
	for _, label := range []string{"agent-failed", "agent-complete", "agent-ambiguous-spec"} {
		t.Run(label, func(t *testing.T) {
			dir := prependFakeGH(t, fakeGHClaimPrecondition)

			c := NewExecClient("owner/repo", testLabels)
			if err := c.TransitionState("10", forge.Dispatchable, forge.InProgress); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// call-00.txt is the precondition's `gh issue view`; call-01.txt is the edit.
			raw, err := os.ReadFile(filepath.Join(dir, "call-01.txt"))
			if err != nil {
				t.Fatalf("call-01.txt (gh issue edit) not written: %v", err)
			}
			argv := string(raw)
			if !strings.Contains(argv, "--remove-label\n"+label) {
				t.Errorf("argv = %q, want --remove-label %s", argv, label)
			}
		})
	}
}

// The stale-terminal-label strip is claim-only (bar a landing clearing a stale
// Failed, see CompleteStripsStaleFailed below), so a transition that does not
// land on InProgress still emits exactly one --add-label/--remove-label pair
// (#1985).
func TestExecClient_TransitionState_NonClaimTransitionUnchanged(t *testing.T) {
	dir := prependFakeGH(t, "")

	c := NewExecClient("owner/repo", testLabels)
	if err := c.TransitionState("10", forge.InProgress, forge.Failed); err != nil {
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

// A landing (to == Complete) also removes a stale agent-failed, so a recovered
// issue parked agent-failed ends wearing only agent-complete (#4651).
func TestExecClient_TransitionState_CompleteStripsStaleFailed(t *testing.T) {
	h := newGithubHarness(t)
	h.SeedIssue(forge.Issue{Number: "56", Title: "parked", Labels: []string{"agent-failed"}})

	if err := h.Tracker().TransitionState("56", forge.InProgress, forge.Complete); err != nil {
		t.Fatalf("TransitionState: %v", err)
	}

	if got := h.labels(t, "56"); len(got) != 1 || got[0] != "agent-complete" {
		t.Errorf("labels = %v, want exactly [agent-complete]", got)
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

	if got := h.labels(t, "55"); len(got) != 1 || got[0] != "agent-in-progress" {
		t.Errorf("labels = %v, want exactly [agent-in-progress]", got)
	}
}

// Every label a claim removes must appear in agent-dispatch.yml's own
// claim-remove-labels list, read straight from the workflow file, so the two
// cannot drift apart (#1985). The reverse is not asserted: agent-trigger and
// agent-recover are GitHub Actions trigger gestures with no forge.DispatchState
// equivalent, so the Go claim cannot strip them.
func TestExecClient_TransitionState_TransitionRemoveLabelsMatchDispatchWorkflow(t *testing.T) {
	workflowSet, rawValue := forgetest.ParseWorkflowRemoveLabelSet(t,
		filepath.Join("..", "..", "..", "..", "..", ".github", "workflows", "agent-dispatch.yml"),
		"claim-remove-labels")

	dir := prependFakeGH(t, fakeGHClaimPrecondition)
	c := NewExecClient("owner/repo", testLabels)
	if err := c.TransitionState("10", forge.Dispatchable, forge.InProgress); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// call-00.txt is the precondition's `gh issue view`; call-01.txt is the edit.
	callRaw, err := os.ReadFile(filepath.Join(dir, "call-01.txt"))
	if err != nil {
		t.Fatalf("call-01.txt (gh issue edit) not written: %v", err)
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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
		got := renderFailureDetail(contexts, nil)
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
		if got := renderFailureDetail(contexts, nil); got != "" {
			t.Fatalf("want empty string, got %q", got)
		}
	})

	t.Run("truncates to maxFailureDetailBytes", func(t *testing.T) {
		contexts := []failureDetailContext{
			{TypeName: "CheckRun", Name: "huge", Conclusion: "FAILURE", Summary: strings.Repeat("x", forge.MaxFailureDetailBytes*2)},
		}
		got := renderFailureDetail(contexts, nil)
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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
	c := NewExecClient("owner/repo", forge.DispatchLabels{})
	mergeErr := errors.New("exit status 1")
	err := c.classifyMergeFailure("https://github.com/owner/repo/pull/42", mergeErr, "HTTP 502: Bad Gateway (https://api.github.com/graphql)\n")
	if !errors.Is(err, forge.ErrMergeTransient) {
		t.Fatalf("want forge.ErrMergeTransient, got: %v", err)
	}
}

// An auth failure is not retryable and must not classify as transient.
func TestClassifyMergeFailure_NonTransientNonConflictStderrDoesNotWrapErrMergeTransient(t *testing.T) {
	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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
	if !strings.Contains(string(closeCall), "--reason\ncompleted") {
		t.Errorf("last gh call = %q, want explicit `--reason completed` so the close is never \"not planned\"", closeCall)
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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
	var _ forge.HostPostedIssueFiler = NewExecClient("owner/repo", testLabels)
}

// forge.MergeCloser is settle's deterministic post-merge close backstop (issue
// #1892). forge.IssueCloser stays reserved for the local adapter's
// reconcile-owned "closed:" axis. A github adapter implementing it too would
// let an ISSUE_TRACKER=local plus CODE_FORGE=github pairing close a local
// issue through the wrong path.
func TestExecClient_ImplementsMergeCloser(t *testing.T) {
	var _ forge.MergeCloser = NewExecClient("owner/repo", testLabels)
	if _, ok := any(NewExecClient("owner/repo", testLabels)).(forge.IssueCloser); ok {
		t.Error("ExecClient satisfies forge.IssueCloser, want it hidden")
	}
}

func TestExecClient_PostIssue_ReturnsURL(t *testing.T) {
	prependFakeGH(t, `if [ "$1" = "issue" ] && [ "$2" = "create" ]; then
  echo "https://github.com/owner/repo/issues/99"
  exit 0
fi
`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
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

// backlogDedupIndex's capability path (issue #3609 review) needs bodies and
// the newest issues, not ListOpenIssues's unlabelled/oldest-first page.
func TestExecClient_ImplementsLabeledBacklogLister(t *testing.T) {
	var _ forge.LabeledBacklogLister = NewExecClient("owner/repo", testLabels)
}

// When every label's gh call fails, the failure has to surface: it is the
// signal backlogDedupIndex uses to fall back to intra-run-only dedup instead
// of silently treating an empty index as "nothing to dedup against".
func TestExecClient_ListIssuesWithLabels_AllFailuresReturnsError(t *testing.T) {
	prependFakeGH(t, `echo 'boom' >&2
exit 1`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
	_, err := c.ListIssuesWithLabels(forge.IssueOpen, []string{"agent-review-finding", "agent-research-finding"})
	if err == nil {
		t.Fatal("ListIssuesWithLabels: want error when every label fails, got nil")
	}
}

// A state that isn't OPEN or CLOSED (e.g. forge.IssueMerged, an issue-view-only
// state) must error before any gh process runs, not silently pass an
// unrecognized value to --state.
func TestExecClient_ListIssuesWithLabels_UnsupportedStateErrorsWithoutInvokingGH(t *testing.T) {
	dir := prependFakeGH(t, `echo 'should not run' >&2; exit 1`)

	c := NewExecClient("owner/repo", forge.DispatchLabels{})
	_, err := c.ListIssuesWithLabels(forge.IssueMerged, []string{"agent-review-finding"})
	if err == nil {
		t.Fatal("ListIssuesWithLabels(IssueMerged): want error, got nil")
	}

	calls, err2 := filepath.Glob(filepath.Join(dir, "call-*.txt"))
	if err2 != nil {
		t.Fatal(err2)
	}
	if len(calls) != 0 {
		t.Fatalf("want no gh invocation for an unsupported state, got %d", len(calls))
	}
}

// AddLabels issues one gh issue edit call carrying a repeated --add-label
// flag per label, mirroring TransitionState's exec style (issue #4074).
func TestExecClient_AddLabels_OneCallRepeatedFlags(t *testing.T) {
	dir := prependFakeGH(t, "")

	c := NewExecClient("owner/repo", testLabels)
	if err := c.AddLabels("10", []string{"agent-butler-finding", "agent-butler-patch"}); err != nil {
		t.Fatalf("AddLabels: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "call-") {
			calls++
		}
	}
	if calls != 1 {
		t.Fatalf("gh invoked %d times, want exactly 1", calls)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "call-00.txt"))
	if err != nil {
		t.Fatal(err)
	}
	argv := string(raw)
	if !strings.Contains(argv, "issue\nedit\n10\n--repo\nowner/repo\n--add-label\nagent-butler-finding\n--add-label\nagent-butler-patch") {
		t.Errorf("argv = %q, want one edit call with two --add-label flags", argv)
	}
}

// gh's stderr must reach the returned error, matching TransitionState's
// GenuineFailureSurfaced coverage.
func TestExecClient_AddLabels_GenuineFailureSurfaced(t *testing.T) {
	prependFakeGH(t, `printf 'HTTP 403: Resource not accessible by integration\n' >&2
exit 1`)

	c := NewExecClient("owner/repo", testLabels)
	err := c.AddLabels("10", []string{"ready-for-agent"})
	if err == nil {
		t.Fatal("AddLabels: want error, got nil")
	}
	if !strings.Contains(err.Error(), "403") {
		t.Fatalf("AddLabels error must contain gh's stderr; got: %q", err.Error())
	}
}

func TestExecClient_ImplementsIssueSearcher(t *testing.T) {
	var _ forge.IssueSearcher = NewExecClient("owner/repo", testLabels)
}

func TestExecClient_SearchIssues_RejectsUnsearchableInput(t *testing.T) {
	dir := prependFakeGH(t, `printf '[]'`)
	c := NewExecClient("owner/repo", forge.DispatchLabels{})
	for name, text := range map[string]string{
		"double quote": `a" repo:evil/x "b`,
		"backslash":    `a\ repo:evil/x`,
		"newline":      "a\nrepo:evil/x",
		"tab":          "a\tb",
		"nul":          "a\x00b",
		"empty":        "   ",
	} {
		if _, err := c.SearchIssues(text, forge.IssueOpen, "", 3); err == nil {
			t.Errorf("%s: SearchIssues(%q) = nil error, want rejection", name, text)
		}
	}
	if _, err := c.SearchIssues("x", forge.IssueState("MERGED"), "", 3); err == nil {
		t.Error("unsupported state: want an error")
	}
	if calls, _ := filepath.Glob(filepath.Join(dir, "call-*.txt")); len(calls) != 0 {
		t.Errorf("gh was invoked %d times for rejected input, want 0", len(calls))
	}
}
