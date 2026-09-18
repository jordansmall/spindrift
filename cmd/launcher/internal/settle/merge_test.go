package settle

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
)

// Conflict resolution routes through a dispatch.Dispatcher (issue #442)
// instead of a raw callback.
func TestMergeImmediate(t *testing.T) {
	cases := []struct {
		name                      string
		maxRebaseAttempts         int
		transientRetryMax         int
		mergeErr                  error
		mergeErrs                 []error
		rebaseErr                 error
		rebaseErrs                []error
		conflictResolveErr        error
		noDispatcher              bool
		postForcePushGreen        bool
		wantErr                   bool
		wantMerged                bool
		wantRebaseCalled          int
		wantConflictResolveCalled int
	}{
		{
			name:       "clean merge succeeds",
			wantErr:    false,
			wantMerged: true,
		},
		{
			name:       "non-conflict merge failure is returned",
			mergeErr:   errors.New("required review missing"),
			wantErr:    true,
			wantMerged: false,
		},
		{
			name:               "conflict → rebase → retry succeeds",
			maxRebaseAttempts:  3,
			mergeErrs:          []error{forge.ErrMergeConflict, nil},
			postForcePushGreen: true,
			wantErr:            false,
			wantMerged:         true,
			wantRebaseCalled:   1,
		},
		{
			name:              "conflict → rebase fails → error returned",
			maxRebaseAttempts: 3,
			mergeErrs:         []error{forge.ErrMergeConflict},
			rebaseErr:         errors.New("rebase failed: conflict"),
			wantErr:           true,
			wantMerged:        false,
			wantRebaseCalled:  1,
		},
		{
			name:              "transient merge failure retried then succeeds",
			maxRebaseAttempts: 3,
			transientRetryMax: 3,
			mergeErrs:         []error{forge.ErrMergeTransient, nil},
			wantErr:           false,
			wantMerged:        true,
		},
		{
			name:              "transient merge failure persists → retries exhausted, error returned",
			maxRebaseAttempts: 3,
			transientRetryMax: 3,
			mergeErrs: []error{
				forge.ErrMergeTransient,
				forge.ErrMergeTransient,
				forge.ErrMergeTransient,
				forge.ErrMergeTransient,
			},
			wantErr:    true,
			wantMerged: false,
		},
		{
			// TRANSIENT_RETRY_MAX, not MaxRebaseAttempts, must bound the
			// merge-transient retry loop (issue #2325 AC #4): a much larger
			// MaxRebaseAttempts must not mask a small Policy.Max.
			name:              "transient merge failure honors Policy.Max independent of MaxRebaseAttempts",
			maxRebaseAttempts: 10,
			transientRetryMax: 1,
			mergeErrs: []error{
				forge.ErrMergeTransient,
				forge.ErrMergeTransient,
			},
			wantErr:    true,
			wantMerged: false,
		},
		{
			name:              "conflict exhausts maxRebaseAttempts → error returned",
			maxRebaseAttempts: 1,
			mergeErrs:         []error{forge.ErrMergeConflict, forge.ErrMergeConflict},
			wantErr:           true,
			wantMerged:        false,
			wantRebaseCalled:  1,
		},
		{
			name:                      "rebase conflict → conflict-resolve fn succeeds → merge succeeds",
			maxRebaseAttempts:         3,
			mergeErrs:                 []error{forge.ErrMergeConflict, nil},
			rebaseErr:                 forge.ErrMergeConflict,
			postForcePushGreen:        true,
			wantErr:                   false,
			wantMerged:                true,
			wantRebaseCalled:          1,
			wantConflictResolveCalled: 1,
		},
		{
			name:                      "rebase conflict → conflict-resolve fn fails → error returned",
			maxRebaseAttempts:         3,
			mergeErrs:                 []error{forge.ErrMergeConflict},
			rebaseErr:                 forge.ErrMergeConflict,
			conflictResolveErr:        errors.New("agent could not resolve conflict"),
			wantErr:                   true,
			wantMerged:                false,
			wantRebaseCalled:          1,
			wantConflictResolveCalled: 1,
		},
		{
			// After conflict-resolve succeeds the forge's mergeability
			// snapshot is briefly stale, so the next Merge still reports a
			// conflict. The loop must retry Merge directly, because the box
			// already rebased and force-pushed.
			name:                      "conflict-resolve succeeds → stale conflict on retry does not re-rebase",
			maxRebaseAttempts:         3,
			mergeErrs:                 []error{forge.ErrMergeConflict, forge.ErrMergeConflict, nil},
			rebaseErr:                 forge.ErrMergeConflict,
			postForcePushGreen:        true,
			wantErr:                   false,
			wantMerged:                true,
			wantRebaseCalled:          1,
			wantConflictResolveCalled: 1,
		},
		{
			name:                      "rebase conflict → no dispatcher → error returned",
			maxRebaseAttempts:         3,
			mergeErrs:                 []error{forge.ErrMergeConflict},
			rebaseErr:                 forge.ErrMergeConflict,
			noDispatcher:              true,
			wantErr:                   true,
			wantMerged:                false,
			wantRebaseCalled:          1,
			wantConflictResolveCalled: 0,
		},
		{
			// A transient push failure (forge outage, network fault) during
			// the force-push must not block the merge outright. The push is
			// retried, and here the retry succeeds.
			name:               "conflict → rebase transient push failure → retry succeeds",
			maxRebaseAttempts:  3,
			mergeErrs:          []error{forge.ErrMergeConflict, nil},
			rebaseErrs:         []error{forge.ErrTransientPushFailure, nil},
			postForcePushGreen: true,
			wantErr:            false,
			wantMerged:         true,
			wantRebaseCalled:   2,
		},
		{
			// The forge stays down, so every retry hits the same transient
			// error. The retry must be bounded rather than spin forever, and
			// the eventual failure must still reach the caller.
			name:              "conflict → rebase transient push failure persists → retries exhausted, error returned",
			maxRebaseAttempts: 2,
			mergeErrs:         []error{forge.ErrMergeConflict},
			rebaseErrs: []error{
				forge.ErrTransientPushFailure,
				forge.ErrTransientPushFailure,
				forge.ErrTransientPushFailure,
			},
			wantErr:          true,
			wantMerged:       false,
			wantRebaseCalled: 3,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			if tc.maxRebaseAttempts != 0 {
				c.MaxRebaseAttempts = tc.maxRebaseAttempts
			}
			if tc.transientRetryMax != 0 {
				c.Policy.Max = tc.transientRetryMax
			}
			fc := forge.NewFake()
			if len(tc.mergeErrs) > 0 {
				fc.MergeErrs = tc.mergeErrs
			} else {
				fc.MergeErr = tc.mergeErr
			}
			if len(tc.rebaseErrs) > 0 {
				fc.RebaseErrs = tc.rebaseErrs
			} else {
				fc.RebaseErr = tc.rebaseErr
			}
			if tc.postForcePushGreen {
				fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
			}
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})

			var d dispatch.Dispatcher
			var df *dispatch.Fake
			if !tc.noDispatcher {
				df = dispatch.NewFake()
				df.ResolveConflictErr = tc.conflictResolveErr
				d = df
			}

			s := newTestSettle(c, fc, fc)
			err := s.mergeImmediate("1", 0, testPR, d)

			if (err != nil) != tc.wantErr {
				t.Errorf("mergeImmediate err=%v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantMerged && fc.Merged != testPR {
				t.Errorf("Merge not called; fc.Merged=%q", fc.Merged)
			}
			if !tc.wantMerged && fc.Merged != "" {
				t.Errorf("Merge should not have been called; fc.Merged=%q", fc.Merged)
			}
			if got := len(fc.RebasedURLs); got != tc.wantRebaseCalled {
				t.Errorf("Rebase called %d times, want %d", got, tc.wantRebaseCalled)
			}
			gotConflictResolveCalled := 0
			if df != nil {
				gotConflictResolveCalled = len(df.ResolveConflictCalls)
			}
			if gotConflictResolveCalled != tc.wantConflictResolveCalled {
				t.Errorf("ResolveConflict called %d times, want %d", gotConflictResolveCalled, tc.wantConflictResolveCalled)
			}
		})
	}
}

