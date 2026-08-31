package settle

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/testutil"
)

// stalePRLabel matches a genuine stale pr= field (issue #892) without
// tripping on a benign substring like expr= or repr= inside free-text
// note/error interpolations.
var stalePRLabel = regexp.MustCompile(`\bpr=`)

// TestSettle_PostsUsageComment_Blocked verifies that Settle posts d's usage
// report as a comment when the outcome is "blocked". Uses a github-shaped
// tracker (AsNoLandingRecorder): a local tracker's blocked path posts an
// additional note comment (TestSettle_LocalForge_BlockedPostsNoteAsComment),
// which is out of scope for this usage-comment-specific assertion.
func TestSettle_PostsUsageComment_Blocked(t *testing.T) {
	const issNum = "42"
	const prURL = "https://github.com/owner/repo/pull/99"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	d.UsageReportBody = "## Run usage\n\ncost: 0.25"
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
	}

	s := newTestSettle(baseConfig(), fc.AsNoLandingRecorder(), fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(fc.CommentCalls))
	}
	if fc.CommentCalls[0].Body != d.UsageReportBody {
		t.Errorf("comment body: got %q, want %q", fc.CommentCalls[0].Body, d.UsageReportBody)
	}
}

// TestSettle_BlockedOutcome_DemotesToFailed verifies that a status=blocked
// outcome (including the synthetic backstop's) swaps agent-in-progress to
// agent-failed so the issue lands in the human-triage queue instead of
// looking in-flight forever (issue #1605, observed on #1542).
func TestSettle_BlockedOutcome_DemotesToFailed(t *testing.T) {
	const issNum = "42"
	const prURL = "https://github.com/owner/repo/pull/99"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(d, issNum, 0, result)

	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("blocked outcome must demote to agent-failed; got labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-in-progress") {
		t.Errorf("blocked outcome must remove agent-in-progress; got labels=%v", iss.Labels)
	}
}

// TestSettle_ConsoleUsesLandingLabel verifies that Settle's operator-report
// console print uses the landing= label (matching the wire grammar's
// o.Landing field name), not the stale pr= label the value may not even be
// a PR (issue #655).
func TestSettle_ConsoleUsesLandingLabel(t *testing.T) {
	const issNum = "42"
	const prURL = "https://github.com/owner/repo/pull/99"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing; expr=1 mismatch"},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	out := testutil.CaptureStdout(t, func() {
		s.Settle(d, issNum, 0, result)
	})

	if !strings.Contains(out, "landing="+prURL) {
		t.Errorf("console output must print landing=%s; got: %q", prURL, out)
	}
	if stalePRLabel.MatchString(out) {
		t.Errorf("console output must not use the stale pr= label; got: %q", out)
	}
}

// TestSettle_UsageMissing_NoCrash verifies that Settle still posts whatever
// UsageReport returns (including its "unavailable" fallback body) without
// crashing. Uses a github-shaped tracker (AsNoLandingRecorder) for the same
// reason as TestSettle_PostsUsageComment_Blocked.
func TestSettle_UsageMissing_NoCrash(t *testing.T) {
	const issNum = "7"
	const prURL = "https://github.com/owner/repo/pull/7"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	d.UsageReportBody = "## Run usage\n\nModel: `unknown`\n\nUsage data unavailable (no result event in log)."
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "no result"},
		},
	}

	s := newTestSettle(baseConfig(), fc.AsNoLandingRecorder(), fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted even without usage data, got %d", len(fc.CommentCalls))
	}
	if !strings.Contains(fc.CommentCalls[0].Body, "unavailable") {
		t.Errorf("comment should say unavailable when usage missing; got: %q", fc.CommentCalls[0].Body)
	}
}

// TestSettle_PostsUsageComment_Ready verifies that Settle posts the usage
// comment after driving selfHeal for a "ready" outcome too.
func TestSettle_PostsUsageComment_Ready(t *testing.T) {
	const issNum = "55"
	const prURL = "https://github.com/owner/repo/pull/55"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	d := dispatch.NewFake()
	d.UsageReportBody = "## Run usage\n\nbreakdown included"
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "ready", Note: "ok"},
		},
	}

	c := baseConfig()
	s := newTestSettle(c, fc, fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(fc.CommentCalls))
	}
	if fc.CommentCalls[0].Body != d.UsageReportBody {
		t.Errorf("comment body: got %q, want %q", fc.CommentCalls[0].Body, d.UsageReportBody)
	}
}

