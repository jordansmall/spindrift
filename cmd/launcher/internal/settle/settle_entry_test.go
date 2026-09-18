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

// The tracker is github-shaped (AsNoLandingRecorder) because a local
// tracker's blocked path posts an extra note comment
// (TestSettle_LocalForge_BlockedPostsNoteAsComment) that would break this
// comment count.
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

// A status=blocked outcome, including the synthetic backstop's, swaps
// agent-in-progress to agent-failed so the issue lands in the human-triage
// queue instead of looking in-flight forever (issue #1605, seen on #1542).
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

// The operator report prints landing=, matching the wire grammar's o.Landing
// field name, not the stale pr= label: the value may not be a PR at all
// (issue #655).
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

// Settle posts whatever UsageReport returns, including its "unavailable"
// fallback body. The tracker is github-shaped (AsNoLandingRecorder) for the
// same reason as TestSettle_PostsUsageComment_Blocked.
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

// Settle posts the usage comment after selfHeal runs, not only on the
// blocked path that skips it.
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

// A confirmed immediate merge closes the issue through the optional
// forge.MergeCloser, a deterministic backstop to GitHub's own merged-PR
// auto-close for agent PRs whose body omitted or reworded Closes #<N>
// (issue #1892).
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

// ISSUE_TRACKER=local paired with a PRForge Code Forge is a valid
// combination, and settle's post-merge backstop is scoped to
// forge.MergeCloser, which the local adapter (AsLocalShaped) does not
// implement even though it does implement IssueCloser. Only reconcile's
// sweep may write local's closed: axis.
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

// A green CI outcome under manual/auto MergeMode leaves the PR open
// (landingManual, never landingMerged), so issue #1892's backstop must not
// close the issue. Only a confirmed merge may.
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

// An outcome that never reaches green CI (landingFailed) must not close the
// issue. The "ready" case's landingFailed print must also carry selfHeal's
// own classified reason rather than the old hardcoded "CI or merge failed"
// literal (issue #2328).
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

// A ParseErr result with no adoptable PR runs the same no-PR demotion as the
// no-outcome-found path (issue #1898): a box that mangled its outcome line
// and never opened a PR produced nothing landable, so it demotes to
// agent-failed rather than silently no-opping.
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

// A ParseErr result, a box that exited zero but emitted an unparseable
// outcome line, still runs the PR-adoption check: an open PR is reported
// status=blocked, not dropped under status=malformed with no further trace
// (issue #1898, seen on #1895 / PR #1897, where a clean, green, mergeable PR
// was left un-adopted).
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

// A push-only forge's "merged" status must skip verifyMerged's PR-state
// check: that forge's PRState always errors, so an unguarded call would
// wrongly demote the issue to agent-failed.
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

// A box exiting with no outcome line reports status=blocked and takes no
// action even when the discovered PR is non-draft: a no-outcome run is never
// adopted off draft-ness (issue #1654). Adoption happens only through the
// explicit agent-recover entry point, SettleAdopted.
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

// No outcome line and no open PR means the Driver crashed before ever
// opening a PR, so nothing is left to adopt: status=missing and a demotion
// to agent-failed (issue #1605).
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

// A transient forge lookup failure leaves genuine doubt about whether a
// live, mergeable PR exists, unlike a confirmed absence, so settle reports
// it without demoting the issue and burying a possibly-fine run under
// agent-failed (issue #1605 review follow-up).
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

// The lookup-error console line still carries the log's class=/reason= note,
// matching the confirmed-no-PR branch: a lookup failure must not drop detail
// a human triaging agent-failed relies on.
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

// Issue #1605's demotion also fires on a push-only Code Forge: it implements
// no PRForge at all, so ResolveOpenPR always reports not-found, the same "no
// adoptable PR exists" case a github forge hits when no PR was opened.
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

// Settle calls the optional LandingRecorder with the parsed outcome's
// landing ref once a work-kind outcome line is parsed (ADR 0029), exercised
// here on the simplest "blocked" path.
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

// recordLanding sits ahead of the status switch, so "ready" records the
// landing ref just as "blocked" does.
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

// A tracker without LandingRecorder, matching the github/jira adapters'
// shape, must still settle without panicking.
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

// The optional LandingPassRecorder gets the last Passes entry whose
// OutcomeFound is true (issue #2983): the pass whose own log the settled
// outcome was parsed from, not merely the last pass that ran.
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

// With no manifest entry marked OutcomeFound, recordLandingPass falls back
// to the last entry overall, not any earlier one (issue #2983), e.g. when
// the settled outcome came from the synthetic-backstop tier rather than a
// genuine in-pass marker.
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

// recordLandingPass forwards an empty Kind on the picked entry unchanged
// rather than substituting or swallowing it (issue #2983): an entry with
// Kind == "", e.g. from an older manifest.json, is local.render's job to
// degrade gracefully, not settle's to paper over.
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

// recordLandingPass logs, rather than propagates, an error from the
// tracker's RecordLandingPass call (issue #2983): best-effort bookkeeping
// must never fail settling itself.
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

// With no manifest evidence in result.Passes (issue #2983), e.g. a Box that
// wrote no manifest file to its outbox, Settle makes no RecordLandingPass
// call and still settles without error.
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

// A blank landing must never clear an already-recorded ref (issue #2983),
// mirroring recordLanding's own guard. Without it, a later blocked run with
// manifest evidence but no landing would overwrite the pass provenance of a
// PR landed by an earlier run, attributing it to a pass that produced no
// landing at all.
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

// A tracker without LandingPassRecorder, matching the github/jira adapters'
// shape, must settle normally even when result.Passes carries manifest
// evidence.
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

// A SPINDRIFT_ISSUE_INTENT line that carried the token but failed nonce
// verification reaches settle only through Result.IssueIntentsRejected
// (issue #2976), and must produce a warning naming the channel and the
// count, instead of the old silent drop that left no trace at all.
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

// gate.go's logRejectedSignals warns for a rejected comment line only when a
// verifying match was also found on that channel. dispatch.outcomeResult's
// own comment-scan warning in retry.go already covers CommentFound=false,
// where every line on the channel was rejected, so gate.go stays silent
// there rather than warning twice.
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
