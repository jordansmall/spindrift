package settle

import (
	"fmt"
	"io"
	"os"
	"reflect"
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
// sweep may close a landed local issue (ADR 0029).
func TestSettle_LocalTrackerWithPRForgeDoesNotClose(t *testing.T) {
	const issNum = "58"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	result := dispatch.Result{
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
	s.Settle(dispatch.NewFake(), "3", 0, dispatch.Result{})

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
	s.Settle(dispatch.NewFake(), "4", 0, dispatch.Result{})

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
	s.Settle(dispatch.NewFake(), "6", 0, dispatch.Result{})

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
	s.Settle(dispatch.NewFake(), "8", 0, dispatch.Result{})

	iss, _ := fc.Issue("8")
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("push-only forge no-outcome case must demote to agent-failed; got labels=%v", iss.Labels)
	}
}

// The Box's landing= value is untrusted and reconcile trusts whatever settle
// records, so a blocked or ambiguous outcome must never persist it (issue #5005).
func TestSettle_DoesNotRecordBoxLanding_OnNonLandedOutcome(t *testing.T) {
	const issNum = "42"
	for _, tc := range []struct{ name, status, landing string }{
		{"blocked branch", "blocked", "integration/foo@abc123"},
		{"blocked bare integration branch", "blocked", "integration/foo"},
		{"blocked PR URL", "blocked", "https://github.com/owner/repo/pull/99"},
		{"ambiguous bare integration branch", "ambiguous", "integration/foo"},
		{"ambiguous branch", "ambiguous", "integration/foo@abc123"},
		{"ambiguous PR URL", "ambiguous", "https://github.com/owner/repo/pull/99"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := forge.NewFake()
			fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

			result := dispatch.Result{
				Resolved: outcome.Resolved{
					Found:   true,
					Outcome: outcome.Outcome{Issue: issNum, Landing: tc.landing, Status: tc.status, Note: "n"},
				},
			}

			s := newTestSettle(baseConfig(), fc, fc)
			s.Settle(dispatch.NewFake(), issNum, 0, result)

			if len(fc.RecordLandingCalls) != 0 {
				t.Errorf("want no RecordLanding calls, got %+v", fc.RecordLandingCalls)
			}
		})
	}
}

