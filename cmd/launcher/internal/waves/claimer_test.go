package waves

import (
	"errors"
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