// A genuine ErrMergeConflict on the conflict-retry loop's first Merge attempt
// flips the PR to draft before the rebase, then back to ready once the rebased
// head re-confirms green, all before the retried Merge lands it (issue #1863).
func TestMergeImmediate_ConflictDemotesToDraftAndRestoresOnGreen(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	want := []string{"Merge:" + testPR, "MarkDraft:" + testPR, "MarkReady:" + testPR, "Merge:" + testPR}
	if !slices.Equal(fc.LandingCallLog, want) {
		t.Errorf("LandingCallLog = %v, want %v", fc.LandingCallLog, want)
	}
}

// A Code Forge implementing forge.BundleRelay (the github read-only adapter,
// issue #1919) must relay the resolved branch in from the outbox before the
// retried Merge. Under BOX_FORGE_AND_ISSUE_ACCESS=read-only the conflict-resolve
// Box exits without running the main agent (issue #1979), so the relay is the
// only way its work lands; without it Merge sees the same conflict forever.
func TestMergeImmediate_ConflictResolveRelaysBundleWhenReadOnly(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.RebaseErr = forge.ErrMergeConflict
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	df := dispatch.NewFake()
	cf := fc.AsGithubReadOnly()
	s := newTestSettle(c, fc, cf)

	err := s.mergeImmediate("1", 0, testPR, df)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if len(df.ResolveConflictCalls) != 1 {
		t.Errorf("ResolveConflict called %d times, want 1", len(df.ResolveConflictCalls))
	}
	if len(fc.RelayBundleCalls) != 1 {
		t.Fatalf("RelayBundle called %d times, want 1: %+v", len(fc.RelayBundleCalls), fc.RelayBundleCalls)
	}
	want := forge.RelayBundleCall{OutboxDir: "/outbox/1", Ref: cf.AgentBranch("1")}
	if fc.RelayBundleCalls[0] != want {
		t.Errorf("RelayBundle call = %+v, want %+v", fc.RelayBundleCalls[0], want)
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called after conflict-resolve; fc.Merged=%q", fc.Merged)
	}
}

