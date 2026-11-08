package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/testutil"
)

func TestSettled_ReturnsSettlement(t *testing.T) {
	got := Settled(dispatchkey.Issue("7"), "", ForReason(dispatchrecord.ReasonMerged), hostop.DispatchSettled{PRURL: testPR, Note: "n"})

	want := Settlement{Verdict: ForReason(dispatchrecord.ReasonMerged), PRURL: testPR, Note: "n"}
	if got != want {
		t.Fatalf("Settled = %+v, want %+v", got, want)
	}
}

func TestSettle_BlockHandoffReturnsInProgressHandoffBlockedWithoutPR(t *testing.T) {
	const num = "1919"
	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: num, Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = forge.ErrBundleNotFound
	c := baseConfig()
	c.ReadOnly = true
	c.OutboxDir = func(n string) string { return "/outbox/" + n }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())

	got := s.Settle(dispatch.NewFake(), num, 0, settleReadyResult(num, "agent/issue-1919"))

	want := Settlement{Verdict: ForReason(dispatchrecord.ReasonHandoffBlocked)}
	if got != want {
		t.Fatalf("Settle = %+v, want %+v", got, want)
	}
	if got.State() != forge.InProgress || got.Delivered() {
		t.Fatalf("Settle = %+v, want in-progress and not delivered: blockHandoff never transitions the issue", got)
	}
}

func TestSettle_OffScriptStatusReturnsInProgressOffScript(t *testing.T) {
	const num = "2020"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: num, Labels: []string{"agent-in-progress"}})
	res := dispatch.Result{Resolved: outcome.Resolved{
		Found:   true,
		Outcome: outcome.Outcome{Issue: num, Landing: "agent/issue-2020", Status: "failed: <script>", Note: "ok"},
	}}
	s := newTestSettle(baseConfig(), fc, fc)

	got := s.Settle(dispatch.NewFake(), num, 0, res)

	if got.State() != forge.InProgress || got.Reason() != dispatchrecord.ReasonOffScript || got.PRURL != "" || got.Delivered() {
		t.Fatalf("Settle = %+v, want in-progress off-script, no PR, not delivered", got)
	}
}

func TestSettleAdopted_ReturnsCompleteWithLatchedPR(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 0
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)

	var got Settlement
	testutil.CaptureStdout(t, func() { got = s.SettleAdopted(&dispatch.Fake{RecordIDResult: "work:9@t"}, "9", 0, testPR) })

	if got.State() != forge.Complete || got.PRURL != testPR {
		t.Fatalf("SettleAdopted = %+v, want complete naming %s", got, testPR)
	}
}

func TestSettleAdopted_CompleteThenDemotedReturnsFailed(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 0
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, openAfterMerge{fc})

	var got Settlement
	testutil.CaptureStdout(t, func() { got = s.SettleAdopted(&dispatch.Fake{RecordIDResult: "work:9@t"}, "9", 0, testPR) })

	if got.Verdict != ForReason(dispatchrecord.ReasonMergeUnverified) || got.PRURL != testPR {
		t.Fatalf("SettleAdopted = %+v, want merge-unverified (failed) naming %s", got, testPR)
	}
}

func TestSettle_NothingLatchedReturnsZeroSettlement(t *testing.T) {
	c := baseConfig()
	c.Unclaimed = true
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-butler-finding"}})
	s := newTestSettle(c, fc, fc)
	res := dispatch.Result{Resolved: outcome.Resolved{
		Found:   true,
		Outcome: outcome.Outcome{Issue: "9", Landing: "agent/issue-9", Status: "blocked", Note: "x"},
	}}

	got := s.Settle(dispatch.NewFake(), "9", 0, res)

	if got != (Settlement{}) || got.State() != forge.Dispatchable {
		t.Fatalf("Settle = %+v, want the zero Settlement (dispatchable)", got)
	}
}

func TestSettlePatch_ReturnsGateSettlementAndRecordsNothingIssueKeyed(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 0
	c.Unclaimed = true
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "5", Labels: append([]string{}, butlerPatchLabels...)})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)

	var got Settlement
	testutil.CaptureStdout(t, func() { got = s.SettlePatch(dispatchkey.Chore("dead-code"), "5", testPR) })

	if got.State() != forge.Complete || got.PRURL != testPR {
		t.Fatalf("SettlePatch = %+v, want complete naming %s", got, testPR)
	}
	for _, r := range readRecords() {
		if r.Event == "settled" {
			t.Errorf("owned chore gate emitted a settled record %+v", r)
		}
	}
}

func TestResearchSettle_ReturnsVerdictSettlement(t *testing.T) {
	fc := newResearchFake("42")
	result := dispatch.Result{Resolved: outcome.Resolved{
		Found:   true,
		Outcome: outcome.Outcome{Issue: "42", Landing: "none", Status: "recommend", Note: "grounded"},
	}}
	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels, false, testResearchFindingLabel)

	var got Settlement
	testutil.CaptureStdout(t, func() { got = s.Settle(dispatch.NewFake(), "42", 0, result) })

	want := Settlement{Verdict: ForReason(dispatchrecord.ReasonVerdict), Note: "verdict recommend"}
	if got != want {
		t.Fatalf("Settle = %+v, want %+v", got, want)
	}
}

func TestResearchSettle_MissingOutcomeReturnsResearchFailed(t *testing.T) {
	fc := newResearchFake("42")
	s := NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels, false, testResearchFindingLabel)

	var got Settlement
	testutil.CaptureStdout(t, func() { got = s.Settle(dispatch.NewFake(), "42", 0, dispatch.Result{}) })

	if got.Verdict != ForReason(dispatchrecord.ReasonResearchFailed) {
		t.Fatalf("Settle verdict = %+v, want research-failed", got.Verdict)
	}
}

func settleReadyResult(num, branch string) dispatch.Result {
	return dispatch.Result{Resolved: outcome.Resolved{
		Found:   true,
		Outcome: outcome.Outcome{Issue: num, Landing: branch, Status: "ready", Note: "ok"},
	}}
}

func TestSettlement_Delivered(t *testing.T) {
	tests := []struct {
		name string
		s    Settlement
		want bool
	}{
		{"merged with PR", Settlement{Verdict: ForReason(dispatchrecord.ReasonMerged), PRURL: "u"}, true},
		{"merge-blocked without PR", Settlement{Verdict: ForReason(dispatchrecord.ReasonMergeBlocked)}, false},
		{"merge-blocked with PR", Settlement{Verdict: ForReason(dispatchrecord.ReasonMergeBlocked), PRURL: "u"}, true},
		{"already-resolved", Settlement{Verdict: ForReason(dispatchrecord.ReasonAlreadyResolved)}, false},
		{"relay-failed", Settlement{Verdict: ForReason(dispatchrecord.ReasonRelayFailed)}, false},
		{"ci-red with PR", Settlement{Verdict: ForReason(dispatchrecord.ReasonCIRed), PRURL: "u"}, false},
		{"push-only manual without PR", Settlement{Verdict: ForReason(dispatchrecord.ReasonManual)}, true},
		{"transition-refused complete", Settlement{Verdict: refused(ForReason(dispatchrecord.ReasonMerged))}, true},
		{"off-script", Settlement{Verdict: ForReason(dispatchrecord.ReasonOffScript)}, false},
		{"handoff-blocked with PR", Settlement{Verdict: ForReason(dispatchrecord.ReasonHandoffBlocked), PRURL: "u"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.Delivered(); got != tc.want {
				t.Errorf("%+v.Delivered() = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}
