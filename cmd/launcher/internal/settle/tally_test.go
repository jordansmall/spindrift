package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
)

func TestTally_RecordFoldsSettlementUnderKey(t *testing.T) {
	tally := &Tally{}
	st := Settlement{Verdict: ForReason(dispatchrecord.ReasonMerged), PRURL: "https://example.test/pr/1"}

	tally.Record(dispatchkey.Issue("7"), st)

	want := DispatchOutcome{Key: dispatchkey.Issue("7"), Settlement: st}
	got := tally.Outcomes()
	if len(got) != 1 || got[0] != want {
		t.Fatalf("Outcomes() = %+v, want [%+v]", got, want)
	}
}

func TestTally_LastWriteWinsKeepingFirstRecordedPosition(t *testing.T) {
	tally := &Tally{}

	tally.Record(dispatchkey.Issue("1"), Settlement{Verdict: ForReason(dispatchrecord.ReasonCIRed)})
	tally.Record(dispatchkey.Issue("2"), Settlement{Verdict: ForReason(dispatchrecord.ReasonMerged)})
	tally.Record(dispatchkey.Issue("1"), Settlement{Verdict: ForReason(dispatchrecord.ReasonMerged)})

	got := tally.Outcomes()
	if len(got) != 2 {
		t.Fatalf("Outcomes() = %+v, want 2 entries", got)
	}
	if got[0].Key != dispatchkey.Issue("1") || got[0].State() != forge.Complete {
		t.Errorf("got[0] = %+v, want issue 1 overwritten to complete in first position", got[0])
	}
	if got[1].Key != dispatchkey.Issue("2") {
		t.Errorf("got[1] = %+v, want issue 2", got[1])
	}
}

func TestTally_OutcomesReturnsCopy(t *testing.T) {
	tally := &Tally{}
	tally.Record(dispatchkey.Issue("1"), Settlement{Verdict: ForReason(dispatchrecord.ReasonMerged)})

	tally.Outcomes()[0].PRURL = "mutated"

	if got := tally.Outcomes()[0].PRURL; got != "" {
		t.Fatalf("PRURL = %q, want the stored record untouched", got)
	}
}

func TestTally_RecordIgnoresZeroSettlement(t *testing.T) {
	tally := &Tally{}
	tally.Record(dispatchkey.Issue("1"), Settlement{})
	if got := tally.Outcomes(); len(got) != 0 {
		t.Fatalf("Outcomes() = %+v, want none for the no-decision Settlement", got)
	}

	tally.Record(dispatchkey.Issue("1"), Settlement{Verdict: ForReason(dispatchrecord.ReasonMerged)})
	tally.Record(dispatchkey.Issue("1"), Settlement{})
	if got := tally.Outcomes(); len(got) != 1 || got[0].State() != forge.Complete {
		t.Fatalf("Outcomes() = %+v, want the earlier Complete kept", got)
	}
}

func TestTally_NilRecordIsSafe(t *testing.T) {
	var tally *Tally
	tally.Record(dispatchkey.Issue("1"), Settlement{Verdict: ForReason(dispatchrecord.ReasonMerged)})
}