// A ready outcome on a read-write forge must not persist the Box's PR URL
// either; only host-derived landings are recorded (issue #5005).
func TestSettle_DoesNotRecordBoxLanding_OnReadyOutcome(t *testing.T) {
	const issNum = "55"
	const prURL = "https://github.com/owner/repo/pull/55"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "ready", Note: "ok"},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if got := fc.RecordLandingCalls; len(got) != 0 {
		t.Errorf("RecordLandingCalls = %+v, want none (the Box's landing= is never recorded)", got)
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

// hostLandedIssue is the issue number the host-landed fixtures settle.
const hostLandedIssue = "42"

// hostLandedPassFixture builds a read-only Settle whose ready hand-off opens a
// host-side draft PR, the host-derived landing recordLanding persists (issue
// #5189), over a local-shaped tracker that implements both LandingRecorder and
// LandingPassRecorder. It returns the Settle and the run's Result carrying
// passes.
func hostLandedPassFixture(t *testing.T, fc *forge.Fake, passes []passmanifest.Entry) (*Settle, dispatch.Result) {
	t.Helper()
	return hostLandedPassFixtureOver(t, fc, fc.AsLocalShaped(), passes)
}

// hostLandedPassFixtureOver is hostLandedPassFixture over a caller-chosen
// tracker, for shapes the Fake has no view of.
func hostLandedPassFixtureOver(t *testing.T, fc *forge.Fake, tracker forge.IssueTracker, passes []passmanifest.Entry) (*Settle, dispatch.Result) {
	t.Helper()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: hostLandedIssue, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = draftPRRetryURL
	fc.SetCheckStates(draftPRRetryURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	t.Cleanup(InstallTally(&Tally{}))

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(string) string { return t.TempDir() }
	c.BaseBranch = "main"
	_, c.Clock = recordingClock()
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: hostLandedIssue, Landing: fc.AgentBranch(hostLandedIssue), Status: "ready"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
		Passes:        passes,
	}
	return newTestSettle(c, tracker, fc.AsGithubReadOnly()), result
}

// settleHostLanded settles a host-landed run over passes and returns the Fake
// with that run's RecordLanding and RecordLandingPass calls recorded.
func settleHostLanded(t *testing.T, passes []passmanifest.Entry) *forge.Fake {
	t.Helper()
	fc := forge.NewFake(testDispatchLabels)
	s, result := hostLandedPassFixture(t, fc, passes)
	captureStdout(t, func() { s.Settle(dispatch.NewFake(), hostLandedIssue, 0, result) })
	if len(fc.RecordLandingCalls) == 0 {
		t.Fatal("fixture must record a host-derived landing")
	}
	return fc
}

// implementThenFixPasses is the manifest of a run whose implement pass printed
// no outcome and whose fix pass did.
func implementThenFixPasses() []passmanifest.Entry {
	return []passmanifest.Entry{
		{Pass: 1, Kind: "implement", OutcomeFound: false},
		{Pass: 2, Kind: "fix", OutcomeFound: true},
	}
}

// The optional LandingPassRecorder gets the last Passes entry whose
// OutcomeFound is true (issue #2983): the pass whose own log the settled
// outcome was parsed from, not merely the last pass that ran. It is written
// alongside the host-derived landing, never off the Box's outcome line (issue
// #5189).
func TestSettle_RecordsLandingPass_PicksLastOutcomeFoundEntry(t *testing.T) {
	fc := settleHostLanded(t, implementThenFixPasses())

	want := []forge.RecordLandingPassCall{{Num: hostLandedIssue, Pass: 2, Kind: "fix"}}
	if !reflect.DeepEqual(fc.RecordLandingPassCalls, want) {
		t.Errorf("RecordLandingPass calls = %+v, want %+v", fc.RecordLandingPassCalls, want)
	}
}

// With no manifest entry marked OutcomeFound, recordLandingPass falls back
// to the last entry overall, not any earlier one (issue #2983), e.g. when
// the settled outcome came from the synthetic-backstop tier rather than a
// genuine in-pass marker.
func TestSettle_RecordsLandingPass_FallsBackToLastEntryWhenNoneHasOutcomeFound(t *testing.T) {
	fc := settleHostLanded(t, []passmanifest.Entry{
		{Pass: 1, Kind: "implement", OutcomeFound: false},
		{Pass: 2, Kind: "fix", OutcomeFound: false},
		{Pass: 3, Kind: "verify", OutcomeFound: false},
	})

	want := []forge.RecordLandingPassCall{{Num: hostLandedIssue, Pass: 3, Kind: "verify"}}
	if !reflect.DeepEqual(fc.RecordLandingPassCalls, want) {
		t.Errorf("RecordLandingPass calls = %+v, want %+v", fc.RecordLandingPassCalls, want)
	}
}

// recordLandingPass forwards an empty Kind on the picked entry unchanged
// rather than substituting or swallowing it (issue #2983): an entry with
// Kind == "", e.g. from an older manifest.json, is local.render's job to
// degrade gracefully, not settle's to paper over.
func TestSettle_RecordsLandingPass_PassesThroughEmptyKind(t *testing.T) {
	fc := settleHostLanded(t, []passmanifest.Entry{
		{Pass: 1, Kind: "implement", OutcomeFound: false},
		{Pass: 2, Kind: "", OutcomeFound: true},
	})

	want := []forge.RecordLandingPassCall{{Num: hostLandedIssue, Pass: 2, Kind: ""}}
	if !reflect.DeepEqual(fc.RecordLandingPassCalls, want) {
		t.Errorf("RecordLandingPass calls = %+v, want %+v", fc.RecordLandingPassCalls, want)
	}
}

// recordLandingPass logs, rather than propagates, an error from the
// tracker's RecordLandingPass call (issue #2983): best-effort bookkeeping
// must never fail settling itself.
func TestSettle_RecordLandingPass_LogsErrorOnFailure(t *testing.T) {
	sentinel := fmt.Errorf("some tracker failure")

	fc := forge.NewFake(testDispatchLabels)
	fc.RecordLandingPassErr = sentinel
	s, result := hostLandedPassFixture(t, fc, []passmanifest.Entry{
		{Pass: 1, Kind: "implement", OutcomeFound: true},
	})

	stderr := captureStderr(t, func() {
		captureStdout(t, func() { s.Settle(dispatch.NewFake(), hostLandedIssue, 0, result) })
	})

	if len(fc.RecordLandingPassCalls) != 1 {
		t.Fatalf("want 1 RecordLandingPass call, got %d: %+v", len(fc.RecordLandingPassCalls), fc.RecordLandingPassCalls)
	}
	if !strings.Contains(stderr, sentinel.Error()) {
		t.Errorf("want stderr to contain %q, got %q", sentinel.Error(), stderr)
	}
	if !strings.Contains(stderr, "#42") {
		t.Errorf("want stderr to name the issue #42, got %q", stderr)
	}
}

// A failed RecordLanding write skips the pass write (issue #5189): a pass
// recorded against a landing that never persisted would be orphaned provenance.
func TestSettle_RecordLandingPass_SkippedWhenRecordLandingFails(t *testing.T) {
	sentinel := fmt.Errorf("landing write failed")

	fc := forge.NewFake(testDispatchLabels)
	fc.RecordLandingErr = sentinel
	s, result := hostLandedPassFixture(t, fc, []passmanifest.Entry{
		{Pass: 1, Kind: "implement", OutcomeFound: true},
	})

	stderr := captureStderr(t, func() {
		captureStdout(t, func() { s.Settle(dispatch.NewFake(), hostLandedIssue, 0, result) })
	})

	if len(fc.RecordLandingCalls) == 0 {
		t.Fatal("fixture must attempt a host-derived landing write")
	}
	if len(fc.RecordLandingPassCalls) != 0 {
		t.Errorf("RecordLandingPass must not run after a failed RecordLanding, got %+v", fc.RecordLandingPassCalls)
	}
	if !strings.Contains(stderr, sentinel.Error()) {
		t.Errorf("want stderr to contain %q, got %q", sentinel.Error(), stderr)
	}
}

// A host landing written with no manifest evidence (issue #5189), e.g. from
// spindrift recover, whose Result carries no Passes, clears the recorded pass
// rather than leaving an earlier run's pass attached to the new landing.
func TestSettle_RecordLandingPass_ClearsPassWhenPassesEmpty(t *testing.T) {
	fc := settleHostLanded(t, nil)

	want := []forge.RecordLandingPassCall{{Num: hostLandedIssue, Pass: 0, Kind: ""}}
	if !reflect.DeepEqual(fc.RecordLandingPassCalls, want) {
		t.Errorf("RecordLandingPass calls = %+v, want %+v", fc.RecordLandingPassCalls, want)
	}
}

// A blocked run records no landing, so it records no pass either, however
// its Box-controlled outcome line fills in landing= (issue #5189).
func TestSettle_RecordLandingPass_NoOpWhenNoHostLanding(t *testing.T) {
	const issNum = "42"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "x", Status: "blocked", Note: "tests failing"},
		},
		Passes: []passmanifest.Entry{
			{Pass: 1, Kind: "implement", OutcomeFound: true},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	if len(fc.RecordLandingPassCalls) != 0 || len(fc.RecordLandingCalls) != 0 {
		t.Errorf("want no landing or pass writes, got landing=%+v pass=%+v", fc.RecordLandingCalls, fc.RecordLandingPassCalls)
	}
}

