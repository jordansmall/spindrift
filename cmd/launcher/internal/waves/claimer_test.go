package waves

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
)

// Claim must perform exactly one Dispatchable->InProgress transition and
// return nil on success.
func TestLabelClaimer_Claim_TransitionsAndSucceeds(t *testing.T) {
	fc := forge.NewFake()
	c := NewLabelClaimer(fc, "agent-trigger", "agent-in-progress")

	err := c.Claim("7")

	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("want exactly one TransitionState call, got %+v", fc.TransitionStateCalls)
	}
	got := fc.TransitionStateCalls[0]
	if got.Num != "7" || got.From != forge.Dispatchable || got.To != forge.InProgress {
		t.Errorf("want Num=7 From=Dispatchable To=InProgress, got %+v", got)
	}
}

// Claim must surface a TransitionState failure to the caller as-is, never
// swallow it -- Claimer's own doc comment says a non-nil error means skip,
// which is the caller's job, not Claim's.
func TestLabelClaimer_Claim_ReturnsTransitionStateErr(t *testing.T) {
	fc := forge.NewFake()
	fc.TransitionStateErr = forge.ErrAuthFailure
	c := NewLabelClaimer(fc, "agent-trigger", "agent-in-progress")

	err := c.Claim("7")

	if err != forge.ErrAuthFailure {
		t.Fatalf("want forge.ErrAuthFailure, got %v", err)
	}
}

// Claim must surface ErrAlreadyClaimed like any other error (the caller's job
// is to skip on it) but must not print the "?? could not claim" warning line
// for it -- that line means a real failure, and losing a claim race is not
// one (#3887).
func TestLabelClaimer_Claim_AlreadyClaimed_ReturnsErrSilently(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "7", Labels: []string{testInProgressLabel}})
	c := NewLabelClaimer(fc, "agent-trigger", testInProgressLabel)

	stderr := testutil.CaptureStderr(t, func() {
		err := c.Claim("7")
		if !errors.Is(err, forge.ErrAlreadyClaimed) {
			t.Fatalf("want forge.ErrAlreadyClaimed, got %v", err)
		}
	})

	if strings.Contains(stderr, "??") {
		t.Errorf("stderr = %q, want no ?? warning line for an already-claimed skip", stderr)
	}
}

func claimRefusal(offered ...string) error {
	return fmt.Errorf("wrapped: %w", &forge.TransitionRefusedError{Issue: "7", From: "Open", To: "In Progress", Offered: offered})
}

// A refused claim (JIRA_LIFECYCLE=status) leaves no label behind, so Claim
// must post the reason once and stay quiet on an unchanged refusal.
func TestLabelClaimer_Claim_RefusalCommentsOncePerUnchangedRefusal(t *testing.T) {
	fc := forge.NewFake()
	fc.TransitionStateErr = claimRefusal()
	c := NewLabelClaimer(fc, "agent-trigger", "agent-in-progress")

	testutil.CaptureStderr(t, func() {
		for range 2 {
			if err := c.Claim("7"); err == nil {
				t.Fatal("want the refusal returned")
			}
		}
	})

	want := "spindrift could not move this issue: issue 7 could not move from status \"Open\" to \"In Progress\": the workflow offers no transitions"
	if len(fc.CommentCalls) != 1 || fc.CommentCalls[0].Num != "7" || fc.CommentCalls[0].Body != want {
		t.Errorf("want exactly one comment %q, got %+v", want, fc.CommentCalls)
	}
}

// A comment that fails to post must not be memoized, or the card never gets
// the reason for the life of the process. The fake records failed attempts.
func TestLabelClaimer_Claim_FailedCommentIsRetried(t *testing.T) {
	fc := forge.NewFake()
	fc.TransitionStateErr = claimRefusal()
	c := NewLabelClaimer(fc, "agent-trigger", "agent-in-progress")

	testutil.CaptureStderr(t, func() {
		fc.CommentErr = errors.New("jira 503")
		_ = c.Claim("7")
		fc.CommentErr = nil
		_ = c.Claim("7")
		_ = c.Claim("7")
	})

	if len(fc.CommentCalls) != 2 {
		t.Errorf("want the failed attempt, one retry, then silence (2 calls), got %+v", fc.CommentCalls)
	}
}

func TestLabelClaimer_Claim_ChangedRefusalCommentsAgain(t *testing.T) {
	fc := forge.NewFake()
	c := NewLabelClaimer(fc, "agent-trigger", "agent-in-progress")

	testutil.CaptureStderr(t, func() {
		fc.TransitionStateErr = claimRefusal()
		_ = c.Claim("7")
		fc.TransitionStateErr = claimRefusal("Review")
		_ = c.Claim("7")
	})

	if len(fc.CommentCalls) != 2 {
		t.Errorf("want a second comment for a changed refusal, got %+v", fc.CommentCalls)
	}
}

// A successful claim clears the memo so a later recurrence is reported again.
func TestLabelClaimer_Claim_SuccessResetsRefusalMemo(t *testing.T) {
	fc := forge.NewFake()
	c := NewLabelClaimer(fc, "agent-trigger", "agent-in-progress")

	testutil.CaptureStderr(t, func() {
		fc.TransitionStateErr = claimRefusal()
		_ = c.Claim("7")
		fc.TransitionStateErr = nil
		if err := c.Claim("7"); err != nil {
			t.Fatalf("want success, got %v", err)
		}
		fc.TransitionStateErr = claimRefusal()
		_ = c.Claim("7")
	})

	if len(fc.CommentCalls) != 2 {
		t.Errorf("want a comment after the recurrence, got %+v", fc.CommentCalls)
	}
}

func TestLabelClaimer_Claim_NonRefusalErrorsPostNoComment(t *testing.T) {
	for name, err := range map[string]error{
		"auth":            forge.ErrAuthFailure,
		"already-claimed": forge.ErrAlreadyClaimed,
	} {
		t.Run(name, func(t *testing.T) {
			fc := forge.NewFake()
			fc.TransitionStateErr = err
			c := NewLabelClaimer(fc, "agent-trigger", "agent-in-progress")

			testutil.CaptureStderr(t, func() { _ = c.Claim("7") })

			if len(fc.CommentCalls) != 0 {
				t.Errorf("want no comment, got %+v", fc.CommentCalls)
			}
		})
	}
}