// A MarkDraft error on a genuine conflict is logged but never blocks the
// rebase/merge landing path (issue #1863), matching MarkReady's own
// best-effort contract at green.
func TestMergeImmediate_MarkDraftFailureIsBestEffort(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.MarkDraftErr = errors.New("gh pr ready --undo: permission denied")
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	var err error
	out := testutil.CaptureStdout(t, func() {
		err = s.mergeImmediate("1", 0, testPR, nil)
	})

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called to completion; fc.Merged=%q", fc.Merged)
	}
	if !strings.Contains(out, "mark-draft-failed") {
		t.Errorf("console output must log the MarkDraft failure; got: %q", out)
	}
}

// A MarkReady error while restoring ready-state after a conflict re-greens is
// logged but never blocks the merge (issue #1863).
func TestMergeImmediate_MarkReadyRestoreFailureIsBestEffort(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.MarkReadyErr = errors.New("gh pr ready: permission denied")
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	var err error
	out := testutil.CaptureStdout(t, func() {
		err = s.mergeImmediate("1", 0, testPR, nil)
	})

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called to completion; fc.Merged=%q", fc.Merged)
	}
	if !strings.Contains(out, "mark-ready-failed") {
		t.Errorf("console output must log the MarkReady restore failure; got: %q", out)
	}
}