// Regression for issue #5189: a later blocked run that prints landing=x at an
// earlier pass must not overwrite the pass provenance of the push-only
// placeholder an earlier run landed.
func TestSettle_RecordLandingPass_LaterBlockedRunDoesNotOverwriteLandedPass(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "manual"
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: hostLandedIssue, Labels: []string{"agent-in-progress"}})
	s := newTestSettle(c, fc.AsLocalShaped(), fc.AsLocal())
	landed := dispatch.Result{
		Resolved: outcome.Resolved{
			SelfReportFound: true,
			SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
		},
		Passes: []passmanifest.Entry{{Pass: 2, Kind: "fix", OutcomeFound: true}},
	}
	sit := s.situationFor(hostLandedIssue, false, landed)
	if got, err := s.SettleRelayedBranch(dispatch.NewFake(), hostLandedIssue, 0, sit, landed); !got || err != nil {
		t.Fatalf("SettleRelayedBranch = %v, %v; want true, nil", got, err)
	}
	landings, passWrites := len(fc.RecordLandingCalls), len(fc.RecordLandingPassCalls)
	if landings == 0 {
		t.Fatal("fixture must record the push-only placeholder landing")
	}

	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: hostLandedIssue, Landing: "x", Status: "blocked", Note: "tests failing"},
		},
		Passes: []passmanifest.Entry{
			{Pass: 1, Kind: "implement", OutcomeFound: true},
		},
	}
	s.Settle(dispatch.NewFake(), hostLandedIssue, 0, result)

	if len(fc.RecordLandingPassCalls) != passWrites {
		t.Errorf("blocked run must not write a pass, got %+v", fc.RecordLandingPassCalls)
	}
	for _, call := range fc.RecordLandingPassCalls {
		if want := (forge.RecordLandingPassCall{Num: hostLandedIssue, Pass: 2, Kind: "fix"}); call != want {
			t.Errorf("RecordLandingPass call = %+v, want only the landed run's %+v", call, want)
		}
	}
	if len(fc.RecordLandingCalls) != landings {
		t.Errorf("blocked run must not record a landing, got %+v", fc.RecordLandingCalls)
	}
}

