package forge_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
)

func TestCommentRefusal_PostsWrappedRefusal(t *testing.T) {
	fc := forge.NewFake()
	refused := &forge.TransitionRefusedError{Issue: "9", From: "A", To: "B"}

	got, _ := forge.CommentRefusal(fc, "9", fmt.Errorf("wrapped: %w", refused))

	if got != refused {
		t.Fatalf("want the refusal back, got %v", got)
	}
	want := "spindrift could not move this issue: " + refused.Error()
	if len(fc.CommentCalls) != 1 || fc.CommentCalls[0].Num != "9" || fc.CommentCalls[0].Body != want {
		t.Errorf("want one comment %q on 9, got %+v", want, fc.CommentCalls)
	}
}

func TestCommentRefusal_NonRefusalPostsNothing(t *testing.T) {
	for name, err := range map[string]error{
		"nil":             nil,
		"plain":           errors.New("boom"),
		"already-claimed": forge.ErrAlreadyClaimed,
	} {
		t.Run(name, func(t *testing.T) {
			fc := forge.NewFake()
			if got, cerr := forge.CommentRefusal(fc, "9", err); got != nil || cerr != nil {
				t.Errorf("want nil refusal and nil error, got %v, %v", got, cerr)
			}
			if len(fc.CommentCalls) != 0 {
				t.Errorf("want no comment, got %+v", fc.CommentCalls)
			}
		})
	}
}

func TestCommentRefusal_CommentFailureLoggedAndReturned(t *testing.T) {
	fc := forge.NewFake()
	fc.CommentErr = errors.New("api down")
	refused := &forge.TransitionRefusedError{Issue: "9", To: "B"}
	var got *forge.TransitionRefusedError
	var cerr error

	stderr := testutil.CaptureStderr(t, func() { got, cerr = forge.CommentRefusal(fc, "9", refused) })

	if got != refused {
		t.Errorf("want the refusal back despite the comment failure, got %v", got)
	}
	if cerr == nil {
		t.Error("want the comment failure returned")
	}
	if !strings.Contains(stderr, "could not comment the refused transition") {
		t.Errorf("stderr = %q, want the comment-failure line", stderr)
	}
}
