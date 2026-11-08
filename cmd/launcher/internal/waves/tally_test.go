package waves

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

func oneIssueFixture(t *testing.T, boxFails bool) (Config, *forge.Fake, *runner.Fake, Claimer, string) {
	t.Helper()
	c := baseConfig()
	c.MaxParallel = 1
	label := "agent-trigger"
	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fr := runner.NewFake()
	if boxFails {
		fr.RunErr = boxErr
	}
	return c, fc, fr, NewLabelClaimer(fc, label, testInProgressLabel), tempLogDir(t)
}

func wantOutcome(t *testing.T, tally *settle.Tally, want settle.Settlement) {
	t.Helper()
	got := tally.Outcomes()
	if len(got) != 1 || got[0].Key != dispatchkey.Issue("1") || got[0].Settlement != want {
		t.Fatalf("Outcomes() = %+v, want issue 1 -> %+v", got, want)
	}
}

func wantBoxFailed(t *testing.T, tally *settle.Tally) {
	t.Helper()
	got := tally.Outcomes()
	if len(got) != 1 || got[0].Key != dispatchkey.Issue("1") || got[0].Reason() != dispatchrecord.ReasonBoxFailed || got[0].State() != forge.Failed {
		t.Fatalf("Outcomes() = %+v, want issue 1 failed (box-failed)", got)
	}
}

func TestDispatch_FoldsReturnedSettlementIntoSessionTally(t *testing.T) {
	c, fc, fr, claimer, dir := oneIssueFixture(t, false)
	s := settle.NewFake()
	s.Settlement = settle.Settlement{Verdict: settle.ForReason(dispatchrecord.ReasonMerged), PRURL: "https://example.test/pr/1"}
	tally := &settle.Tally{}

	in := Input{Origin: OriginDiscovered, Batch: Batch{Issues: []Issue{{Number: "1", Title: "first"}}}}
	if err := Dispatch(c, &Session{Tally: tally}, fc, fc, dir, testFactory(t, dir, fr), s, in, claimer); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	wantOutcome(t, tally, s.Settlement)
}

func TestDispatch_FoldsBoxFailedSettlementIntoSessionTally(t *testing.T) {
	c, fc, fr, claimer, dir := oneIssueFixture(t, true)
	tally := &settle.Tally{}

	in := Input{Origin: OriginDiscovered, Batch: Batch{Issues: []Issue{{Number: "1", Title: "first"}}}}
	if err := Dispatch(c, &Session{Tally: tally}, fc, fc, dir, testFactory(t, dir, fr), settle.NewFake(), in, claimer); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	wantBoxFailed(t, tally)
}

func runContinuousOnce(t *testing.T, boxFails bool, s settle.Settler, tally *settle.Tally) {
	t.Helper()
	c, fc, fr, _, dir := oneIssueFixture(t, boxFails)
	q := NewFakeQueue()
	q.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}, Edges: map[string][]string{}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }
	if err := RunContinuous(c, &Session{Tally: tally}, fc, fc, testFactory(t, dir, fr), s, q, fresh); err != nil {
		t.Fatalf("RunContinuous: %v", err)
	}
}

func TestRunContinuous_FoldsReturnedSettlementIntoSessionTally(t *testing.T) {
	s := settle.NewFake()
	s.Settlement = settle.Settlement{Verdict: settle.ForReason(dispatchrecord.ReasonMerged), PRURL: "https://example.test/pr/1"}
	tally := &settle.Tally{}

	runContinuousOnce(t, false, s, tally)

	wantOutcome(t, tally, s.Settlement)
}

func TestRunContinuous_FoldsBoxFailedSettlementIntoSessionTally(t *testing.T) {
	tally := &settle.Tally{}

	runContinuousOnce(t, true, settle.NewFake(), tally)

	wantBoxFailed(t, tally)
}