// landPushOnly records the pass alongside the host-derived placeholder branch
// it writes after the relay (issue #5189).
func TestSelfHeal_PushOnlyLanding_RecordsPassWithPlaceholder(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "manual"
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	branch := fc.AgentBranch("1")
	s := newTestSettle(c, fc.AsLocalShaped(), fc.AsLocal())
	passes := implementThenFixPasses()

	s.selfHeal(dispatch.NewFake(), "1", 0, branch, passes)

	wantLanding := []forge.RecordLandingCall{{Num: "1", Landing: branch}}
	if !reflect.DeepEqual(fc.RecordLandingCalls, wantLanding) {
		t.Errorf("RecordLanding calls = %+v, want %+v", fc.RecordLandingCalls, wantLanding)
	}
	wantPass := []forge.RecordLandingPassCall{{Num: "1", Pass: 2, Kind: "fix"}}
	if !reflect.DeepEqual(fc.RecordLandingPassCalls, wantPass) {
		t.Errorf("RecordLandingPass calls = %+v, want %+v", fc.RecordLandingPassCalls, wantPass)
	}
}

// SettleAdopted has no pass evidence, so on a push-only forge it records the
// placeholder landing and clears the recorded pass rather than leaving an
// earlier run's attached (issue #5189).
func TestSettleAdopted_PushOnlyLanding_ClearsRecordedPass(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "manual"
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	branch := fc.AgentBranch("1")
	s := newTestSettle(c, fc.AsLocalShaped(), fc.AsLocal())

	captureStdout(t, func() { s.SettleAdopted(dispatch.NewFake(), "1", 0, branch) })

	wantLanding := []forge.RecordLandingCall{{Num: "1", Landing: branch}}
	if !reflect.DeepEqual(fc.RecordLandingCalls, wantLanding) {
		t.Errorf("RecordLanding calls = %+v, want %+v", fc.RecordLandingCalls, wantLanding)
	}
	wantPass := []forge.RecordLandingPassCall{{Num: "1", Pass: 0, Kind: ""}}
	if !reflect.DeepEqual(fc.RecordLandingPassCalls, wantPass) {
		t.Errorf("RecordLandingPass calls = %+v, want %+v", fc.RecordLandingPassCalls, wantPass)
	}
}

// landingOnlyTracker implements LandingRecorder but, unlike the local adapter,
// not LandingPassRecorder; embedding the interfaces hides the Fake's other
// methods.
type landingOnlyTracker struct {
	forge.IssueTracker
	forge.LandingRecorder
}

// A tracker with LandingRecorder but without LandingPassRecorder must still
// record the landing, skipping only the pass write, even when result.Passes
// carries manifest evidence.
func TestSettle_RecordLandingPass_NoOpWhenTrackerDoesNotImplementIt(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	tracker := landingOnlyTracker{IssueTracker: fc.AsLocalShaped(), LandingRecorder: fc}
	s, result := hostLandedPassFixtureOver(t, fc, tracker, []passmanifest.Entry{
		{Pass: 1, Kind: "implement", OutcomeFound: true},
	})

	captureStdout(t, func() { s.Settle(dispatch.NewFake(), hostLandedIssue, 0, result) })

	if len(fc.RecordLandingCalls) == 0 {
		t.Error("want the host-derived landing recorded")
	}
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
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
		},
		IssueIntentsRejected: outcome.Rejections{NonceMismatch: 1},
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