// Once a rebase-conflict resolve has restored the PR to ready, the
// stale-mergeability-snapshot retry (skipRebase) must not demote it back to
// draft. That would attempt the final, successful Merge against a draft PR
// (issue #1863).
func TestMergeImmediate_StaleConflictRetryDoesNotRedemoteAfterRestore(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	sleeps, clock := recordingClock()
	c.Clock = clock
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, forge.ErrMergeConflict, nil}
	fc.RebaseErr = forge.ErrMergeConflict
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	df := dispatch.NewFake()
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, df)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	want := []string{
		"Merge:" + testPR,
		"MarkDraft:" + testPR,
		"MarkReady:" + testPR,
		"Merge:" + testPR,
		"Merge:" + testPR,
	}
	if !slices.Equal(fc.LandingCallLog, want) {
		t.Errorf("LandingCallLog = %v, want %v", fc.LandingCallLog, want)
	}
	if len(fc.MarkDraftCalls) != 1 {
		t.Errorf("MarkDraft called %d times, want 1 (stale-snapshot retry must not re-demote); calls=%v", len(fc.MarkDraftCalls), fc.MarkDraftCalls)
	}
	// The stale-snapshot retry must sleep through the injected clock, not a
	// bare time.Sleep (issue #2502): recordingClock records only what routes
	// through it, so a bare sleep leaves sleeps empty. The rewaitAfterForcePush
	// confirm-poll ahead of it also routes through s.clock and records the same
	// MergePollInterval duration first.
	pollSleep := time.Duration(c.MergePollInterval) * time.Second
	wantSleeps := []time.Duration{pollSleep, pollSleep}
	if !slices.Equal(*sleeps, wantSleeps) {
		t.Errorf("clock sleeps = %v, want %v (merge-retry-settle retry must sleep through s.clock)", *sleeps, wantSleeps)
	}
}

// A push-only Code Forge (s.pr == nil, e.g. CODE_FORGE=git/local) has no draft
// concept to demote to or restore from, so a merge conflict must not call
// MarkDraft or MarkReady (issue #1863).
func TestMergeImmediate_PushOnlyForgeNeverCallsMarkDraftOrMarkReady(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	s := newTestSettle(c, fc, fc.AsPushOnly())

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called to completion; fc.Merged=%q", fc.Merged)
	}
	if len(fc.MarkDraftCalls) != 0 {
		t.Errorf("push-only forge must never call MarkDraft; calls=%v", fc.MarkDraftCalls)
	}
	if len(fc.MarkReadyCalls) != 0 {
		t.Errorf("push-only forge must never call MarkReady; calls=%v", fc.MarkReadyCalls)
	}
}

// A Rebase force-push resets the PR's checks, so mergeImmediate must not retry
// the merge until a fresh gateToGreen wait confirms the new head is green
// (issue #567). No checks ever register here, so the re-wait times out.
func TestMergeImmediate_RewaitsAfterForcePush(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.MergePollTimeout = 0 // no checks ever register after the force-push
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err == nil {
		t.Fatal("mergeImmediate: want error when CI never reaches green after force-push, got nil")
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not succeed while the post-force-push CI wait never confirmed green; fc.Merged=%q", fc.Merged)
	}
	if len(fc.RebasedURLs) != 1 {
		t.Errorf("Rebase called %d times, want 1 (merge must wait for CI, not retry rebase)", len(fc.RebasedURLs))
	}
}

// Once the post-force-push re-wait confirms green, the merge proceeds and the
// stale-conflict retry consumes no further rebase attempt.
func TestMergeImmediate_RewaitGreenMergesWithoutFurtherRebase(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 1
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called to completion; fc.Merged=%q", fc.Merged)
	}
	if len(fc.RebasedURLs) != 1 {
		t.Errorf("Rebase called %d times, want 1 (single rebase attempt, no extra attempt consumed)", len(fc.RebasedURLs))
	}
}

// A re-wait ending in genuine CI failure, not just a timeout, returns an error
// without a second rebase attempt. It must not fold into the conflict-retry
// path.
func TestMergeImmediate_RewaitGenuineRedNotTreatedAsConflict(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err == nil {
		t.Fatal("mergeImmediate: want error when re-wait confirms genuine CI red, got nil")
	}
	if errors.Is(err, forge.ErrMergeConflict) {
		t.Errorf("genuine CI red after force-push must not surface as forge.ErrMergeConflict; got %v", err)
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not succeed after genuine CI red; fc.Merged=%q", fc.Merged)
	}
	if len(fc.RebasedURLs) != 1 {
		t.Errorf("Rebase called %d times, want 1 (no further rebase attempt on CI red)", len(fc.RebasedURLs))
	}
}

