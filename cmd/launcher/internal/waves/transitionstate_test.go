package waves

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/testutil"
)

// TestTransitionState_NoteReported pins the #3627 review finding: a Failed
// transition must carry whatever note the caller passes through to the
// settled record, not a hardcoded "".
func TestTransitionState_NoteReported(t *testing.T) {
	read := testutil.InstallPipeReporter(t)
	fc := forge.NewFake()
	transitionState(fc, "42", forge.InProgress, forge.Failed, "box never launched: boom")

	recs := read()
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1: %+v", len(recs), recs)
	}
	rec := recs[0]
	if rec.Event != report.EventSettled || rec.Issue != "42" || rec.State != "failed" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if want := "box never launched: boom"; rec.Note != want {
		t.Errorf("rec.Note = %q, want %q", rec.Note, want)
	}
}