// TestSettle_ImmediateMergeClosesIssue verifies that a confirmed immediate
// merge (issue #1892: a merged agent PR whose body may have omitted or
// reworded the Closes #<N> keyword) closes the issue through the optional
// forge.MergeCloser surface, as a deterministic backstop to GitHub's own
// merged-PR auto-close.
func TestSettle_ImmediateMergeClosesIssue(t *testing.T) {
	const issNum = "55"
	const prURL = "https://github.com/owner/repo/pull/55"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "ready", Note: "ok"},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.CloseMergedIssueCalls) != 1 || fc.CloseMergedIssueCalls[0] != issNum {
		t.Errorf("CloseMergedIssueCalls = %v, want [%s]", fc.CloseMergedIssueCalls, issNum)
	}
}

// TestSettle_LocalTrackerWithPRForgeDoesNotClose verifies that
// ISSUE_TRACKER=local paired with a PRForge-implementing Code Forge (a
// valid, independently-configured combination — CODE_FORGE=github, say)
// never drives the local tracker's IssueCloser through settle's post-merge
// backstop. Only reconcile's sweep may write local's closed: axis; settle's
// backstop is scoped to forge.MergeCloser, which the local adapter's shape
// (AsLocalShaped) does not implement, even though it does implement
// IssueCloser.
func TestSettle_LocalTrackerWithPRForgeDoesNotClose(t *testing.T) {
	const issNum = "58"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: testPR, Status: "ready", Note: "ok"},
		},
	}

	s := newTestSettle(baseConfig(), fc.AsLocalShaped(), fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none", fc.CloseIssueCalls)
	}
	if len(fc.CloseMergedIssueCalls) != 0 {
		t.Errorf("CloseMergedIssueCalls = %v, want none", fc.CloseMergedIssueCalls)
	}
}

// TestSettle_ManualModeDoesNotCloseIssue verifies that a green CI outcome
// under manual/auto MergeMode — which leaves the PR open rather than merging
// it (landingManual, never landingMerged) — never closes the issue: issue
// #1892's backstop must fire only after a confirmed merge, not merely a green
// CI run.
func TestSettle_ManualModeDoesNotCloseIssue(t *testing.T) {
	for _, mode := range []string{"manual", "auto"} {
		t.Run(mode, func(t *testing.T) {
			const issNum = "56"

			fc := forge.NewFake(testDispatchLabels)
			fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
			fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

			c := baseConfig()
			c.MergeMode = mode
			result := dispatch.Result{
				Success: true,
				Resolved: outcome.Resolved{
					Found:   true,
					Outcome: outcome.Outcome{Issue: issNum, Landing: testPR, Status: "ready", Note: "ok"},
				},
			}

			s := newTestSettle(c, fc, fc)
			s.Settle(dispatch.NewFake(), issNum, 0, result)

			if len(fc.CloseMergedIssueCalls) != 0 {
				t.Errorf("mode=%s: CloseMergedIssueCalls = %v, want none", mode, fc.CloseMergedIssueCalls)
			}
		})
	}
}

// TestSettle_RedCIDoesNotCloseIssue verifies that an outcome which never
// reaches green CI (landingFailed) never closes the issue. Also asserts
// (issue #2328) that the "ready" case's landingFailed print carries
// selfHeal's own classified reason rather than the old hardcoded "CI or
// merge failed" literal.
func TestSettle_RedCIDoesNotCloseIssue(t *testing.T) {
	const issNum = "57"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})

	c := baseConfig()
	c.MaxFixAttempts = 0
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: testPR, Status: "ready", Note: "ok"},
		},
	}

	s := newTestSettle(c, fc, fc)

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	s.Settle(dispatch.NewFake(), issNum, 0, result)
	w.Close()
	os.Stdout = old
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	stdout := string(captured)

	if len(fc.CloseMergedIssueCalls) != 0 {
		t.Errorf("CloseMergedIssueCalls = %v, want none", fc.CloseMergedIssueCalls)
	}
	if strings.Contains(stdout, "CI or merge failed") {
		t.Errorf("stdout must not contain the old hardcoded literal, got: %s", stdout)
	}
	if !strings.Contains(stdout, "ci-red: still red after exhausting 0 fix pass(es)") {
		t.Errorf("stdout must contain selfHeal's classified reason, got: %s", stdout)
	}
}