// A refusal classified as forge.ErrMergeBlockedByChecks (issue #566) triggers
// neither a rebase nor a conflict-resolve dispatch, and the status output names
// checks rather than a conflict. Its retry must also sleep through the injected
// clock, not a bare time.Sleep (issue #2502): recordingClock records only what
// routes through it, so a bare sleep leaves sleeps empty.
func TestMergeImmediate_BlockedByChecks(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	sleeps, clock := recordingClock()
	c.Clock = clock
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeBlockedByChecks, nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})

	df := dispatch.NewFake()
	s := newTestSettle(c, fc, fc)

	var err error
	out := testutil.CaptureStdout(t, func() {
		err = s.mergeImmediate("1", 0, testPR, df)
	})

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called to completion; fc.Merged=%q", fc.Merged)
	}
	if len(fc.RebasedURLs) != 0 {
		t.Errorf("blocked-by-checks must not trigger Rebase; called %d times", len(fc.RebasedURLs))
	}
	if len(df.ResolveConflictCalls) != 0 {
		t.Errorf("blocked-by-checks must not trigger conflict-resolve; called %d times", len(df.ResolveConflictCalls))
	}
	if !strings.Contains(out, "checks") {
		t.Errorf("status output must name checks as the reason the merge is waiting; got: %q", out)
	}
	if strings.Contains(out, "conflict") {
		t.Errorf("status output must not name a conflict for a blocked-by-checks refusal; got: %q", out)
	}
	if !strings.Contains(out, "landing="+testPR) {
		t.Errorf("console output must print landing=%s, not the stale pr= label; got: %q", testPR, out)
	}
	if stalePRLabel.MatchString(out) {
		t.Errorf("console output must not use the stale pr= label; got: %q", out)
	}
	if len(fc.MarkDraftCalls) != 0 {
		t.Errorf("blocked-by-checks is not a content conflict and must not demote to draft; MarkDraftCalls=%v", fc.MarkDraftCalls)
	}
	wantSleeps := []time.Duration{time.Duration(c.MergePollInterval) * time.Second}
	if !slices.Equal(*sleeps, wantSleeps) {
		t.Errorf("clock sleeps = %v, want %v (blocked-by-checks retry must sleep through s.clock)", *sleeps, wantSleeps)
	}
}

// A merge permanently blocked by checks bails out with ErrMergeBlockedByChecks
// rather than polling forever.
func TestMergeImmediate_BlockedByChecksExhausted(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 2
	fc := forge.NewFake()
	fc.MergeErr = forge.ErrMergeBlockedByChecks
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})

	s := newTestSettle(c, fc, fc)
	err := s.mergeImmediate("1", 0, testPR, nil)

	if !errors.Is(err, forge.ErrMergeBlockedByChecks) {
		t.Fatalf("want ErrMergeBlockedByChecks, got: %v", err)
	}
	if len(fc.RebasedURLs) != 0 {
		t.Errorf("blocked-by-checks must never trigger Rebase; called %d times", len(fc.RebasedURLs))
	}
}

// A PR the forge reports as behind its base (NeedsUpdate) is rebased and
// re-confirmed green before mergeImmediate calls Merge, even though it carries
// no textual conflict. That gap let #670 and #672 land a combined compile break
// on main (issue #936): each was green against its own stale base, and neither
// was re-tested against the other's changes before landing.
func TestMergeImmediate_StaleBaseTriggersProactiveRebase(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if len(fc.RebasedURLs) != 1 {
		t.Errorf("Rebase called %d times, want 1 (proactive rebase on stale base)", len(fc.RebasedURLs))
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called after rebase; fc.Merged=%q", fc.Merged)
	}
}

// This reproduces the #670 / #672 collision (issue #936): a PR is green and
// content-mergeable on its own stale base, but the forge reports it BEHIND. The
// proactive rebase re-tests it against the base that now holds the sibling, and
// that combined tree fails CI. mergeImmediate must return the failure and never
// call Merge, rather than landing the still-green-looking PR.
func TestMergeImmediate_StaleBaseCombinedBreakBlocksMerge(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err == nil {
		t.Fatal("mergeImmediate: want error when the rebased combined tree fails CI, got nil")
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called when the combined tree never re-confirmed green; fc.Merged=%q", fc.Merged)
	}
	if len(fc.RebasedURLs) != 1 {
		t.Errorf("Rebase called %d times, want 1", len(fc.RebasedURLs))
	}
}

