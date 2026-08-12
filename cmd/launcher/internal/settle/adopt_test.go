package settle

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
)

// TestSettleAdopted_ConsoleUsesLandingLabel verifies that SettleAdopted's
// operator-report console print uses the landing= label, not the stale pr=
// label (issue #655) — prURL here may be a res.URL discovery, not always
// literally a PR under the wire grammar's landing vocabulary.
func TestSettleAdopted_ConsoleUsesLandingLabel(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "77", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)

	out := testutil.CaptureStdout(t, func() {
		s.SettleAdopted(dispatch.NewFake(), "77", 0, testPR)
	})

	if !strings.Contains(out, "landing="+testPR) {
		t.Errorf("console output must print landing=%s; got: %q", testPR, out)
	}
	if stalePRLabel.MatchString(out) {
		t.Errorf("console output must not use the stale pr= label; got: %q", out)
	}
}

// TestSettleAdopted_ImmediateMergeFailureStaysComplete verifies that
// SettleAdopted in immediate mode does not demote the issue to agent-failed
// when the merge itself fails after CI goes green (spec: merge-blocked stays
// at agent-complete).
func TestSettleAdopted_ImmediateMergeFailureStaysComplete(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 0
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	// A leading PENDING proves this run's own checks registered — issue
	// #1652's adopted-path gate does not trust an immediate SUCCESS alone.
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	fc.MergeErr = errors.New("required review missing")
	s := New(c, fc, fc)

	s.SettleAdopted(dispatch.NewFake(), "1", 0, testPR)

	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after green+merge-failure; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed after merge failure on green PR; labels=%v", iss.Labels)
	}
}

// TestSettleAdopted_ManualModeStaysComplete verifies that SettleAdopted in
// manual (and auto) mode leaves the issue at agent-complete and never swaps
// it to agent-failed after CI reaches green without a merge.
func TestSettleAdopted_ManualModeStaysComplete(t *testing.T) {
	for _, mode := range []string{"manual", "auto"} {
		t.Run(mode, func(t *testing.T) {
			c := baseConfig()
			c.MergeMode = mode
			fc := forge.NewFake(testDispatchLabels)
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
			// A leading PENDING proves this run's own checks registered —
			// issue #1652's adopted-path gate does not trust an immediate
			// SUCCESS alone.
			fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
			s := New(c, fc, fc)

			s.SettleAdopted(dispatch.NewFake(), "1", 0, testPR)

			iss, _ := fc.Issue("1")
			if !containsLabel(iss.Labels, "agent-complete") {
				t.Errorf("mode=%s: issue must carry agent-complete after green; labels=%v", mode, iss.Labels)
			}
			if containsLabel(iss.Labels, "agent-failed") {
				t.Errorf("mode=%s: issue must NOT carry agent-failed after green in non-immediate mode; labels=%v", mode, iss.Labels)
			}
		})
	}
}

// TestSettleAdopted_RedFollowsSelfHeal verifies that a red CI on an adopted
// PR is demoted to agent-failed once fix passes are exhausted. Also asserts
// (issue #2328) that SettleAdopted's landingFailed print carries
// selfHealAdopted's own classified reason rather than the old hardcoded "CI
// or merge failed" literal.
func TestSettleAdopted_RedFollowsSelfHeal(t *testing.T) {
	c := baseConfig()
	c.MaxFixAttempts = 0 // no fix passes — just mark failed
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "77", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	s := New(c, fc, fc)

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	s.SettleAdopted(dispatch.NewFake(), "77", 0, testPR)
	w.Close()
	os.Stdout = old
	captured, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	stdout := string(captured)

	if fc.Merged != "" {
		t.Errorf("expected no merge on red CI; fc.Merged=%q", fc.Merged)
	}
	if len(fc.TransitionStateCalls) == 0 {
		t.Fatal("expected TransitionState call for failedLabel")
	}
	if last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]; last.To != forge.Failed {
		t.Errorf("last transition To=%v, want Failed", last.To)
	}
	if strings.Contains(stdout, "CI or merge failed") {
		t.Errorf("stdout must not contain the old hardcoded literal, got: %s", stdout)
	}
	if !strings.Contains(stdout, "ci-red: still red after exhausting 0 fix pass(es)") {
		t.Errorf("stdout must contain selfHealAdopted's classified reason, got: %s", stdout)
	}
}