// TestSettle_MalformedOutcome_NoPRDemotesToFailed verifies that a ParseErr
// result with no adoptable PR runs the same no-PR demotion as the
// no-outcome-found path (issue #1898): a box that mangled its outcome line
// AND never opened a PR has produced nothing landable, so it demotes to
// agent-failed exactly like a genuinely missing outcome line does — never a
// silent no-op.
func TestSettle_MalformedOutcome_NoPRDemotesToFailed(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	result := dispatch.Result{ParseErr: errFake}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(dispatch.NewFake(), "9", 0, result)

	if len(fc.CommentCalls) != 0 {
		t.Errorf("malformed outcome must not post a usage comment; got %+v", fc.CommentCalls)
	}
	iss, _ := fc.Issue("9")
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("malformed outcome with no PR must demote to agent-failed; got labels=%v", iss.Labels)
	}
}

// TestSettle_MalformedOutcome_NonDraftPRBlocked verifies that a ParseErr
// result (a box that exited zero but emitted an unparseable outcome line)
// still runs the same PR-adoption check as the no-outcome-found path: an
// open PR must be reported status=blocked, not silently dropped under
// status=malformed with no further trace (issue #1898, observed on #1895 /
// PR #1897 — a clean, green, mergeable PR left un-adopted by a malformed
// outcome line).
func TestSettle_MalformedOutcome_NonDraftPRBlocked(t *testing.T) {
	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	branch := fc.AgentBranch("9")
	fc.SetPR(branch, forge.PR{URL: testPR})

	c := baseConfig()
	s := newTestSettle(c, fc, fc)
	result := dispatch.Result{ParseErr: errFake}

	out := testutil.CaptureStdout(t, func() {
		s.Settle(dispatch.NewFake(), "9", 0, result)
	})

	if !strings.Contains(out, "status=blocked") {
		t.Errorf("malformed outcome with an open PR must report status=blocked; got: %q", out)
	}
	if strings.Contains(out, "status=malformed") {
		t.Errorf("malformed outcome with an open PR must not be silently dropped as status=malformed; got: %q", out)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("open PR must not trigger label churn; got %v", fc.TransitionStateCalls)
	}
}

// TestSettle_GitForge_MergedStatusSkipsVerify verifies that a push-only
// forge's "merged" outcome status never reaches verifyMerged's PR-state
// check: the push-only forge's PRState always errors, so an unguarded call
// would wrongly demote the issue to agent-failed even though nothing is
// actually wrong.
func TestSettle_GitForge_MergedStatusSkipsVerify(t *testing.T) {
	const branch = "agent/issue-1"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.PRStateErr = errFake

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "1", Landing: branch, Status: "merged", Note: "ok"},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc.AsPushOnly())
	s.Settle(d, "1", 0, result)

	iss, _ := fc.Issue("1")
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue 1 must NOT have agent-failed; got labels=%v", iss.Labels)
	}
}

// TestSettle_NoOutcome_NonDraftPRBlocked verifies that a box exiting with no
// outcome line reports status=blocked and takes no action even when the
// discovered PR is non-draft — a no-outcome run is never adopted off
// draft-ness (issue #1654); adoption only happens via the explicit
// agent-recover entry point (SettleAdopted).
func TestSettle_NoOutcome_NonDraftPRBlocked(t *testing.T) {
	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{"agent-in-progress"}})
	branch := fc.AgentBranch("3")
	fc.SetPR(branch, forge.PR{URL: testPR})

	c := baseConfig()
	s := newTestSettle(c, fc, fc)
	s.Settle(dispatch.NewFake(), "3", 0, dispatch.Result{Success: true})

	if fc.Merged != "" {
		t.Errorf("non-draft PR must not be merged off draft-ness; fc.Merged=%q", fc.Merged)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("non-draft PR must not trigger label churn; got %v", fc.TransitionStateCalls)
	}
}

