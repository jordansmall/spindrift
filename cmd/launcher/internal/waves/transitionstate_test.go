package waves

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/testutil"
)

// TestTransitionState_NoteReported pins the #3627 review finding: a Failed
// transition must carry whatever note the caller passes through to the
// settled record, not a hardcoded "". It also pins issue #4785: the same
// terminal settle appends one dispatch_settled to the Dispatch's primary log
// and the report carries its record_id.
func TestTransitionState_NoteReported(t *testing.T) {
	read := testutil.InstallPipeReporter(t)
	fc := forge.NewFake()
	path := filepath.Join(t.TempDir(), "issue-42.log")
	stamp := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchStart, Start: &claude.DispatchStart{RecordID: "work:42@x"}})
	if err := os.WriteFile(path, []byte(stamp), 0o644); err != nil {
		t.Fatal(err)
	}
	d := dispatch.NewFake()
	d.LogPathResult = path
	d.RecordIDResult = "work:42@x"

	transitionState(d, fc, "42", forge.InProgress, forge.Failed, "box never launched: boom", settle.ReasonBoxFailed)

	recs := read()
	if len(recs) != 1 {
		t.Fatalf("got %d records, want 1: %+v", len(recs), recs)
	}
	rec := recs[0]
	if rec.Event != report.EventSettled || rec.Key != dispatchkey.Issue("42") || rec.State != "failed" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if want := "box never launched: boom"; rec.Note != want {
		t.Errorf("rec.Note = %q, want %q", rec.Note, want)
	}
	if rec.RecordID != "work:42@x" {
		t.Errorf("rec.RecordID = %q, want work:42@x", rec.RecordID)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settled []claude.DispatchSettled
	for _, line := range strings.Split(string(data), "\n") {
		var ev claude.Event
		if json.Unmarshal([]byte(line), &ev) == nil && ev.SpindriftOp != nil && ev.SpindriftOp.Settled != nil {
			settled = append(settled, *ev.SpindriftOp.Settled)
		}
	}
	if len(settled) != 1 || settled[0].RecordID != "work:42@x" || settled[0].State != "failed" || settled[0].Reason != "box-failed" {
		t.Fatalf("settled ops = %+v, want one failed/box-failed for work:42@x", settled)
	}
}
