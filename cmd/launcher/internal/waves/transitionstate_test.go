package waves

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostop"
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
	stamp := hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpDispatchStart, Start: &hostop.DispatchStart{RecordID: "work:42@x"}})
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
	var settled []hostop.DispatchSettled
	for _, line := range strings.Split(string(data), "\n") {
		var ev hostop.Event
		if json.Unmarshal([]byte(line), &ev) == nil && ev.SpindriftOp != nil && ev.SpindriftOp.Settled != nil {
			settled = append(settled, *ev.SpindriftOp.Settled)
		}
	}
	if len(settled) != 1 || settled[0].RecordID != "work:42@x" || settled[0].State != "failed" || settled[0].Reason != "box-failed" {
		t.Fatalf("settled ops = %+v, want one failed/box-failed for work:42@x", settled)
	}
}

// TestTransitionState_WarnsWithTrackerError pins issue #6067: the best-effort
// warning must name the tracker's error, not just the target state.
func TestTransitionState_WarnsWithTrackerError(t *testing.T) {
	testutil.InstallPipeReporter(t)
	fc := forge.NewFake()
	fc.TransitionStateErr = fmt.Errorf("boom")
	d := dispatch.NewFake()

	got := testutil.CaptureStderr(t, func() {
		transitionState(d, fc, "42", forge.InProgress, forge.Failed, "", settle.ReasonBoxFailed)
	})

	if want := "    ?? #42: could not transition to state failed: boom\n"; !strings.Contains(got, want) {
		t.Errorf("stderr = %q, want it to contain %q", got, want)
	}
}

// JIRA_LIFECYCLE=status (issue #6353): a crashed Box whose Failed transition
// is refused must leave the card a comment and settle transition-refused, the
// same as settle's own path.
func TestTransitionState_Refused_CommentsAndSettlesDistinctReason(t *testing.T) {
	testutil.InstallPipeReporter(t)
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "42"})
	fc.TransitionStateErr = fmt.Errorf("wrapped: %w", &forge.TransitionRefusedError{Issue: "42", From: "In Progress", To: "Blocked"})
	path := filepath.Join(t.TempDir(), "issue-42.log")
	stamp := hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpDispatchStart, Start: &hostop.DispatchStart{RecordID: "work:42@x"}})
	if err := os.WriteFile(path, []byte(stamp), 0o644); err != nil {
		t.Fatal(err)
	}
	d := dispatch.NewFake()
	d.LogPathResult = path
	d.RecordIDResult = "work:42@x"

	testutil.CaptureStderr(t, func() {
		transitionState(d, fc, "42", forge.InProgress, forge.Failed, "box crashed", settle.ReasonBoxFailed)
	})

	if len(fc.CommentCalls) != 1 || fc.CommentCalls[0].Num != "42" || !strings.Contains(fc.CommentCalls[0].Body, `"Blocked"`) {
		t.Fatalf("comments = %+v, want one naming the target status on issue 42", fc.CommentCalls)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settled []hostop.DispatchSettled
	for _, line := range strings.Split(string(data), "\n") {
		var ev hostop.Event
		if json.Unmarshal([]byte(line), &ev) == nil && ev.SpindriftOp != nil && ev.SpindriftOp.Settled != nil {
			settled = append(settled, *ev.SpindriftOp.Settled)
		}
	}
	if len(settled) != 1 || settled[0].State != "failed" || settled[0].Reason != settle.ReasonTransitionRefused {
		t.Fatalf("settled ops = %+v, want one failed/transition-refused", settled)
	}
	if n := settled[0].Note; !strings.Contains(n, "box crashed") || !strings.Contains(n, settle.ReasonBoxFailed) {
		t.Errorf("note = %q, want the displaced reason and note kept", n)
	}
}