// TestSettle_NoOutcome_NoPRFound reports status=missing and demotes the
// issue to agent-failed when no outcome line and no open PR exist — the
// Driver crashed before ever opening a PR, so there is nothing left to
// adopt (issue #1605).
func TestSettle_NoOutcome_NoPRFound(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "4", Labels: []string{"agent-in-progress"}})

	c := baseConfig()
	s := newTestSettle(c, fc, fc)
	s.Settle(dispatch.NewFake(), "4", 0, dispatch.Result{Success: true})

	iss, _ := fc.Issue("4")
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("no-PR case must demote to agent-failed; got labels=%v", iss.Labels)
	}
	if len(fc.CommentCalls) != 0 {
		t.Errorf("no-PR case must not post a usage comment; got %v", fc.CommentCalls)
	}
}

// TestSettle_NoOutcome_PRLookupError_NoLabelChurn verifies that a transient
// forge lookup failure while resolving the open PR is reported but does not
// demote the issue: unlike a confirmed absence of a PR, a lookup error
// leaves genuine doubt about whether a live, mergeable PR exists, and
// wrongly demoting it would bury a possibly-fine run under agent-failed
// (issue #1605 review follow-up).
func TestSettle_NoOutcome_PRLookupError_NoLabelChurn(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "6", Labels: []string{"agent-in-progress"}})
	fc.OpenPRForBranchErr = errFake

	c := baseConfig()
	s := newTestSettle(c, fc, fc)
	s.Settle(dispatch.NewFake(), "6", 0, dispatch.Result{Success: true})

	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("PR lookup error must not trigger label churn; got %v", fc.TransitionStateCalls)
	}
}

// TestSettle_NoOutcome_PRLookupError_PrintsClassification verifies that a
// lookup-error console line still carries the log's classification note
// (class=/reason=), matching the confirmed-no-PR branch's console output —
// a lookup failure must not silently drop diagnostic detail a human
// triaging agent-failed would otherwise rely on.
func TestSettle_NoOutcome_PRLookupError_PrintsClassification(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "6", Labels: []string{"agent-in-progress"}})
	fc.OpenPRForBranchErr = errFake

	c := baseConfig()
	s := newTestSettle(c, fc, fc)
	result := dispatch.Result{
		Success:        true,
		Classification: driver.Classification{Class: driver.Terminal, Reason: driver.TaskFailed},
	}
	out := testutil.CaptureStdout(t, func() {
		s.Settle(dispatch.NewFake(), "6", 0, result)
	})

	if !strings.Contains(out, "class=terminal") || !strings.Contains(out, "reason=taskFailed") {
		t.Errorf("console output must carry classification on a lookup error; got: %q", out)
	}
}

// TestSettle_GitForge_NoOutcome_DemotesToFailed verifies that the demotion
// added by issue #1605 also fires on a push-only Code Forge: it has no
// PRForge surface at all, so ResolveOpenPR always reports not-found for it,
// and a box that exits with no outcome line has produced nothing landable —
// the same "no adoptable PR exists" case a github forge hits when no PR was
// opened.
func TestSettle_GitForge_NoOutcome_DemotesToFailed(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "8", Labels: []string{"agent-in-progress"}})

	c := baseConfig()
	s := newTestSettle(c, fc, fc.AsPushOnly())
	s.Settle(dispatch.NewFake(), "8", 0, dispatch.Result{Success: true})

	iss, _ := fc.Issue("8")
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("push-only forge no-outcome case must demote to agent-failed; got labels=%v", iss.Labels)
	}
}

// TestSettle_RecordsLanding_WhenTrackerImplementsIt verifies Settle calls
// the optional LandingRecorder method with the parsed outcome's landing ref
// once a work-kind outcome line is parsed, for a tracker that implements it
// (ADR 0029) — exercised on the simplest "blocked" outcome path.
func TestSettle_RecordsLanding_WhenTrackerImplementsIt(t *testing.T) {
	const issNum = "42"
	const prURL = "https://github.com/owner/repo/pull/99"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.RecordLandingCalls) != 1 {
		t.Fatalf("want 1 RecordLanding call, got %d", len(fc.RecordLandingCalls))
	}
	call := fc.RecordLandingCalls[0]
	if call.Num != issNum || call.Landing != prURL {
		t.Errorf("unexpected call: %+v", call)
	}
}

