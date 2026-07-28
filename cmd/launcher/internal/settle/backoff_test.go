package settle

import (
	"errors"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// recordingClock returns a dispatch.Clock whose Sleep records every duration
// it is asked to sleep instead of actually sleeping, plus a pointer to the
// slice it appends to so a test can inspect what was recorded.
func recordingClock() (*[]time.Duration, dispatch.Clock) {
	var sleeps []time.Duration
	return &sleeps, dispatch.Clock{Now: time.Now, Sleep: func(d time.Duration) { sleeps = append(sleeps, d) }}
}

// TestPreflightStaleBaseRebasePushBackoff_SucceedsAfterRetries verifies that
// the preflightStaleBase push-retry loop sleeps a jittered linear backoff
// between transient-failure retries — attempt N waits
// TransientBackoffSecs*N + HoldJitterSecs — and that a success on the final
// retry records exactly N-1 sleeps.
func TestPreflightStaleBaseRebasePushBackoff_SucceedsAfterRetries(t *testing.T) {
	c := baseConfig()
	c.TransientBackoffSecs = 2
	c.HoldJitterSecs = 1
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	sleeps, clock := recordingClock()
	c.Clock = clock

	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	// First Rebase call (the initial stale-base attempt) fails transiently
	// twice more, then succeeds on the third overall call.
	fc.RebaseErrs = []error{
		forge.ErrTransientPushFailure,
		forge.ErrTransientPushFailure,
		nil,
	}
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called after the rebase eventually succeeded; fc.Merged=%q", fc.Merged)
	}
	want := []time.Duration{3 * time.Second, 5 * time.Second}
	if len(*sleeps) != len(want) {
		t.Fatalf("recorded %d sleeps, want %d: got %v", len(*sleeps), len(want), *sleeps)
	}
	for i, d := range want {
		if (*sleeps)[i] != d {
			t.Errorf("sleep[%d] = %v, want %v", i, (*sleeps)[i], d)
		}
	}
}

// TestMergeImmediateRebasePushBackoff_ExhaustsWithOriginalError verifies that
// a persistent transient push failure on the reactive rebase-retry loop
// (inside mergeImmediate, distinct from preflightStaleBase) bails once
// MaxRebaseAttempts retries are exhausted, surfacing the ORIGINAL
// forge.ErrTransientPushFailure unchanged, recording exactly
// MaxRebaseAttempts sleeps, and never calling Merge.
func TestMergeImmediateRebasePushBackoff_ExhaustsWithOriginalError(t *testing.T) {
	c := baseConfig()
	c.TransientBackoffSecs = 2
	c.HoldJitterSecs = 1
	c.MaxRebaseAttempts = 3
	sleeps, clock := recordingClock()
	c.Clock = clock

	fc := forge.NewFake()
	// The reactive loop first hits a merge conflict, triggering the initial
	// Rebase call; every Rebase call thereafter (including that first one)
	// returns the transient error, exhausting the push-retry budget.
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrTransientPushFailure
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if !errors.Is(err, forge.ErrTransientPushFailure) {
		t.Fatalf("mergeImmediate: err = %v, want forge.ErrTransientPushFailure", err)
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called when the rebase-push retries never recover; fc.Merged=%q", fc.Merged)
	}
	if len(*sleeps) != c.MaxRebaseAttempts {
		t.Fatalf("recorded %d sleeps, want %d (== MaxRebaseAttempts): got %v", len(*sleeps), c.MaxRebaseAttempts, *sleeps)
	}
	for i := range *sleeps {
		want := time.Duration(c.TransientBackoffSecs)*time.Second*time.Duration(i+1) + time.Duration(c.HoldJitterSecs)*time.Second
		if (*sleeps)[i] != want {
			t.Errorf("sleep[%d] = %v, want %v", i, (*sleeps)[i], want)
		}
	}
}

// TestRebasePushBackoff_NoRetryNoSleep verifies that a non-transient rebase
// failure (no retries attempted) records zero backoff sleeps — the backoff
// only fires between retries, never on the first, un-retried attempt.
func TestRebasePushBackoff_NoRetryNoSleep(t *testing.T) {
	c := baseConfig()
	c.TransientBackoffSecs = 2
	c.HoldJitterSecs = 1
	c.MaxRebaseAttempts = 2
	sleeps, clock := recordingClock()
	c.Clock = clock

	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrMergeConflict
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err == nil {
		t.Fatal("mergeImmediate: want error on a non-transient rebase failure, got nil")
	}
	if len(*sleeps) != 0 {
		t.Errorf("recorded %d sleeps, want 0 (non-transient failure retries no push loop): got %v", len(*sleeps), *sleeps)
	}
}

// TestRebasePushBackoff_ZeroMaxRebaseAttemptsNoSleep verifies that a zero
// MaxRebaseAttempts (no retry budget) records zero backoff sleeps.
func TestRebasePushBackoff_ZeroMaxRebaseAttemptsNoSleep(t *testing.T) {
	c := baseConfig()
	c.TransientBackoffSecs = 2
	c.HoldJitterSecs = 1
	c.MaxRebaseAttempts = 0
	sleeps, clock := recordingClock()
	c.Clock = clock

	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrTransientPushFailure
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err == nil {
		t.Fatal("mergeImmediate: want error when MaxRebaseAttempts is 0, got nil")
	}
	if len(*sleeps) != 0 {
		t.Errorf("recorded %d sleeps, want 0 (MaxRebaseAttempts=0 disables retry): got %v", len(*sleeps), *sleeps)
	}
}