// TestSettleAdopted_StaleSuccessMergesAfterWindow verifies the issue #2475
// fix directly at the SettleAdopted seam: an adopted PR whose rollup reads
// SUCCESS on every poll, with no PENDING/EXPECTED/NONE ever proving this
// run's own checks registered, still merges once the bounded registration
// window (registrationWindowPolls) elapses — a settled SUCCESS that never
// produces a fresh non-terminal poll is treated as proof CI already
// finished, not proof it's still mid-registration, so it no longer times out
// and demotes the issue the way issue #1652's original absolute guard did.
func TestSettleAdopted_StaleSuccessMergesAfterWindow(t *testing.T) {
	c := baseConfig()
	c.MergePollTimeout = 10 // comfortably longer than registrationWindowPolls(3) * actualIv(1)
	c.MaxFixAttempts = 0
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)

	s.SettleAdopted(dispatch.NewFake(), "1", 0, testPR)

	if fc.Merged != testPR {
		t.Errorf("expected the settled SUCCESS rollup to merge once the registration window elapses; fc.Merged=%q, want %q", fc.Merged, testPR)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must reach agent-complete after the window-elapsed merge; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT be demoted to agent-failed; labels=%v", iss.Labels)
	}
}

// TestSettleAdopted_StaleSuccessStillTimesOutWithinWindow is the "still
// waits" counterpart to TestSettleAdopted_StaleSuccessMergesAfterWindow: the
// deadline (MergePollTimeout==1) is shorter than the registration window
// (registrationWindowPolls(3) * actualIv(1) == 3), so the guard must still be
// withholding trust when the deadline is hit, unlike the sibling test where
// MergePollTimeout(10) leaves comfortable slack past the window for the
// SUCCESS to get accepted. An all-SUCCESS rollup that hasn't yet cleared the
// registration window must be rejected (times out, demotes to
// agent-failed), not merged.
func TestSettleAdopted_StaleSuccessStillTimesOutWithinWindow(t *testing.T) {
	c := baseConfig()
	c.MergePollTimeout = 1 // less than registrationWindowPolls(3) * actualIv(1)
	c.MaxFixAttempts = 0
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)

	s.SettleAdopted(dispatch.NewFake(), "1", 0, testPR)

	if fc.Merged != "" {
		t.Errorf("expected no merge before the registration window elapses; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must be demoted to agent-failed when the deadline is hit before the registration window elapses; labels=%v", iss.Labels)
	}
}

// TestSettleAdopted_PushOnlyForgeSkipsVerify verifies that SettleAdopted's
// landingMerged case guards the verifyMerged call against a push-only
// forge's nil s.pr (issue #697), mirroring gate.go's "ready" case guard
// (silent skip, no logging when s.pr is nil).
func TestSettleAdopted_PushOnlyForgeSkipsVerify(t *testing.T) {
	const branch = "agent/issue-1"

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})

	c := baseConfig()
	s := New(c, fc, fc.AsPushOnly())

	s.SettleAdopted(dispatch.NewFake(), "1", 0, branch)

	iss, _ := fc.Issue("1")
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue 1 must NOT have agent-failed; got labels=%v", iss.Labels)
	}
}

// TestSettleAdopted_GreenMergesAndCompletes verifies the green-CI path merges
// the adopted PR and reaches agent-complete without dispatching any fix pass.
func TestSettleAdopted_GreenMergesAndCompletes(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "77", Labels: []string{"agent-in-progress"}})
	// A leading PENDING proves this run's own checks registered — issue
	// #1652's adopted-path gate does not trust an immediate SUCCESS alone.
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	s.SettleAdopted(d, "77", 0, testPR)

	if fc.Merged != testPR {
		t.Errorf("expected PR to be merged; fc.Merged=%q", fc.Merged)
	}
	if len(d.FixCalls) != 0 {
		t.Errorf("expected no fix calls on green CI, got %+v", d.FixCalls)
	}
	if len(fc.TransitionStateCalls) == 0 {
		t.Fatal("expected TransitionState call for completeLabel")
	}
	if last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]; last.To != forge.Complete {
		t.Errorf("last transition To=%v, want Complete", last.To)
	}
}