// TestSettle_RecordsLanding_OnReadyOutcome verifies recordLanding fires on
// the "ready" outcome path too, not just "blocked" — it sits ahead of the
// status switch, so every work outcome status records the landing ref.
func TestSettle_RecordsLanding_OnReadyOutcome(t *testing.T) {
	const issNum = "55"
	const prURL = "https://github.com/owner/repo/pull/55"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "ready", Note: "ok"},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.RecordLandingCalls) != 1 {
		t.Fatalf("want 1 RecordLanding call, got %d", len(fc.RecordLandingCalls))
	}
	call := fc.RecordLandingCalls[0]
	if call.Num != issNum || call.Landing != prURL {
		t.Errorf("unexpected call: %+v", call)
	}
}

// TestSettle_RecordLanding_NoOpWhenTrackerDoesNotImplementIt verifies Settle
// settles normally, without panicking, against a tracker that doesn't
// implement LandingRecorder — matching the github/jira adapters' shape.
func TestSettle_RecordLanding_NoOpWhenTrackerDoesNotImplementIt(t *testing.T) {
	const issNum = "42"
	const prURL = "https://github.com/owner/repo/pull/99"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
	}

	s := newTestSettle(baseConfig(), fc.AsNoLandingRecorder(), fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.RecordLandingCalls) != 0 {
		t.Errorf("want no RecordLanding calls against a tracker that doesn't implement it, got %+v", fc.RecordLandingCalls)
	}
}

// TestSettle_RecordsLandingPass_PicksLastOutcomeFoundEntry verifies Settle
// calls the optional LandingPassRecorder method with the last Passes entry
// whose OutcomeFound is true (issue #2983) — the pass whose own log the
// settled outcome was actually parsed from, not merely the last pass that
// ran.
func TestSettle_RecordsLandingPass_PicksLastOutcomeFoundEntry(t *testing.T) {
	const issNum = "42"
	const prURL = "https://github.com/owner/repo/pull/99"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
		Passes: []passmanifest.Entry{
			{Pass: 1, Kind: "implement", OutcomeFound: false},
			{Pass: 2, Kind: "fix", OutcomeFound: true},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.RecordLandingPassCalls) != 1 {
		t.Fatalf("want 1 RecordLandingPass call, got %d: %+v", len(fc.RecordLandingPassCalls), fc.RecordLandingPassCalls)
	}
	call := fc.RecordLandingPassCalls[0]
	if call.Num != issNum || call.Pass != 2 || call.Kind != "fix" {
		t.Errorf("unexpected call: %+v, want {Num: %q, Pass: 2, Kind: fix}", call, issNum)
	}
}

// TestSettle_RecordsLandingPass_FallsBackToLastEntryWhenNoneHasOutcomeFound
// verifies recordLandingPass (issue #2983) falls back to the last Passes
// entry overall — not any earlier entry — when no entry in the manifest has
// OutcomeFound set, e.g. a manifest present but the settled outcome came
// from the synthetic-backstop tier rather than a genuine in-pass marker.
func TestSettle_RecordsLandingPass_FallsBackToLastEntryWhenNoneHasOutcomeFound(t *testing.T) {
	const issNum = "42"
	const prURL = "https://github.com/owner/repo/pull/99"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
		Passes: []passmanifest.Entry{
			{Pass: 1, Kind: "implement", OutcomeFound: false},
			{Pass: 2, Kind: "fix", OutcomeFound: false},
			{Pass: 3, Kind: "verify", OutcomeFound: false},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.RecordLandingPassCalls) != 1 {
		t.Fatalf("want 1 RecordLandingPass call, got %d: %+v", len(fc.RecordLandingPassCalls), fc.RecordLandingPassCalls)
	}
	call := fc.RecordLandingPassCalls[0]
	if call.Num != issNum || call.Pass != 3 || call.Kind != "verify" {
		t.Errorf("unexpected call: %+v, want {Num: %q, Pass: 3, Kind: verify}", call, issNum)
	}
}