// gate.go's RecordSettleWarnings warns for a rejected comment line only when a
// verifying match was also found on that channel. dispatch.outcomeResult's
// own comment-scan warning in retry.go already covers CommentFound=false,
// where every line on the channel was rejected, so gate.go stays silent
// there rather than warning twice.
func TestSettle_NonceRejectedComment_FoundSuppressesDuplicate(t *testing.T) {
	const issNum = "2976"
	const prURL = "https://github.com/owner/repo/pull/2976"

	baseResult := func(commentFound bool) dispatch.Result {
		return dispatch.Result{
			Resolved: outcome.Resolved{
				Found:   true,
				Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
			},
			CommentFound:    commentFound,
			CommentRejected: outcome.Rejections{NonceMismatch: 1},
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

// RecordSettleWarnings must report the actual rejection cause — nonce mismatch,
// malformed payload, or both — rather than always saying nonce-mismatched
// (issue #3670). Table-driven so each cause's exact wording, sourced from
// outcome.Rejections.Cause(), stays pinned.
func TestSettle_RecordSettleWarnings_CausePinned(t *testing.T) {
	const issNum = "3670"
	const prURL = "https://github.com/owner/repo/pull/3670"

	tests := []struct {
		name      string
		rejected  outcome.Rejections
		wantCause string
	}{
		{"nonce mismatch only", outcome.Rejections{NonceMismatch: 1}, "nonce-mismatched"},
		{"malformed only", outcome.Rejections{Malformed: 1, LongestPayload: 8154}, "malformed"},
		{"both causes", outcome.Rejections{NonceMismatch: 1, Malformed: 1, LongestPayload: 8154}, "nonce-mismatched or malformed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fc := forge.NewFake(testDispatchLabels)
			fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

			result := dispatch.Result{
				Resolved: outcome.Resolved{
					Found:   true,
					Outcome: outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
				},
				CommentFound:    true,
				CommentRejected: tt.rejected,
			}

			s := newTestSettle(baseConfig(), fc, fc)
			stderr := testutil.CaptureStderr(t, func() {
				s.Settle(dispatch.NewFake(), issNum, 0, result)
			})

			want := fmt.Sprintf("#%s: %d %s comment line(s) rejected", issNum, tt.rejected.Total(), tt.wantCause)
			if !strings.Contains(stderr, want) {
				t.Errorf("stderr must warn with the exact cause wording; want substring %q, got: %q", want, stderr)
			}
		})
	}
}

var errFake = fakeErr("fake error")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

// Every kind's settle hands the sidecar writer one batch (issue #3744): the
// scan warnings outcomeResult collected, then the rejection warnings settle
// itself derives -- the same text stderr gets, minus its "    ?? #<n>: "
// prefix.
func TestSettle_RecordsScanThenRejectionWarnings(t *testing.T) {
	const issNum = "3744"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	scan := make([]string, 1, 4)
	scan[0] = "comment: 2 lines carried the token but none verified"
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Status: "blocked", Note: "tests failing"},
		},
		Warnings:        scan,
		CommentFound:    true,
		CommentRejected: outcome.Rejections{NonceMismatch: 1},
	}

	d := dispatch.NewFake()
	s := newTestSettle(baseConfig(), fc, fc)
	stderr := testutil.CaptureStderr(t, func() { s.Settle(d, issNum, 0, result) })

	rejection := "1 nonce-mismatched comment line(s) rejected"
	want := [][]string{{scan[0], rejection}}
	if !reflect.DeepEqual(d.RecordedWarnings, want) {
		t.Errorf("RecordedWarnings = %q, want %q", d.RecordedWarnings, want)
	}
	if !strings.Contains(stderr, "    ?? #"+issNum+": "+rejection+"\n") {
		t.Errorf("stderr = %q, want the rejection line unchanged", stderr)
	}
	if spare := scan[:cap(scan)][1]; spare != "" {
		t.Errorf("result.Warnings spare capacity was written into: %q", spare)
	}
}

// A settle with nothing to warn about still records one empty batch, so the
// sidecar from an earlier run is cleared.
func TestSettle_NoWarnings_RecordsEmptyBatch(t *testing.T) {
	const issNum = "3744"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Status: "blocked", Note: "tests failing"},
		},
	}

	d := dispatch.NewFake()
	newTestSettle(baseConfig(), fc, fc).Settle(d, issNum, 0, result)

	if len(d.RecordedWarnings) != 1 || len(d.RecordedWarnings[0]) != 0 {
		t.Errorf("RecordedWarnings = %q, want exactly one empty batch", d.RecordedWarnings)
	}
}
