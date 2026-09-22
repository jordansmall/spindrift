package settle

import (
	"errors"
	"fmt"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/testutil"
)

// A terminal transitionState call only latches num's decision (issue #3627):
// nothing reaches the pipe until flushSettled pops it.
func TestSettle_TransitionState_Terminal_LatchesWithoutEmittingUntilFlush(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	s := New(Config{}, fc, fc)

	s.transitionState("9", forge.InProgress, forge.Failed, "")

	if recs := readRecords(); len(recs) != 0 {
		t.Fatalf("records: got %+v, want none before flushSettled pops the latch", recs)
	}
}

// flushSettled emits the latched terminal decision exactly once, carrying the
// note passed to transitionState, whether or not the tracker write itself
// succeeded (see transitionState's comment for why).
func TestSettle_FlushSettled_EmitsLatchedRecordOnce(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	s := New(Config{}, fc, fc)

	s.transitionState("9", forge.InProgress, forge.Failed, "some reason")
	s.flushSettled("9")
	// A second flush must not re-emit: the pop already drained the latch.
	s.flushSettled("9")

	recs := readRecords()
	if len(recs) != 1 {
		t.Fatalf("records: got %d, want exactly 1: %+v", len(recs), recs)
	}
	if recs[0].Event != "settled" || recs[0].Issue != "9" || recs[0].State != "failed" || recs[0].Note != "some reason" {
		t.Errorf("record = %+v, want event=settled issue=9 state=failed note=%q", recs[0], "some reason")
	}
}

// Two terminal transitionState calls for the same issue latch last-write-wins
// (issue #3627): flushSettled must report only the second (tracker's final)
// decision, never both.
func TestSettle_TransitionState_SecondTerminalCallOverwritesLatch(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	s := New(Config{}, fc, fc)

	s.transitionState("9", forge.InProgress, forge.Complete, "")
	s.transitionState("9", forge.InProgress, forge.Failed, "demoted")
	s.flushSettled("9")

	recs := readRecords()
	if len(recs) != 1 {
		t.Fatalf("records: got %d, want exactly 1 (last write wins): %+v", len(recs), recs)
	}
	if recs[0].State != "failed" || recs[0].Note != "demoted" {
		t.Errorf("record = %+v, want state=failed note=demoted", recs[0])
	}
}

// A non-terminal transitionState call (e.g. Untriaged -> Dispatchable, the
// promotion path) latches nothing, so flushSettled emits no record: settle's
// report stream is only interested in terminal outcomes.
func TestSettle_TransitionState_NonTerminal_EmitsNoRecord(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "9"})
	s := New(Config{}, fc, fc)

	s.transitionState("9", forge.Untriaged, forge.Dispatchable, "")
	s.flushSettled("9")

	if recs := readRecords(); len(recs) != 0 {
		t.Fatalf("records: got %+v, want none", recs)
	}
}

// Even a failed tracker write still reports what the host decided (issue
// #3627): the record is an operator-visible statement of intent, not a
// confirmation the label landed.
func TestSettle_TransitionState_Terminal_EmitsRecordEvenOnTrackerError(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	fc.TransitionStateErr = fmt.Errorf("boom")
	s := New(Config{}, fc, fc)

	s.transitionState("9", forge.InProgress, forge.Failed, "")
	s.flushSettled("9")

	recs := readRecords()
	if len(recs) != 1 || recs[0].State != "failed" {
		t.Fatalf("records = %+v, want one settled/failed record despite the tracker error", recs)
	}
}

// The blocking review finding at gate.go:227 (issue #3627): a green self-heal
// that lands Complete (completeLanding) and is then demoted by verifyMerged's
// PR-state re-check (adopt.go) must reach the daemon as exactly one settled
// record carrying the tracker's final decision, state=failed — never two
// contradicting records.
func TestSettleAdopted_CompleteThenDemoted_EmitsSingleFailedSettledRecord(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 0
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	// The leading PENDING proves this run's own checks registered (issue
	// #1652). PRStateErr models the transient PRState error adopt.go:37
	// describes: verifyMerged swallows it into prState == "" and demotes,
	// even though the Merge call just above it succeeded.
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)
	fc.PRStateErr = errors.New("transient: PR lookup failed")

	s.SettleAdopted(dispatch.NewFake(), "9", 0, testPR)

	recs := readRecords()
	if len(recs) != 1 {
		t.Fatalf("records: got %d, want exactly 1 (completeLanding's Complete must never itself reach the daemon): %+v", len(recs), recs)
	}
	if recs[0].Event != "settled" || recs[0].Issue != "9" || recs[0].State != "failed" {
		t.Errorf("record = %+v, want event=settled issue=9 state=failed (verifyMerged's demotion, not completeLanding's earlier Complete)", recs[0])
	}
}

// The blocking review finding at gate.go:227 (issue #3627): settleUnresolved's
// "no outcome in log" park must carry that reason into the settled record's
// note, so the settle outcome is diagnosable on disk without the daemon's
// terminal.
func TestSettle_SettleUnresolved_NoOutcomeNote_ReachesSettledRecord(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	s := newTestSettle(baseConfig(), fc, fc)

	result := dispatch.Result{
		Resolved:    outcome.Resolved{Found: false},
		ClassifyErr: errors.New("classify boom"),
	}
	s.Settle(dispatch.NewFake(), "9", 0, result)

	recs := readRecords()
	if len(recs) != 1 {
		t.Fatalf("records: got %d, want 1: %+v", len(recs), recs)
	}
	if recs[0].State != "failed" || recs[0].Note != "no outcome in log" {
		t.Errorf("record = %+v, want state=failed note=%q", recs[0], "no outcome in log")
	}
}

// research's fail() carries its note into the settled record's Note field
// (the one case the issue's own comment calls out by name).
func TestResearchSettle_Fail_CarriesNoteIntoSettledRecord(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fc := newResearchFake("42")
	result := dispatch.Result{Success: true, Resolved: outcome.Resolved{Found: false}}

	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels, false)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	recs := readRecords()
	if len(recs) != 1 {
		t.Fatalf("records: got %d, want 1: %+v", len(recs), recs)
	}
	want := report.Record{Event: "settled", Issue: "42", State: "failed", Note: "no verdict outcome line"}
	if recs[0] != want {
		t.Errorf("record = %+v, want %+v", recs[0], want)
	}
}

// research's successful verdict tail carries the verdict itself in the
// settled record's note (state stays DispatchState-only: "complete").
func TestResearchSettle_Recommend_EmitsSettledRecordWithVerdictNote(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fc := newResearchFake("42")
	result := dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "42", Landing: "https://github.com/owner/repo/issues/42#issuecomment-1", Status: "recommend", Note: "grounded in code"},
		},
	}

	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels, false)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	recs := readRecords()
	if len(recs) != 1 {
		t.Fatalf("records: got %d, want 1: %+v", len(recs), recs)
	}
	want := report.Record{Event: "settled", Issue: "42", State: "complete", Note: "verdict recommend"}
	if recs[0] != want {
		t.Errorf("record = %+v, want %+v", recs[0], want)
	}
}