// A NeedsUpdate query error must not block the landing. It is logged and
// swallowed, and the normal Merge attempt proceeds, reporting any real problem
// through its own error handling.
func TestMergeImmediate_StaleBaseCheckErrorFallsThroughToMerge(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.NeedsUpdateErr = errors.New("gh api graphql: rate limited")
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called after NeedsUpdate error was swallowed; fc.Merged=%q", fc.Merged)
	}
	if len(fc.RebasedURLs) != 0 {
		t.Errorf("Rebase called %d times, want 0 (no proactive rebase on a check error)", len(fc.RebasedURLs))
	}
}

// A persistent Rebase failure during the stale-base preflight (issue #940) is
// fatal to the landing. Unlike a NeedsUpdate query error, which leaves staleness
// merely unknown, it confirms staleness and a failed correction, so
// mergeImmediate must not fall through to Merge on an unrevalidated stale base.
func TestMergeImmediate_StaleBaseRebaseFailureBlocksMerge(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 2
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	// preflightStaleBase makes 1 initial Rebase call plus up to
	// MaxRebaseAttempts push-retries, so 3 calls total at MaxRebaseAttempts=2.
	// Every one must return the transient error to exhaust the budget.
	fc.RebaseErrs = []error{
		forge.ErrTransientPushFailure,
		forge.ErrTransientPushFailure,
		forge.ErrTransientPushFailure,
	}
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err == nil {
		t.Fatal("mergeImmediate: want error when the stale-base rebase never recovers, got nil")
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called after the stale-base rebase failed; fc.Merged=%q", fc.Merged)
	}
}

// A non-transient Rebase error short circuits the push-retry loop entirely, so
// there is one Rebase call and no retries, and it still blocks the merge. The
// nil dispatcher also pins issue #1319's d != nil guard: an ErrMergeConflict
// with no Dispatcher stays terminal instead of attempting ResolveConflict.
func TestMergeImmediate_StaleBaseNonTransientRebaseFailureBlocksMerge(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 2
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.RebaseErr = forge.ErrMergeConflict
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err == nil {
		t.Fatal("mergeImmediate: want error when the stale-base rebase fails non-transiently, got nil")
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called after the stale-base rebase failed; fc.Merged=%q", fc.Merged)
	}
	if len(fc.RebasedURLs) != 1 {
		t.Errorf("Rebase called %d times, want 1 (non-transient error must not enter the push-retry loop)", len(fc.RebasedURLs))
	}
}

// A genuine ErrMergeConflict from the stale-base preflight's rebase, unlike the
// ErrTransientPushFailure the push-retry loop handles, falls through to the same
// ResolveConflict dispatch the conflict-retry loop uses instead of blocking the
// merge. The preflight lost this fallback when #940 made it fatal (issue #1319).
func TestMergeImmediate_StaleBaseConflictResolvesViaDispatcher(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.RebaseErr = forge.ErrMergeConflict
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	df := dispatch.NewFake()
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, df)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if len(df.ResolveConflictCalls) != 1 {
		t.Errorf("ResolveConflict called %d times, want 1", len(df.ResolveConflictCalls))
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called after conflict-resolve; fc.Merged=%q", fc.Merged)
	}
	if len(fc.RebasedURLs) != 1 {
		t.Errorf("Rebase called %d times, want 1", len(fc.RebasedURLs))
	}
}

// A genuine ErrMergeConflict from the stale-base preflight's rebase flips the PR
// to draft before the conflict-resolve dispatch, then back to ready once the
// resolved head re-confirms green, all before the merge lands it (issue #1863).
func TestMergeImmediate_StaleBaseConflictDemotesToDraftAndRestoresOnGreen(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.RebaseErr = forge.ErrMergeConflict
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	df := dispatch.NewFake()
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, df)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	want := []string{"MarkDraft:" + testPR, "MarkReady:" + testPR, "Merge:" + testPR}
	if !slices.Equal(fc.LandingCallLog, want) {
		t.Errorf("LandingCallLog = %v, want %v", fc.LandingCallLog, want)
	}
}

