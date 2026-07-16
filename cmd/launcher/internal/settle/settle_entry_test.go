package settle

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/testutil"
)

// TestSettle_PostsUsageComment_Blocked verifies that Settle posts d's usage
// report as a comment when the outcome is "blocked".
func TestSettle_PostsUsageComment_Blocked(t *testing.T) {
	const issNum = "42"
	const prURL = "https://github.com/owner/repo/pull/99"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	d.UsageReportBody = "## Run usage\n\ncost: 0.25"
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
	}

	s := New(baseConfig(), fc, fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(fc.CommentCalls))
	}
	if fc.CommentCalls[0].Body != d.UsageReportBody {
		t.Errorf("comment body: got %q, want %q", fc.CommentCalls[0].Body, d.UsageReportBody)
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
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "tests failing"},
	}

	s := New(baseConfig(), fc, fc)
	out := testutil.CaptureStdout(t, func() {
		s.Settle(d, issNum, 0, result)
	})

	if !strings.Contains(out, "landing="+prURL) {
		t.Errorf("console output must print landing=%s; got: %q", prURL, out)
	}
	if strings.Contains(out, "pr=") {
		t.Errorf("console output must not use the stale pr= label; got: %q", out)
	}
}

// TestSettle_UsageMissing_NoCrash verifies that Settle still posts whatever
// UsageReport returns (including its "unavailable" fallback body) without
// crashing.
func TestSettle_UsageMissing_NoCrash(t *testing.T) {
	const issNum = "7"
	const prURL = "https://github.com/owner/repo/pull/7"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	d.UsageReportBody = "## Run usage\n\nModel: `unknown`\n\nUsage data unavailable (no result event in log)."
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: issNum, Landing: prURL, Status: "blocked", Note: "no result"},
	}

	s := New(baseConfig(), fc, fc)
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
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: issNum, Landing: prURL, Status: "ready", Note: "ok"},
	}

	c := baseConfig()
	s := New(c, fc, fc)
	s.Settle(d, issNum, 0, result)

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(fc.CommentCalls))
	}
	if fc.CommentCalls[0].Body != d.UsageReportBody {
		t.Errorf("comment body: got %q, want %q", fc.CommentCalls[0].Body, d.UsageReportBody)
	}
}

// TestSettle_MalformedOutcome_NoPanic verifies that a ParseErr result is
// reported and returns without attempting any gate logic.
func TestSettle_MalformedOutcome_NoPanic(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	result := dispatch.Result{ParseErr: errFake}

	s := New(baseConfig(), fc, fc)
	s.Settle(dispatch.NewFake(), "9", 0, result)

	if len(fc.CommentCalls) != 0 {
		t.Errorf("malformed outcome must not post a usage comment; got %+v", fc.CommentCalls)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("malformed outcome must not transition state; got %+v", fc.TransitionStateCalls)
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
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "1", Landing: branch, Status: "merged", Note: "ok"},
	}

	s := New(baseConfig(), fc, fc.AsPushOnly())
	s.Settle(d, "1", 0, result)

	iss, _ := fc.Issue("1")
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue 1 must NOT have agent-failed; got labels=%v", iss.Labels)
	}
}

// TestSettle_NoOutcome_AdoptsDiscoveredPR verifies that a box exiting with no
// outcome line falls back to discovering an open non-draft PR on the issue's
// branch and running the merge gate on it (SettleAdopted).
func TestSettle_NoOutcome_AdoptsDiscoveredPR(t *testing.T) {
	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{"agent-in-progress"}})
	branch := fc.AgentBranch("3")
	fc.SetPR(branch, forge.PR{URL: testPR, IsDraft: false})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	c := baseConfig()
	s := New(c, fc, fc)
	s.Settle(dispatch.NewFake(), "3", 0, dispatch.Result{Success: true})

	if fc.Merged != testPR {
		t.Errorf("expected the discovered PR to be merged; fc.Merged=%q", fc.Merged)
	}
}

// TestSettle_NoOutcome_NoPRFound reports status=missing and takes no action
// when no outcome line and no open PR exist.
func TestSettle_NoOutcome_NoPRFound(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "4", Labels: []string{"agent-in-progress"}})

	c := baseConfig()
	s := New(c, fc, fc)
	s.Settle(dispatch.NewFake(), "4", 0, dispatch.Result{Success: true})

	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("no-PR case must not trigger label churn; got %v", fc.TransitionStateCalls)
	}
	if len(fc.CommentCalls) != 0 {
		t.Errorf("no-PR case must not post a usage comment; got %v", fc.CommentCalls)
	}
}

// TestSettle_NoOutcome_DraftPRBlocked reports status=blocked and takes no
// action when the only discoverable PR is a draft.
func TestSettle_NoOutcome_DraftPRBlocked(t *testing.T) {
	fc := forge.NewFake()
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "5", Labels: []string{"agent-in-progress"}})
	branch := fc.AgentBranch("5")
	fc.SetPR(branch, forge.PR{URL: testPR, IsDraft: true})

	c := baseConfig()
	s := New(c, fc, fc)
	s.Settle(dispatch.NewFake(), "5", 0, dispatch.Result{Success: true})

	if fc.Merged != "" {
		t.Errorf("draft PR must not be merged; fc.Merged=%q", fc.Merged)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("draft PR must not trigger label churn; got %v", fc.TransitionStateCalls)
	}
}

var errFake = fakeErr("fake error")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }
