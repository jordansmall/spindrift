package settle

import (
	"errors"
	"fmt"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
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
	if recs[0].Event != "settled" || recs[0].Key != dispatchkey.Issue("9") || recs[0].State != "failed" || recs[0].Note != "some reason" {
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
	if recs[0].Event != "settled" || recs[0].Key != dispatchkey.Issue("9") || recs[0].State != "failed" {
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
	result := dispatch.Result{Resolved: outcome.Resolved{Found: false}}

	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels, false)
	s.Settle(dispatch.NewFake(), "42", 0, result)

	recs := readRecords()
	if len(recs) != 1 {
		t.Fatalf("records: got %d, want 1: %+v", len(recs), recs)
	}
	want := report.Record{Event: "settled", Key: dispatchkey.Issue("42"), State: "failed", Note: "no verdict outcome line"}
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
	want := report.Record{Event: "settled", Key: dispatchkey.Issue("42"), State: "complete", Note: "verdict recommend"}
	if recs[0] != want {
		t.Errorf("record = %+v, want %+v", recs[0], want)
	}
}

// A PR latched for num rides the settled record flushSettled emits.
func TestSettle_FlushSettled_CarriesLatchedPR(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	s := New(Config{}, fc, fc)

	s.latchPR("9", testPR)
	// The transition after the latch must not drop the PR.
	s.transitionState("9", forge.InProgress, forge.Failed, "red")
	s.flushSettled("9")

	recs := readRecords()
	if len(recs) != 1 || recs[0].PRURL != testPR || recs[0].State != "failed" {
		t.Fatalf("records = %+v, want one failed record with PRURL=%s", recs, testPR)
	}
}

// No PR latched leaves the record's PRURL empty.
func TestSettle_FlushSettled_NoPRWhenNoneLatched(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	s := New(Config{}, fc, fc)

	s.transitionState("9", forge.InProgress, forge.Failed, "")
	s.flushSettled("9")

	recs := readRecords()
	if len(recs) != 1 || recs[0].PRURL != "" {
		t.Fatalf("records = %+v, want one record with empty PRURL", recs)
	}
}

// A latched PR with no terminal state emits nothing, and the flush clears it so
// it does not leak into a later record.
func TestSettle_FlushSettled_UnsettledLatchedPRDoesNotLeak(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	s := New(Config{}, fc, fc)

	s.latchPR("9", testPR)
	s.flushSettled("9")
	s.transitionState("9", forge.InProgress, forge.Failed, "")
	s.flushSettled("9")

	recs := readRecords()
	if len(recs) != 1 || recs[0].PRURL != "" {
		t.Fatalf("records = %+v, want one record with empty PRURL", recs)
	}
}

// readWriteRecords settles a read-write run for issue 77 whose Box printed
// status and claimed landing, and returns the settled records. setup seeds the
// forge; the agent branch is fc.AgentBranch("77").
func readWriteRecords(t *testing.T, status, landing string, setup func(fc *forge.Fake)) []report.Record {
	t.Helper()
	readRecords := testutil.InstallPipeReporter(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: "77", Labels: []string{"agent-in-progress"}})
	setup(fc)
	s := newTestSettle(baseConfig(), fc, fc)

	s.Settle(dispatch.NewFake(), "77", 0, dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "77", Landing: landing, Status: status, Note: "ok"},
		},
	})
	return readRecords()
}

// The read-write ready path names the agent branch's open PR, resolved
// host-side; the Box's landing= is only what the gate watches.
func TestSettle_ReadWriteReady_BranchPR_Latched(t *testing.T) {
	recs := readWriteRecords(t, "ready", testPR, func(fc *forge.Fake) {
		fc.SetPR(fc.AgentBranch("77"), forge.PR{URL: testPR})
		fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	})
	if len(recs) != 1 || recs[0].PRURL != testPR {
		t.Fatalf("records = %+v, want one record with PRURL=%s", recs, testPR)
	}
}

// A Box-supplied landing= that no branch PR backs must never reach the record,
// whether or not the forge can answer a check read for it.
func TestSettle_ReadWriteReady_LandingWithoutBranchPR_NotLatched(t *testing.T) {
	for _, landing := range []string{"https://evil.example/x", testPR, "123", "agent/issue-77", "javascript:alert(1)", "ftp://x/y"} {
		recs := readWriteRecords(t, "ready", landing, func(fc *forge.Fake) {
			fc.SetCheckStates(landing, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
		})
		if len(recs) != 1 || recs[0].PRURL != "" {
			t.Errorf("landing %q: records = %+v, want one record with empty PRURL", landing, recs)
		}
	}
}

// A Box-supplied landing= differing from the branch's open PR does not
// displace it on the record.
func TestSettle_ReadWriteReady_LandingDiffersFromBranchPR_BranchPRWins(t *testing.T) {
	const evil = "https://evil.example/x"
	recs := readWriteRecords(t, "ready", evil, func(fc *forge.Fake) {
		fc.SetPR(fc.AgentBranch("77"), forge.PR{URL: testPR})
		fc.SetCheckStates(evil, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	})
	if len(recs) != 1 || recs[0].PRURL != testPR {
		t.Fatalf("records = %+v, want one record with PRURL=%s", recs, testPR)
	}
}

// A host-proven PR latches where the host produced it, so a first CheckState
// failure (a rate limit, say) still leaves the record naming it.
func TestSettle_GithubReadOnly_ReadyOpenedPR_LatchedDespiteCheckStateError(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	const issNum = "1919"
	const prURL = "https://github.com/owner/repo/pull/1919"
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL
	fc.SetCheckStateErrors(prURL, []error{errors.New("403 rate limit")})

	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "ready", Note: "ok"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}
	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	recs := readRecords()
	if len(recs) != 1 || recs[0].PRURL != prURL || recs[0].State != "failed" {
		t.Fatalf("records = %+v, want one failed record with PRURL=%s", recs, prURL)
	}
}

// SettleAdopted's prURL is host-discovered (reconcile, recover, butler patch),
// so it names the record even when the gate's first CheckState fails.
func TestSettleAdopted_CheckStateError_StillCarriesPR(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "77", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStateErrors(testPR, []error{errors.New("403 rate limit")})
	s := newTestSettle(baseConfig(), fc, fc)

	s.SettleAdopted(dispatch.NewFake(), "77", 0, testPR)

	recs := readRecords()
	if len(recs) != 1 || recs[0].PRURL != testPR || recs[0].State != "failed" {
		t.Fatalf("records = %+v, want one failed record with PRURL=%s", recs, testPR)
	}
}

// A read-write Box that opened its draft PR and then stopped blocked has the
// PR resolved host-side from the branch, never from its own landing=.
func TestSettle_ReadWriteBlocked_PROnBranch_SettledRecordCarriesPR(t *testing.T) {
	recs := readWriteRecords(t, "blocked", "https://evil.example/x", func(fc *forge.Fake) {
		fc.SetPR(fc.AgentBranch("77"), forge.PR{URL: testPR})
	})
	if len(recs) != 1 || recs[0].PRURL != testPR || recs[0].State != "failed" {
		t.Fatalf("records = %+v, want one failed record with PRURL=%s", recs, testPR)
	}
}

func TestSettle_ReadWriteBlocked_NoPR_NoPRURL(t *testing.T) {
	recs := readWriteRecords(t, "blocked", "https://evil.example/x", func(*forge.Fake) {})
	if len(recs) != 1 || recs[0].PRURL != "" || recs[0].State != "failed" {
		t.Fatalf("records = %+v, want one failed record with empty PRURL", recs)
	}
}

// The agent branch is reused across retries, so a PR an earlier run left
// closed must not be named on this run's record.
func TestSettle_ReadWriteBlocked_ClosedEarlierPR_NoPRURL(t *testing.T) {
	const old = "https://github.com/owner/repo/pull/1"
	recs := readWriteRecords(t, "blocked", "", func(fc *forge.Fake) {
		fc.SetPR(fc.AgentBranch("77"), forge.PR{URL: old})
		fc.SetPRState(old, forge.PRClosed)
	})
	if len(recs) != 1 || recs[0].PRURL != "" || recs[0].State != "failed" {
		t.Fatalf("records = %+v, want one failed record with empty PRURL", recs)
	}
}

// status=merged is off-script, so the branch lookup that backs its verification
// must not name a closed earlier PR on the record the run settles failed with.
func TestSettle_ReadWriteMerged_ClosedEarlierPR_NoPRURL(t *testing.T) {
	const old = "https://github.com/owner/repo/pull/1"
	recs := readWriteRecords(t, "merged", "", func(fc *forge.Fake) {
		fc.SetPR(fc.AgentBranch("77"), forge.PR{URL: old})
		fc.SetPRState(old, forge.PRClosed)
	})
	if len(recs) != 1 || recs[0].PRURL != "" || recs[0].State != "failed" {
		t.Fatalf("records = %+v, want one failed record with empty PRURL", recs)
	}
}

// A failed branch lookup leaves the record without a PR rather than failing
// the settle.
func TestSettle_ReadWriteBlocked_PRLookupError_NoPRURL(t *testing.T) {
	recs := readWriteRecords(t, "blocked", "", func(fc *forge.Fake) {
		fc.SetPR(fc.AgentBranch("77"), forge.PR{URL: testPR})
		fc.OpenPRForBranchErr = errors.New("503")
	})
	if len(recs) != 1 || recs[0].PRURL != "" || recs[0].State != "failed" {
		t.Fatalf("records = %+v, want one failed record with empty PRURL", recs)
	}
}

// A blocked read-only run that opened a draft PR host-side names it on the
// settled record.
func TestSettle_GithubReadOnly_BlockedDraftPR_SettledRecordCarriesPR(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	const issNum = "1933"
	const prURL = "https://github.com/owner/repo/pull/1933"
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	branch := fc.AgentBranch(issNum)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL

	result := dispatch.Result{
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: issNum, Landing: branch, Status: "blocked", Note: "stuck"},
		},
		PRIntent:      "feat: add widget\n\nAdds a widget.",
		PRIntentFound: true,
	}
	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())
	s.Settle(dispatch.NewFake(), issNum, 0, result)

	recs := readRecords()
	if len(recs) != 1 || recs[0].PRURL != prURL || recs[0].State != "failed" {
		t.Fatalf("records = %+v, want one failed record with PRURL=%s", recs, prURL)
	}
}

// The adopted-PR entry point latches the PR it gates, so a PR that lands
// reaches the daemon on the settled record.
func TestSettleAdopted_SettledRecordCarriesPR(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)

	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "77", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)

	s.SettleAdopted(dispatch.NewFake(), "77", 0, testPR)

	recs := readRecords()
	if len(recs) != 1 || recs[0].Event != report.EventSettled || recs[0].PRURL != testPR {
		t.Fatalf("records = %+v, want one settled record with PRURL=%s", recs, testPR)
	}
}