// A transient push failure during the stale-base preflight's rebase is not a
// content conflict, so it never demotes the PR to draft (issue #1863), even
// though it retries and eventually succeeds.
func TestMergeImmediate_StaleBaseTransientPushFailureDoesNotDemote(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.RebaseErrs = []error{forge.ErrTransientPushFailure, nil}
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called to completion; fc.Merged=%q", fc.Merged)
	}
	if len(fc.MarkDraftCalls) != 0 {
		t.Errorf("a transient push failure is not a content conflict and must not demote to draft; MarkDraftCalls=%v", fc.MarkDraftCalls)
	}
}

// A MarkDraft error at the stale-base preflight's conflict site is logged but
// never blocks the conflict-resolve/rewait/merge landing path (issue #1863),
// matching the conflict-retry loop's own best-effort contract.
func TestMergeImmediate_StaleBaseMarkDraftFailureIsBestEffort(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.RebaseErr = forge.ErrMergeConflict
	fc.MarkDraftErr = errors.New("gh pr ready --undo: permission denied")
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	df := dispatch.NewFake()
	s := newTestSettle(c, fc, fc)

	var err error
	out := testutil.CaptureStdout(t, func() {
		err = s.mergeImmediate("1", 0, testPR, df)
	})

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called to completion; fc.Merged=%q", fc.Merged)
	}
	if !strings.Contains(out, "mark-draft-failed") {
		t.Errorf("console output must log the MarkDraft failure; got: %q", out)
	}
}

// When the stale-base preflight's ResolveConflict dispatch fails, the merge is
// blocked with an errLandingNeverGreen-wrapped error rather than the raw
// ErrMergeConflict, and Merge is never attempted.
func TestMergeImmediate_StaleBaseConflictResolveFailureBlocksMerge(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.RebaseErr = forge.ErrMergeConflict
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	df := dispatch.NewFake()
	df.ResolveConflictErr = errors.New("agent could not resolve conflict")
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, df)

	if err == nil {
		t.Fatal("mergeImmediate: want error when preflight conflict-resolve fails, got nil")
	}
	if !errors.Is(err, errLandingNeverGreen) {
		t.Errorf("mergeImmediate err=%v, want wrapped errLandingNeverGreen", err)
	}
	if len(df.ResolveConflictCalls) != 1 {
		t.Errorf("ResolveConflict called %d times, want 1", len(df.ResolveConflictCalls))
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called after preflight conflict-resolve failed; fc.Merged=%q", fc.Merged)
	}
}

// When the stale-base preflight's ResolveConflict succeeds but the re-wait for
// green after its force-push never confirms, the merge is blocked rather than
// falling through to Merge on an unconfirmed head.
func TestMergeImmediate_StaleBaseConflictResolveRewaitFailsBlocksMerge(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	c.MergePollTimeout = 0 // no checks ever register after the force-push
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.RebaseErr = forge.ErrMergeConflict
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	df := dispatch.NewFake()
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, df)

	if err == nil {
		t.Fatal("mergeImmediate: want error when the post-resolve re-wait never goes green, got nil")
	}
	if errors.Is(err, forge.ErrMergeConflict) {
		t.Errorf("mergeImmediate err=%v must not be a raw ErrMergeConflict — that would re-enter the reactive conflict-retry path instead of surfacing rewaitAfterForcePush's own failure", err)
	}
	if len(df.ResolveConflictCalls) != 1 {
		t.Errorf("ResolveConflict called %d times, want 1", len(df.ResolveConflictCalls))
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called when the post-resolve re-wait never confirmed green; fc.Merged=%q", fc.Merged)
	}
}

// MaxRebaseAttempts=0 disables the stale-base preflight even with
// PreflightStaleBase on. NeedsUpdate is true, so the !stale short circuit cannot
// hide the MaxRebaseAttempts disjunct.
func TestMergeImmediate_StaleBaseSkippedWhenRebaseDisabled(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 0
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if len(fc.RebasedURLs) != 0 {
		t.Errorf("Rebase called %d times, want 0 (MaxRebaseAttempts=0 disables the preflight)", len(fc.RebasedURLs))
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called after the disabled preflight fell through; fc.Merged=%q", fc.Merged)
	}
}

