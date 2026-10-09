package settle

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/testutil"
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

// The daemon parses a butler child's records as chore-keyed (issue #4966), so a
// patch gate's ci_wait must carry the owning Chore's key, and the gate must add
// no settled record: the Chore already sent its one terminal record.
func TestSettlePatch_ReportsUnderOwnerAndEmitsNoSettled(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 0
	c.Unclaimed = true
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "5", Labels: append([]string{}, butlerPatchLabels...)})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)
	owner := dispatchkey.Chore("dead-code")

	testutil.CaptureStdout(t, func() { s.SettlePatch(owner, "5", testPR) })

	if fc.Merged != testPR {
		t.Errorf("expected PR to be merged; fc.Merged=%q", fc.Merged)
	}
	recs := readRecords()
	waits := 0
	for _, r := range recs {
		if r.Key != owner {
			t.Errorf("record = %+v, want key %v", r, owner)
		}
		if r.Event != report.EventCIWait {
			t.Errorf("unexpected %q record %+v from the patch gate", r.Event, r)
			continue
		}
		waits++
	}
	if waits == 0 {
		t.Errorf("want a ci_wait record keyed by the owner; got %+v", recs)
	}
	if len(s.reportKey) != 0 || len(s.prLatch) != 0 {
		t.Errorf("latches not flushed: reportKey=%v prLatch=%v", s.reportKey, s.prLatch)
	}
}

// A nil-Dispatcher settle that no Chore owns (no SettlePatch) still reports its
// terminal record under the issue key: only an owned issue suppresses it.
func TestSettleAdopted_NilDispatcherUnownedEmitsIssueKeyedSettled(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	s := newTestSettle(c, fc, fc)

	testutil.CaptureStdout(t, func() { s.SettleAdopted(nil, "1", 0, testPR) })

	want := dispatchkey.Issue("1")
	for _, r := range readRecords() {
		if r.Event == report.EventSettled && r.Key == want {
			return
		}
	}
	t.Errorf("want a settled record keyed %v from an unowned nil-Dispatcher settle", want)
}
