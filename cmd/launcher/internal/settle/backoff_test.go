package settle

import (
	"errors"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
)

// recordingClock records every requested duration instead of sleeping, so a
// test can assert the backoff schedule without waiting for it.
func recordingClock() (*[]time.Duration, dispatch.Clock) {
	var sleeps []time.Duration
	return &sleeps, dispatch.Clock{Now: time.Now, Sleep: func(d time.Duration) { sleeps = append(sleeps, d) }}
}

// The preflightStaleBase push-retry loop waits Policy.Unit*N + Policy.Jitter
// before retry N. The trailing MergePollInterval sleep comes from
// rewaitAfterForcePush's gateToGreen confirm-poll, which since issue #2502
// sleeps through the same injected s.clock rather than a real time.Sleep.
func TestPreflightStaleBaseRebasePushBackoff_SucceedsAfterRetries(t *testing.T) {
	c := baseConfig()
	c.Policy = retry.Policy{Unit: 2 * time.Second, Jitter: 1 * time.Second}
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	sleeps, clock := recordingClock()
	c.Clock = clock

	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	// The initial stale-base attempt counts as the first Rebase call, so a
	// success on the third call means two retries.
	fc.RebaseErrs = []error{
		forge.ErrTransientPushFailure,
		forge.ErrTransientPushFailure,
		nil,
	}
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called after the rebase eventually succeeded; fc.Merged=%q", fc.Merged)
	}
	want := []time.Duration{3 * time.Second, 5 * time.Second, time.Duration(c.MergePollInterval) * time.Second}
	if len(*sleeps) != len(want) {
		t.Fatalf("recorded %d sleeps, want %d: got %v", len(*sleeps), len(want), *sleeps)
	}
	for i, d := range want {
		if (*sleeps)[i] != d {
			t.Errorf("sleep[%d] = %v, want %v", i, (*sleeps)[i], d)
		}
	}
}

// This covers the reactive rebase-retry loop inside mergeImmediate, which is a
// separate loop from preflightStaleBase. Exhausting the budget must still
// return forge.ErrTransientPushFailure, not a summary error that hides it.
func TestMergeImmediateRebasePushBackoff_ExhaustsWithOriginalError(t *testing.T) {
	c := baseConfig()
	c.Policy = retry.Policy{Unit: 2 * time.Second, Jitter: 1 * time.Second}
	c.MaxRebaseAttempts = 3
	sleeps, clock := recordingClock()
	c.Clock = clock

	fc := forge.NewFake()
	// The merge conflict is what triggers the initial Rebase call; RebaseErr
	// then fails every call, including that first one.
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrTransientPushFailure
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

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
		want := c.Policy.Unit*time.Duration(i+1) + c.Policy.Jitter
		if (*sleeps)[i] != want {
			t.Errorf("sleep[%d] = %v, want %v", i, (*sleeps)[i], want)
		}
	}
}

// The backoff only fires between retries, never before the first attempt, so a
// non-transient rebase failure records no sleeps.
func TestRebasePushBackoff_NoRetryNoSleep(t *testing.T) {
	c := baseConfig()
	c.Policy = retry.Policy{Unit: 2 * time.Second, Jitter: 1 * time.Second}
	c.MaxRebaseAttempts = 2
	sleeps, clock := recordingClock()
	c.Clock = clock

	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrMergeConflict
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err == nil {
		t.Fatal("mergeImmediate: want error on a non-transient rebase failure, got nil")
	}
	if len(*sleeps) != 0 {
		t.Errorf("recorded %d sleeps, want 0 (non-transient failure retries no push loop): got %v", len(*sleeps), *sleeps)
	}
}

func TestRebasePushBackoff_ZeroMaxRebaseAttemptsNoSleep(t *testing.T) {
	c := baseConfig()
	c.Policy = retry.Policy{Unit: 2 * time.Second, Jitter: 1 * time.Second}
	c.MaxRebaseAttempts = 0
	sleeps, clock := recordingClock()
	c.Clock = clock

	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrTransientPushFailure
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err == nil {
		t.Fatal("mergeImmediate: want error when MaxRebaseAttempts is 0, got nil")
	}
	if len(*sleeps) != 0 {
		t.Errorf("recorded %d sleeps, want 0 (MaxRebaseAttempts=0 disables retry): got %v", len(*sleeps), *sleeps)
	}
}