// TestSettle_RecordsLandingPass_PassesThroughEmptyKind verifies
// recordLandingPass (issue #2983) forwards an empty Kind on the picked
// entry to RecordLandingPass unchanged rather than substituting or
// swallowing it — a manifest entry with Kind == "" (e.g. from a
// stale/older manifest.json) is downstream local.render's job to degrade
// gracefully, not settle's job to paper over.
func TestSettle_RecordsLandingPass_PassesThroughEmptyKind(t *testing.T) {
	const issNum = "42"
	const prURL = "https://github.com/owner/repo/pull/99"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
		Passes: []passmanifest.Entry{
			{Pass: 1, Kind: "implement", OutcomeFound: false},
			{Pass: 2, Kind: "", OutcomeFound: true},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.RecordLandingPassCalls) != 1 {
		t.Fatalf("want 1 RecordLandingPass call, got %d: %+v", len(fc.RecordLandingPassCalls), fc.RecordLandingPassCalls)
	}
	call := fc.RecordLandingPassCalls[0]
	if call.Num != issNum || call.Pass != 2 || call.Kind != "" {
		t.Errorf("unexpected call: %+v, want {Num: %q, Pass: 2, Kind: \"\"}", call, issNum)
	}
}

// TestSettle_RecordLandingPass_LogsErrorOnFailure verifies recordLandingPass
// (issue #2983) logs, rather than propagates, an error returned by the
// tracker's RecordLandingPass call — best-effort bookkeeping that must never
// fail settling itself.
func TestSettle_RecordLandingPass_LogsErrorOnFailure(t *testing.T) {
	const issNum = "42"
	const prURL = "https://github.com/owner/repo/pull/99"
	sentinel := fmt.Errorf("some tracker failure")

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.RecordLandingPassErr = sentinel

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
		Passes: []passmanifest.Entry{
			{Pass: 1, Kind: "implement", OutcomeFound: true},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)

	stderr := captureStderr(t, func() {
		s.Settle(dispatch.NewFake(), issNum, 0, result)
	})

	if len(fc.RecordLandingPassCalls) != 1 {
		t.Fatalf("want 1 RecordLandingPass call, got %d: %+v", len(fc.RecordLandingPassCalls), fc.RecordLandingPassCalls)
	}
	if !strings.Contains(stderr, sentinel.Error()) {
		t.Errorf("want stderr to contain %q, got %q", sentinel.Error(), stderr)
	}
	if !strings.Contains(stderr, issNum) {
		t.Errorf("want stderr to name the issue %q, got %q", issNum, stderr)
	}
}

// TestSettle_RecordLandingPass_NoOpWhenPassesEmpty verifies Settle makes no
// RecordLandingPass call, and settles without error, when result.Passes
// carries no manifest evidence (issue #2983) — e.g. a Box that wrote no
// manifest file to its outbox.
func TestSettle_RecordLandingPass_NoOpWhenPassesEmpty(t *testing.T) {
	const issNum = "42"
	const prURL = "https://github.com/owner/repo/pull/99"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.RecordLandingPassCalls) != 0 {
		t.Errorf("want no RecordLandingPass calls with no Passes evidence, got %+v", fc.RecordLandingPassCalls)
	}
}

// TestSettle_RecordLandingPass_NoOpWhenLandingEmpty verifies Settle makes no
// RecordLandingPass call when the outcome carries no landing ref (issue
// #2983) — mirroring recordLanding's own "a blank write must never clear an
// already-recorded ref" guard. Without this guard, a later blocked run with
// manifest evidence but no landing would silently overwrite the pass
// provenance for a PR landed by an earlier run, attributing that landed PR
// to a pass that produced no landing at all.
func TestSettle_RecordLandingPass_NoOpWhenLandingEmpty(t *testing.T) {
	const issNum = "42"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "", Status: "blocked", Note: "tests failing"},
		},
		Passes: []passmanifest.Entry{
			{Pass: 1, Kind: "implement", OutcomeFound: true},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.RecordLandingPassCalls) != 0 {
		t.Errorf("want no RecordLandingPass calls when landing is empty, got %+v", fc.RecordLandingPassCalls)
	}
}

