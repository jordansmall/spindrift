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
	"spindrift.dev/launcher/internal/testutil"
)

// stalePRLabel matches a genuine stale pr= field (issue #892) without
// tripping on a benign substring like expr= or repr= inside free-text
// note/error interpolations.
var stalePRLabel = regexp.MustCompile(`\bpr=`)

// Uses a github-shaped tracker (AsNoLandingRecorder): a local tracker's blocked
// path posts an additional note comment
// (TestSettle_LocalForge_BlockedPostsNoteAsComment) that would muddy this
// usage-comment-specific assertion.
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

// A status=blocked outcome (including the synthetic backstop's) must land the
// issue in the human-triage queue instead of looking in-flight forever
// (issue #1605, observed on #1542).
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

// The operator-report console print uses the landing= label (matching the wire
// grammar's o.Landing field): the value may not even be a PR (issue #655).
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

// Settle still posts whatever UsageReport returns, including its "unavailable"
// fallback body. Github-shaped tracker for the same reason as
// TestSettle_PostsUsageComment_Blocked.
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

// A deterministic backstop to GitHub's merged-PR auto-close, for the case
// (issue #1892) where the agent PR's body omitted or reworded Closes #<N>.
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

// ISSUE_TRACKER=local paired with a PRForge Code Forge is a valid combination
// (CODE_FORGE=github, say). Only reconcile's sweep may write local's closed:
// axis, so settle's backstop is scoped to forge.MergeCloser — which the local
// adapter's shape (AsLocalShaped) does not implement, even though it does
// implement IssueCloser.
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

// manual/auto MergeMode leaves the PR open (landingManual, never
// landingMerged), and issue #1892's backstop must fire only after a confirmed
// merge, not merely a green CI run.
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

// Also pins issue #2328: the "ready" case's landingFailed print must carry
// selfHeal's own classified reason, not a hardcoded "CI or merge failed".
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

// A box that mangled its outcome line AND never opened a PR has produced
// nothing landable, so it must demote exactly like a genuinely missing outcome
// line does — never a silent no-op (issue #1898).
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

// A box that exited zero but emitted an unparseable outcome line still runs
// the PR-adoption check: an open PR must be reported status=blocked, not
// silently dropped under status=malformed (issue #1898, observed on #1895 /
// PR #1897 — a clean, green, mergeable PR left un-adopted).
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

// A push-only forge's PRState always errors, so an unguarded verifyMerged call
// would wrongly demote the issue to agent-failed with nothing actually wrong.
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

// A no-outcome run is never adopted off draft-ness (issue #1654); adoption
// only happens via the explicit agent-recover entry point (SettleAdopted).
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

// No outcome line and no open PR means the Driver crashed before ever opening
// one, so there is nothing left to adopt (issue #1605).
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

// Unlike a confirmed absence of a PR, a lookup error leaves genuine doubt
// about whether a live, mergeable PR exists — demoting would bury a
// possibly-fine run under agent-failed (issue #1605 review follow-up).
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

// A lookup failure must not drop the class=/reason= diagnostic detail a human
// triaging agent-failed relies on; the confirmed-no-PR branch prints it too.
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

// A push-only Code Forge has no PRForge surface, so ResolveOpenPR always
// reports not-found — the same "no adoptable PR exists" case a github forge
// hits when no PR was opened, and issue #1605's demotion must fire there too.
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

// LandingRecorder is optional (ADR 0029); exercised here on the simplest
// "blocked" outcome path.
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

// recordLanding sits ahead of the status switch, so every work outcome status
// records the landing ref, not just "blocked".
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

// Matches the github/jira adapters' shape: no LandingRecorder at all.
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

// A SPINDRIFT_ISSUE_INTENT line that carried the token but failed nonce
// verification is surfaced only via Result.IssueIntentsRejected (issue #2976);
// without a warning naming the channel and count it leaves no trace at all.
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

// gate.go's logRejectedSignals warns only when a verifying match was also found
// on the channel (CommentFound true): dispatch.outcomeResult's comment-scan
// warning (retry.go) already covers CommentFound=false, so warning there too
// would double-report.
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
