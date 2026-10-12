package settle

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

func TestTally_SettledRecordsOutcome(t *testing.T) {
	tally := &Tally{}
	t.Cleanup(InstallTally(tally))

	Settled(dispatchkey.Issue("7"), "", claude.DispatchSettled{
		State:  forge.Complete.String(),
		Reason: ReasonMerged,
		PRURL:  "https://example.test/pr/1",
	})

	want := []DispatchOutcome{{
		Key:    dispatchkey.Issue("7"),
		State:  forge.Complete.String(),
		Reason: ReasonMerged,
		PRURL:  "https://example.test/pr/1",
	}}
	got := tally.Outcomes()
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Outcomes() = %+v, want %+v", got, want)
	}
}

func TestTally_LastWriteWinsKeepingFirstSettledPosition(t *testing.T) {
	tally := &Tally{}
	t.Cleanup(InstallTally(tally))

	Settled(dispatchkey.Issue("1"), "", claude.DispatchSettled{State: forge.Failed.String(), Reason: ReasonCIRed})
	Settled(dispatchkey.Issue("2"), "", claude.DispatchSettled{State: forge.Complete.String(), Reason: ReasonMerged})
	Settled(dispatchkey.Issue("1"), "", claude.DispatchSettled{State: forge.Complete.String(), Reason: ReasonMerged})

	got := tally.Outcomes()
	if len(got) != 2 {
		t.Fatalf("Outcomes() = %+v, want 2 entries", got)
	}
	if got[0].Key != dispatchkey.Issue("1") || got[0].State != forge.Complete.String() {
		t.Errorf("got[0] = %+v, want issue 1 overwritten to complete in first position", got[0])
	}
	if got[1].Key != dispatchkey.Issue("2") {
		t.Errorf("got[1] = %+v, want issue 2", got[1])
	}
}

func TestTally_OutcomesReturnsCopy(t *testing.T) {
	tally := &Tally{}
	t.Cleanup(InstallTally(tally))
	Settled(dispatchkey.Issue("1"), "", claude.DispatchSettled{State: forge.Complete.String()})

	tally.Outcomes()[0].State = "mutated"

	if got := tally.Outcomes()[0].State; got != forge.Complete.String() {
		t.Fatalf("State = %q, want the stored record untouched", got)
	}
}

func TestTally_SettledWithoutInstalledTallyDoesNotPanic(t *testing.T) {
	t.Cleanup(InstallTally(nil))

	Settled(dispatchkey.Issue("7"), "", claude.DispatchSettled{State: forge.Complete.String()})
}

func TestTally_InstallRestoresPrevious(t *testing.T) {
	outer := &Tally{}
	t.Cleanup(InstallTally(outer))

	restore := InstallTally(&Tally{})
	restore()
	Settled(dispatchkey.Issue("1"), "", claude.DispatchSettled{State: forge.Complete.String()})

	if len(outer.Outcomes()) != 1 {
		t.Fatalf("outer tally not restored: %+v", outer.Outcomes())
	}
}

func settleReadyResult(num, branch string) dispatch.Result {
	return dispatch.Result{Resolved: outcome.Resolved{
		Found:   true,
		Outcome: outcome.Outcome{Issue: num, Landing: branch, Status: "ready", Note: "ok"},
	}}
}

// blockHandoff never transitions the issue, so Settled never runs; the
// banner must still see the Dispatch as undelivered (issue #5145).
func TestTally_ReadOnlyMissingBundleRecordsMergeBlocked(t *testing.T) {
	const num = "1919"
	tally := &Tally{}
	t.Cleanup(InstallTally(tally))

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: num, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = forge.ErrBundleNotFound

	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(n string) string { return "/outbox/" + n }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(dispatch.NewFake(), num, 0, settleReadyResult(num, "agent/issue-1919"))

	want := DispatchOutcome{Key: dispatchkey.Issue(num), State: forge.InProgress.String(), Reason: ReasonMergeBlocked}
	got := tally.Outcomes()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Outcomes() = %+v, want [%+v]", got, want)
	}
}

func TestTally_PushOnlyLandingFailureRecordsCompleteMergeBlocked(t *testing.T) {
	const num = "1"
	tally := &Tally{}
	t.Cleanup(InstallTally(tally))

	c := baseConfig()
	c.MergeMode = "immediate"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: num, Labels: []string{"agent-in-progress"}})
	fc.MergeErr = errors.New("remote rejected: non-fast-forward")
	s := newTestSettle(c, fc, fc.AsPushOnly())
	s.Settle(dispatch.NewFake(), num, 0, settleReadyResult(num, "agent/issue-1"))

	want := DispatchOutcome{Key: dispatchkey.Issue(num), State: forge.Complete.String(), Reason: ReasonMergeBlocked}
	got := tally.Outcomes()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Outcomes() = %+v, want [%+v]", got, want)
	}
}

// An off-script Box status never transitions the issue, so the banner must
// still see the Dispatch as undelivered; its Reason stays empty because
// o.Status is Box-controlled.
func TestTally_OffScriptStatusRecordsInProgressWithoutReason(t *testing.T) {
	const num = "2020"
	tally := &Tally{}
	t.Cleanup(InstallTally(tally))

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: num, Labels: []string{"agent-in-progress"}})
	res := dispatch.Result{Resolved: outcome.Resolved{
		Found:   true,
		Outcome: outcome.Outcome{Issue: num, Landing: "agent/issue-2020", Status: "failed: <script>", Note: "ok"},
	}}
	s := newTestSettle(baseConfig(), fc, fc)
	s.Settle(dispatch.NewFake(), num, 0, res)

	want := DispatchOutcome{Key: dispatchkey.Issue(num), State: forge.InProgress.String()}
	got := tally.Outcomes()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Outcomes() = %+v, want [%+v]", got, want)
	}
}
