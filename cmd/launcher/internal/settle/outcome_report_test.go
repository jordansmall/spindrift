package settle

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/testutil"
)

func mergedResult(issNum string) dispatch.Result {
	return dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "ignored", Status: "merged", Note: "ok"},
		},
	}
}

func TestSettle_MergedStatus_PROpen_ReportsFailed(t *testing.T) {
	const issNum = "51"
	const prURL = "https://github.com/owner/repo/pull/51"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	// agent-complete is present so the PR state is the only failing check.
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress", "agent-complete"}})
	fc.SetPR(fc.AgentBranch(issNum), forge.PR{URL: prURL})
	fc.SetPRState(prURL, forge.PROpen)

	s := newTestSettle(baseConfig(), fc, fc)
	out := testutil.CaptureStdout(t, func() {
		s.Settle(dispatch.NewFake(), issNum, 0, mergedResult(issNum))
	})

	for _, want := range []string{"status=failed", "!!", "expected MERGED"} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q; got: %q", want, out)
		}
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("unmerged PR must demote to agent-failed; got labels=%v", iss.Labels)
	}
}

func TestSettle_MergedStatus_MissingCompleteLabel_ReportsFailed(t *testing.T) {
	const issNum = "52"
	const prURL = "https://github.com/owner/repo/pull/52"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.SetPR(fc.AgentBranch(issNum), forge.PR{URL: prURL})
	fc.SetPRState(prURL, forge.PRMerged)

	s := newTestSettle(baseConfig(), fc, fc)
	out := testutil.CaptureStdout(t, func() {
		s.Settle(dispatch.NewFake(), issNum, 0, mergedResult(issNum))
	})

	for _, want := range []string{"status=failed", "!!", "does not carry"} {
		if !strings.Contains(out, want) {
			t.Errorf("output must contain %q; got: %q", want, out)
		}
	}
	iss, _ := fc.Issue(issNum)
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("merged PR without agent-complete must demote to agent-failed; got labels=%v", iss.Labels)
	}
}

func TestSettle_BlockedOutcome_ReportLinePrintsNote(t *testing.T) {
	const issNum = "53"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: "agent/issue-53", Status: "blocked", Note: "stalled"},
		},
	}

	s := newTestSettle(baseConfig(), fc, fc)
	out := testutil.CaptureStdout(t, func() {
		s.Settle(dispatch.NewFake(), issNum, 0, result)
	})

	if want := "status=blocked  !! stalled"; !strings.Contains(out, want) {
		t.Errorf("output must contain %q; got: %q", want, out)
	}
}

func TestSettle_NoUsableOutcome_Report(t *testing.T) {
	cases := []struct {
		name   string
		result dispatch.Result
	}{
		{"no outcome line", dispatch.Result{Success: true}},
		{"malformed outcome line", dispatch.Result{ParseErr: errFake}},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/no PR", func(t *testing.T) {
			fc := forge.NewFake(testDispatchLabels)
			fc.BranchPrefix = "agent/issue-"
			fc.SetIssue(forge.Issue{Number: "61", Labels: []string{"agent-in-progress"}})

			s := newTestSettle(baseConfig(), fc, fc)
			out := testutil.CaptureStdout(t, func() {
				s.Settle(dispatch.NewFake(), "61", 0, tc.result)
			})

			if !strings.Contains(out, "status=missing") {
				t.Errorf("no PR must report status=missing; got: %q", out)
			}
			iss, _ := fc.Issue("61")
			if !containsLabel(iss.Labels, "agent-failed") {
				t.Errorf("no PR must demote to agent-failed; got labels=%v", iss.Labels)
			}
		})
		t.Run(tc.name+"/open PR", func(t *testing.T) {
			fc := forge.NewFake(testDispatchLabels)
			fc.BranchPrefix = "agent/issue-"
			fc.SetIssue(forge.Issue{Number: "62", Labels: []string{"agent-in-progress"}})
			fc.SetPR(fc.AgentBranch("62"), forge.PR{URL: testPR})

			s := newTestSettle(baseConfig(), fc, fc)
			out := testutil.CaptureStdout(t, func() {
				s.Settle(dispatch.NewFake(), "62", 0, tc.result)
			})

			if !strings.Contains(out, "status=blocked") {
				t.Errorf("open PR must report status=blocked; got: %q", out)
			}
			if strings.Contains(out, "status=adopted") {
				t.Errorf("open PR must never be adopted off a missing outcome; got: %q", out)
			}
			if fc.Merged != "" {
				t.Errorf("open PR must not be merged; fc.Merged=%q", fc.Merged)
			}
			if len(fc.TransitionStateCalls) != 0 {
				t.Errorf("open PR must not trigger label churn; got %v", fc.TransitionStateCalls)
			}
		})
	}
}
