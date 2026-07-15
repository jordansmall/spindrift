package settle

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
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

	out := captureStdout(t, func() {
		s.SettleAdopted(dispatch.NewFake(), "77", testPR)
	})

	if !strings.Contains(out, "landing="+testPR) {
		t.Errorf("console output must print landing=%s; got: %q", testPR, out)
	}
	if strings.Contains(out, "pr=") {
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
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErr = errors.New("required review missing")
	s := New(c, fc, fc)

	s.SettleAdopted(dispatch.NewFake(), "1", testPR)

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
			fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
			s := New(c, fc, fc)

			s.SettleAdopted(dispatch.NewFake(), "1", testPR)

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
// PR is demoted to agent-failed once fix passes are exhausted.
func TestSettleAdopted_RedFollowsSelfHeal(t *testing.T) {
	c := baseConfig()
	c.MaxFixAttempts = 0 // no fix passes — just mark failed
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "77", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	s := New(c, fc, fc)

	s.SettleAdopted(dispatch.NewFake(), "77", testPR)

	if fc.Merged != "" {
		t.Errorf("expected no merge on red CI; fc.Merged=%q", fc.Merged)
	}
	if len(fc.TransitionStateCalls) == 0 {
		t.Fatal("expected TransitionState call for failedLabel")
	}
	if last := fc.TransitionStateCalls[len(fc.TransitionStateCalls)-1]; last.To != forge.Failed {
		t.Errorf("last transition To=%v, want Failed", last.To)
	}
}

// TestSettleAdopted_GreenMergesAndCompletes verifies the green-CI path merges
// the adopted PR and reaches agent-complete without dispatching any fix pass.
func TestSettleAdopted_GreenMergesAndCompletes(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "77", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)

	d := dispatch.NewFake()
	s.SettleAdopted(d, "77", testPR)

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