// TestSettle_RecordLandingPass_NoOpWhenTrackerDoesNotImplementIt verifies
// Settle settles normally, without panicking or erroring, against a tracker
// that doesn't implement LandingPassRecorder — matching the github/jira
// adapters' shape — even when result.Passes carries manifest evidence.
func TestSettle_RecordLandingPass_NoOpWhenTrackerDoesNotImplementIt(t *testing.T) {
	const issNum = "42"
	const prURL = "https://github.com/owner/repo/pull/99"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
		Passes: []passmanifest.Entry{
			{Pass: 1, Kind: "implement", OutcomeFound: true},
		},
	}

	s := newTestSettle(baseConfig(), fc.AsNoLandingRecorder(), fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.RecordLandingPassCalls) != 0 {
		t.Errorf("want no RecordLandingPass calls against a tracker that doesn't implement it, got %+v", fc.RecordLandingPassCalls)
	}
}

// TestSettle_NonceRejectedIssueIntent_LogsWarning verifies a nonce-mismatched
// SPINDRIFT_ISSUE_INTENT line — one that carried the token but failed nonce
// verification, surfaced only via Result.IssueIntentsRejected (issue #2976)
// — produces a settle-logged warning naming the channel and the count,
// instead of the old silent-drop behavior where a rejected line left no
// trace at all.
func TestSettle_NonceRejectedIssueIntent_LogsWarning(t *testing.T) {
	const issNum = "2976"
	const prURL = "https://github.com/owner/repo/pull/2976"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
		IssueIntentsRejected: 1,
	}

	s := newTestSettle(baseConfig(), fc, fc)
	stderr := testutil.CaptureStderr(t, func() {
		s.Settle(dispatch.NewFake(), issNum, 0, result)
	})

	want := fmt.Sprintf("#%s: 1 nonce-mismatched issue-intent line(s) rejected", issNum)
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr must warn about the rejected issue-intent line; want substring %q, got: %q", want, stderr)
	}
}

// TestSettle_NonceRejectedComment_FoundSuppressesDuplicate verifies
// gate.go's logRejectedSignals warns for a rejected comment line only when a
// verifying match was also found on that channel (CommentFound true) —
// dispatch.outcomeResult's own comment-scan warning (retry.go) already
// covers the CommentFound=false case (every line on the channel rejected),
// so gate.go must stay silent there rather than double-warn.
func TestSettle_NonceRejectedComment_FoundSuppressesDuplicate(t *testing.T) {
	const issNum = "2976"
	const prURL = "https://github.com/owner/repo/pull/2976"

	baseResult := func(commentFound bool) dispatch.Result {
		return dispatch.Result{
			Success: true,
			Resolved: outcome.Resolved{
				Found:   true,
				Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
			},
			CommentFound:    commentFound,
			CommentRejected: 1,
		}
	}

	t.Run("found=false stays silent", func(t *testing.T) {
		fc := forge.NewFake(testDispatchLabels)
		fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

		s := newTestSettle(baseConfig(), fc, fc)
		stderr := testutil.CaptureStderr(t, func() {
			s.Settle(dispatch.NewFake(), issNum, 0, baseResult(false))
		})

		if strings.Contains(stderr, "nonce-mismatched comment") {
			t.Errorf("expected no comment-rejection warning when CommentFound is false (retry.go's scan-error path already covers it); got: %q", stderr)
		}
	})

	t.Run("found=true warns", func(t *testing.T) {
		fc := forge.NewFake(testDispatchLabels)
		fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

		s := newTestSettle(baseConfig(), fc, fc)
		stderr := testutil.CaptureStderr(t, func() {
			s.Settle(dispatch.NewFake(), issNum, 0, baseResult(true))
		})

		want := fmt.Sprintf("#%s: 1 nonce-mismatched comment line(s) rejected", issNum)
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr must warn about the rejected comment line when CommentFound is true; want substring %q, got: %q", want, stderr)
		}
	})
}

var errFake = fakeErr("fake error")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }
