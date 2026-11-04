package settle

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
)

func TestTransition_Refused_CommentsAndKeepsDisplacedReasonAndNote(t *testing.T) {
	fc := forge.NewFake()
	refused := &forge.TransitionRefusedError{Issue: "9", From: "A", To: "B"}
	fc.TransitionStateErr = fmt.Errorf("wrapped: %w", refused)

	reason, note, err := Transition(fc, "9", forge.InProgress, forge.Complete, ReasonMerged, "merged cleanly")

	if !errors.Is(err, refused) || reason != ReasonTransitionRefused {
		t.Fatalf("err=%v reason=%q, want the refusal and %q", err, reason, ReasonTransitionRefused)
	}
	if want := refused.Error() + " (settle reason was merged: merged cleanly)"; note != want {
		t.Errorf("note = %q, want %q", note, want)
	}
	if len(fc.CommentCalls) != 1 || fc.CommentCalls[0].Body != "spindrift could not move this issue: "+refused.Error() {
		t.Errorf("comments = %+v", fc.CommentCalls)
	}
}

func TestTransition_Refused_EmptyNoteOmitsSeparator(t *testing.T) {
	fc := forge.NewFake()
	fc.TransitionStateErr = &forge.TransitionRefusedError{Issue: "9", From: "A", To: "B"}

	_, note, _ := Transition(fc, "9", forge.InProgress, forge.Failed, ReasonCIRed, "")

	if !strings.HasSuffix(note, "(settle reason was ci-red)") {
		t.Errorf("note = %q, want no trailing separator", note)
	}
}

func TestTransition_CommentFailureLoggedNotFatal(t *testing.T) {
	fc := forge.NewFake()
	fc.CommentErr = errors.New("no comment")
	fc.TransitionStateErr = &forge.TransitionRefusedError{Issue: "9", To: "B"}

	var reason string
	got := testutil.CaptureStderr(t, func() {
		reason, _, _ = Transition(fc, "9", forge.InProgress, forge.Complete, ReasonMerged, "")
	})

	if reason != ReasonTransitionRefused || !strings.Contains(got, "could not comment the refused transition") {
		t.Errorf("reason=%q stderr=%q", reason, got)
	}
}

func TestTransition_OtherErrorUntouched(t *testing.T) {
	fc := forge.NewFake()
	boom := errors.New("boom")
	fc.TransitionStateErr = boom

	reason, note, err := Transition(fc, "9", forge.InProgress, forge.Failed, ReasonCIRed, "n")

	if err != boom || reason != ReasonCIRed || note != "n" || len(fc.CommentCalls) != 0 {
		t.Errorf("got %q %q err=%v comments=%d, want inputs back and no comment", reason, note, err, len(fc.CommentCalls))
	}
}

func TestTransition_SuccessUntouched(t *testing.T) {
	fc := forge.NewFake()

	reason, note, err := Transition(fc, "9", forge.InProgress, forge.Failed, ReasonCIRed, "n")

	if err != nil || reason != ReasonCIRed || note != "n" || len(fc.CommentCalls) != 0 {
		t.Errorf("got %q %q err=%v comments=%d, want inputs back and no comment", reason, note, err, len(fc.CommentCalls))
	}
}
