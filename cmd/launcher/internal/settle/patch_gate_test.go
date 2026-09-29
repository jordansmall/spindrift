package settle

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// butlerPatchLabels is the label set a butler patch finding's issue actually
// carries (ADR 0057, issue #4076): never agent-in-progress, since the issue
// was never claimed.
var butlerPatchLabels = []string{"agent-butler-finding", "agent-butler-patch"}

// A red gate on an unclaimed PR (issue #4076) must comment the failure rather
// than commit agent-failed: there is no InProgress state to leave, and no
// dispatcher to run a fix pass with.
func TestSettleAdopted_UnclaimedRedComments(t *testing.T) {
	c := baseConfig()
	c.MaxFixAttempts = 0
	c.Unclaimed = true
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: append([]string{}, butlerPatchLabels...)})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	s := newTestSettle(c, fc, fc)
	d := dispatch.NewFake()

	s.SettleAdopted(d, "1", 0, testPR)

	if fc.Merged != "" {
		t.Errorf("expected no merge on red CI; fc.Merged=%q", fc.Merged)
	}
	if len(fc.MarkReadyCalls) != 0 {
		t.Errorf("expected the PR to stay draft (no MarkReady call) on red CI; got %v", fc.MarkReadyCalls)
	}
	if len(d.FixCalls) != 0 {
		t.Errorf("expected no fix pass on an unclaimed issue; got %v", d.FixCalls)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("expected no TransitionState calls on an unclaimed issue; got %+v", fc.TransitionStateCalls)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("expected exactly one failure comment, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
	if !strings.Contains(fc.CommentCalls[0].Body, "ci-red") {
		t.Errorf("comment body = %q, want a substring containing %q", fc.CommentCalls[0].Body, "ci-red")
	}
	iss, _ := fc.Issue("1")
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed on an unclaimed red gate; labels=%v", iss.Labels)
	}
}

// A red gate on an unclaimed PR with a nonzero MaxFixAttempts (a caller that
// forgot to pair the two, issue #4076) must not panic reaching for a fix
// pass: settle.New clamps MaxFixAttempts to 0 whenever Unclaimed is true, so
// the nil Dispatcher here is never dereferenced.
func TestSettleAdopted_UnclaimedIgnoresNonzeroMaxFixAttempts(t *testing.T) {
	c := baseConfig()
	c.MaxFixAttempts = 3
	c.Unclaimed = true
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: append([]string{}, butlerPatchLabels...)})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	s := newTestSettle(c, fc, fc)

	s.SettleAdopted(nil, "1", 0, testPR)

	if len(fc.MarkReadyCalls) != 0 {
		t.Errorf("expected the PR to stay draft (no MarkReady call) on red CI; got %v", fc.MarkReadyCalls)
	}
}

// A gate-terminal failure (e.g. CheckState timeout) on an unclaimed PR must
// also comment without transitioning state (issue #4076); the gateTerminal
// arm already comments regardless of Unclaimed, so this pins that no double
// path and no TransitionState call happen.
func TestSettleAdopted_UnclaimedGateTerminalComments(t *testing.T) {
	c := baseConfig()
	c.MergePollTimeout = 1 // less than registrationWindowPolls(3) * actualIv(1)
	c.MaxFixAttempts = 0
	c.Unclaimed = true
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: append([]string{}, butlerPatchLabels...)})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)

	s.SettleAdopted(nil, "1", 0, testPR)

	if fc.Merged != "" {
		t.Errorf("expected no merge before the registration window elapses; fc.Merged=%q", fc.Merged)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("expected no TransitionState calls on an unclaimed issue; got %+v", fc.TransitionStateCalls)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("expected exactly one comment posted on gate-terminal failure, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
	iss, _ := fc.Issue("1")
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed on an unclaimed gate-terminal failure; labels=%v", iss.Labels)
	}
}

// A green gate on an unclaimed PR under MERGE_MODE=immediate still merges and
// completes the issue (issue #4076): Complete is the one transition an
// unclaimed issue does commit, since it reflects the PR having actually
// landed rather than a claim being released.
func TestSettleAdopted_UnclaimedGreenImmediateMerges(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 0
	c.Unclaimed = true
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: append([]string{}, butlerPatchLabels...)})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)

	s.SettleAdopted(nil, "1", 0, testPR)

	if fc.Merged != testPR {
		t.Errorf("expected PR to be merged; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after an unclaimed merge; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed; labels=%v", iss.Labels)
	}
	if len(fc.CloseMergedIssueCalls) != 1 {
		t.Errorf("expected the merged issue to be closed via MergeCloser; got %v", fc.CloseMergedIssueCalls)
	}
}

// Green-but-manual/auto on an unclaimed PR must mark the PR ready without
// ever touching tracker state: manual/auto never actually lands the PR, so
// there is nothing for an unclaimed issue's gate to commit (issue #4076).
func TestSettleAdopted_UnclaimedGreenManualAndAutoLeaveIssueUntouched(t *testing.T) {
	cases := []struct {
		mode          string
		wantAutoMerge bool
	}{
		{mode: "manual"},
		{mode: "auto", wantAutoMerge: true},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			c := baseConfig()
			c.MergeMode = tc.mode
			c.Unclaimed = true
			fc := forge.NewFake(testDispatchLabels)
			fc.SetIssue(forge.Issue{Number: "1", Labels: append([]string{}, butlerPatchLabels...)})
			fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
			s := newTestSettle(c, fc, fc)

			s.SettleAdopted(nil, "1", 0, testPR)

			if fc.Merged != "" {
				t.Errorf("mode=%s: expected no merge; fc.Merged=%q", tc.mode, fc.Merged)
			}
			if len(fc.MarkReadyCalls) != 1 {
				t.Errorf("mode=%s: expected exactly one MarkReady call; got %v", tc.mode, fc.MarkReadyCalls)
			}
			if tc.wantAutoMerge && len(fc.EnqueueAutoMergeCalls) != 1 {
				t.Errorf("mode=%s: expected auto-merge to be enqueued; got %v", tc.mode, fc.EnqueueAutoMergeCalls)
			}
			if len(fc.TransitionStateCalls) != 0 {
				t.Errorf("mode=%s: expected no TransitionState calls; got %+v", tc.mode, fc.TransitionStateCalls)
			}
			iss, _ := fc.Issue("1")
			if !equalLabels(iss.Labels, butlerPatchLabels) {
				t.Errorf("mode=%s: labels must be left untouched; got %v, want %v", tc.mode, iss.Labels, butlerPatchLabels)
			}
		})
	}
}

// equalLabels compares two label sets order-insensitively.
func equalLabels(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !containsLabel(got, w) {
			return false
		}
	}
	return true
}