// The default (ADR 0027): with PreflightStaleBase off, a green PR behind its
// base merges as-is. NeedsUpdate is never queried, sparing a compare-API round
// trip, and Rebase is never called even though MaxRebaseAttempts would allow it,
// because only a genuine conflict on the Merge attempt triggers a rebase.
func TestMergeImmediate_StaleBaseSkippedWhenPreflightOff(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	// A NeedsUpdate call would fault here; the preflight must not make one.
	fc.NeedsUpdateErr = errors.New("NeedsUpdate must not be called when the preflight is off")
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	if len(fc.RebasedURLs) != 0 {
		t.Errorf("Rebase called %d times, want 0 (preflight off merges a stale-but-green PR as-is)", len(fc.RebasedURLs))
	}
	if fc.Merged != testPR {
		t.Errorf("Merge not called; fc.Merged=%q", fc.Merged)
	}
}

func TestApplyMergeMode_Immediate(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.applyMergeMode("1", 0, testPR, nil)
	if err != nil {
		t.Errorf("applyMergeMode immediate: unexpected error: %v", err)
	}
	if fc.Merged != testPR {
		t.Errorf("immediate mode must call Merge; fc.Merged=%q", fc.Merged)
	}
}

func TestApplyMergeMode_Manual(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "manual"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.applyMergeMode("1", 0, testPR, nil)
	if err != nil {
		t.Errorf("applyMergeMode manual: unexpected error: %v", err)
	}
	if fc.Merged != "" {
		t.Errorf("manual mode must not call Merge; fc.Merged=%q", fc.Merged)
	}
}

func TestApplyMergeMode_Auto_EnqueuesAutoMerge(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "auto"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc)

	err := s.applyMergeMode("1", 0, testPR, nil)
	if err != nil {
		t.Errorf("applyMergeMode auto: unexpected error: %v", err)
	}
	if fc.Merged != "" {
		t.Errorf("auto mode must not call Merge; fc.Merged=%q", fc.Merged)
	}
	if len(fc.EnqueueAutoMergeCalls) != 1 || fc.EnqueueAutoMergeCalls[0] != testPR {
		t.Errorf("auto mode must call EnqueueAutoMerge(%q); calls=%v", testPR, fc.EnqueueAutoMergeCalls)
	}
}

// MERGE_MODE=auto against a push-only Code Forge with no PRForge (CODE_FORGE=git
// reaching applyMergeMode through recover or selective dispatch, neither of
// which runs the run()-only auto-merge preflight) must return an actionable
// error instead of dereferencing the absent PRForge.
func TestApplyMergeMode_Auto_PushOnlyForgeReturnsError(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "auto"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := newTestSettle(c, fc, fc.AsPushOnly())

	err := s.applyMergeMode("1", 0, testPR, nil)
	if err == nil {
		t.Fatal("applyMergeMode auto on a push-only forge: want error, got nil")
	}
}

// An EnqueueAutoMerge failure must not fail the agent: applyMergeMode returns
// nil and posts a warning comment to the issue.
func TestApplyMergeMode_Auto_EnqueueFailureFallsBack(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "auto"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	fc.EnqueueAutoMergeErr = fmt.Errorf("gh pr merge --auto: permission denied")
	s := newTestSettle(c, fc, fc)

	err := s.applyMergeMode("1", 0, testPR, nil)
	if err != nil {
		t.Errorf("auto mode enqueue failure must not propagate error; got: %v", err)
	}
	if fc.Merged != "" {
		t.Errorf("auto mode must not call Merge; fc.Merged=%q", fc.Merged)
	}
	if len(fc.EnqueueAutoMergeCalls) == 0 {
		t.Error("EnqueueAutoMerge must have been called")
	}
	if len(fc.CommentCalls) == 0 {
		t.Error("a warning comment must be posted when auto-merge enqueue fails")
	}
}
