package settle

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/testutil"
)

// TestMergeImmediate verifies the rebase-retry and conflict-resolve behaviors
// that run inside applyMergeMode for the immediate merge mode. Conflict
// resolution is routed through a dispatch.Dispatcher (issue #442) instead of
// a raw callback.
func TestMergeImmediate(t *testing.T) {
	cases := []struct {
		name                      string
		maxRebaseAttempts         int
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
			// After conflict-resolve succeeds, the forge's mergeability
			// snapshot is briefly stale and the next Merge still reports a
			// conflict. The loop must retry Merge directly instead of
			// invoking Rebase a second time (the box already rebased and
			// force-pushed).
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
			// the force-push must not block the merge outright — it's
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
			// The forge stays down: every retry hits the same transient
			// error. The retry must be bounded — not spin indefinitely —
			// and the eventual failure must still surface to the caller.
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

			s := New(c, fc, fc)
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

// TestMergeImmediate_ConflictDemotesToDraftAndRestoresOnGreen verifies that a
// genuine ErrMergeConflict on the reactive conflict-retry loop's initial
// Merge attempt flips the PR to draft before the rebase, and flips it back
// to ready once the rebased head re-confirms green — before the retried
// Merge that lands it (issue #1863).
func TestMergeImmediate_ConflictDemotesToDraftAndRestoresOnGreen(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	want := []string{"Merge:" + testPR, "MarkDraft:" + testPR, "MarkReady:" + testPR, "Merge:" + testPR}
	if !slices.Equal(fc.LandingCallLog, want) {
		t.Errorf("LandingCallLog = %v, want %v", fc.LandingCallLog, want)
	}
}

// TestMergeImmediate_MarkDraftFailureIsBestEffort verifies that a MarkDraft
// error on a genuine conflict is logged to the console but never blocks the
// rebase/merge landing path (issue #1863) — matching MarkReady's own
// best-effort contract at green.
func TestMergeImmediate_MarkDraftFailureIsBestEffort(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.MarkDraftErr = errors.New("gh pr ready --undo: permission denied")
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

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

// TestMergeImmediate_MarkReadyRestoreFailureIsBestEffort verifies that a
// MarkReady error while restoring ready-state after a conflict re-greens is
// logged to the console but never blocks the merge (issue #1863).
func TestMergeImmediate_MarkReadyRestoreFailureIsBestEffort(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.MarkReadyErr = errors.New("gh pr ready: permission denied")
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

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

// TestMergeImmediate_StaleConflictRetryDoesNotRedemoteAfterRestore verifies
// that once a rebase-conflict resolve has restored the PR to ready, the
// stale-mergeability-snapshot retry (skipRebase) does not demote it back to
// draft — that would leave the final, successful Merge attempted against a
// draft PR, violating "the subsequent merge is never attempted against a
// draft PR" (issue #1863).
func TestMergeImmediate_StaleConflictRetryDoesNotRedemoteAfterRestore(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, forge.ErrMergeConflict, nil}
	fc.RebaseErr = forge.ErrMergeConflict
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	df := dispatch.NewFake()
	s := New(c, fc, fc)

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
}

// TestMergeImmediate_PushOnlyForgeNeverCallsMarkDraftOrMarkReady verifies
// that a push-only Code Forge (s.pr == nil, e.g. CODE_FORGE=git/local) never
// calls MarkDraft or MarkReady on a merge conflict — there is no draft
// concept to demote to or restore from (issue #1863), mirroring the
// existing rewaitAfterForcePush / MarkReady-at-green guards.
func TestMergeImmediate_PushOnlyForgeNeverCallsMarkDraftOrMarkReady(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	s := New(c, fc, fc.AsPushOnly())

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

// TestMergeImmediate_RewaitsAfterForcePush verifies that a Rebase force-push
// resets the PR's checks, so mergeImmediate must not retry the merge until a
// fresh gateToGreen wait confirms the new head is green (issue #567). With no
// checks ever registering, the re-wait times out and the merge must not be
// retried a second time.
func TestMergeImmediate_RewaitsAfterForcePush(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.MergePollTimeout = 0 // no checks ever register after the force-push
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

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

// TestMergeImmediate_RewaitGreenMergesWithoutFurtherRebase verifies that once
// the post-force-push re-wait confirms green, the merge proceeds and the
// stale-conflict retry consumes no further rebase attempt.
func TestMergeImmediate_RewaitGreenMergesWithoutFurtherRebase(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 1
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

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

// TestMergeImmediate_RewaitGenuineRedNotTreatedAsConflict verifies that a
// re-wait ending in genuine CI failure (not just a timeout) is surfaced as an
// error without dispatching a second rebase attempt — it must not be folded
// into the conflict-retry path.
func TestMergeImmediate_RewaitGenuineRedNotTreatedAsConflict(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

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

// TestMergeImmediate_BlockedByChecks verifies that a merge refusal classified
// as forge.ErrMergeBlockedByChecks (issue #566) triggers neither a rebase nor
// a conflict-resolve dispatch, and that the status output names checks — not
// a conflict — as the reason the merge is waiting.
func TestMergeImmediate_BlockedByChecks(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.MergeErrs = []error{forge.ErrMergeBlockedByChecks, nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})

	df := dispatch.NewFake()
	s := New(c, fc, fc)

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
}

// TestMergeImmediate_BlockedByChecksExhausted verifies that a merge
// permanently blocked by checks eventually bails out with the
// ErrMergeBlockedByChecks error, rather than polling forever.
func TestMergeImmediate_BlockedByChecksExhausted(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 2
	fc := forge.NewFake()
	fc.MergeErr = forge.ErrMergeBlockedByChecks
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})

	s := New(c, fc, fc)
	err := s.mergeImmediate("1", 0, testPR, nil)

	if !errors.Is(err, forge.ErrMergeBlockedByChecks) {
		t.Fatalf("want ErrMergeBlockedByChecks, got: %v", err)
	}
	if len(fc.RebasedURLs) != 0 {
		t.Errorf("blocked-by-checks must never trigger Rebase; called %d times", len(fc.RebasedURLs))
	}
}

// TestMergeImmediate_StaleBaseTriggersProactiveRebase verifies that a PR the
// forge reports as behind its base (NeedsUpdate) is rebased and
// re-confirmed green *before* mergeImmediate ever calls Merge — even though
// the PR carries no textual conflict and Merge would otherwise succeed
// outright. This is the gap that let #670 and #672 land a combined compile
// break on main (issue #936): each was individually green against its own
// stale base, but neither was ever rebased and re-tested against the
// other's changes before landing.
func TestMergeImmediate_StaleBaseTriggersProactiveRebase(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

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

// TestMergeImmediate_StaleBaseCombinedBreakBlocksMerge reproduces the #670 /
// #672 collision itself (issue #936): a PR is green and content-mergeable on
// its own stale base, but the forge reports it BEHIND. The proactive rebase
// re-tests it against the (now-merged-sibling-containing) base, and here
// that combined tree fails CI — exactly the go-vet break the sibling merge
// introduced. mergeImmediate must surface that failure and never call
// Merge, rather than landing the still-green-looking PR.
func TestMergeImmediate_StaleBaseCombinedBreakBlocksMerge(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

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

// TestMergeImmediate_StaleBaseCheckErrorFallsThroughToMerge verifies that a
// NeedsUpdate query error does not block the landing outright — it is
// logged and swallowed, and the normal Merge attempt proceeds (surfacing any
// real problem through its own, already-tested error handling instead).
func TestMergeImmediate_StaleBaseCheckErrorFallsThroughToMerge(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.NeedsUpdateErr = errors.New("gh api graphql: rate limited")
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

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

// TestMergeImmediate_StaleBaseRebaseFailureBlocksMerge verifies that a
// persistent Rebase failure during the stale-base preflight (issue #940) is
// fatal to the landing — unlike a NeedsUpdate query error (staleness merely
// unknown), a Rebase failure here means staleness is confirmed and the
// corrective action itself failed, so mergeImmediate must not fall through
// to Merge on an unrevalidated stale base.
func TestMergeImmediate_StaleBaseRebaseFailureBlocksMerge(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 2
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	// preflightStaleBase makes 1 initial Rebase call plus up to
	// MaxRebaseAttempts push-retries — 3 calls total for MaxRebaseAttempts=2
	// above. Every one must return the transient error to exhaust the budget.
	fc.RebaseErrs = []error{
		forge.ErrTransientPushFailure,
		forge.ErrTransientPushFailure,
		forge.ErrTransientPushFailure,
	}
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if err == nil {
		t.Fatal("mergeImmediate: want error when the stale-base rebase never recovers, got nil")
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called after the stale-base rebase failed; fc.Merged=%q", fc.Merged)
	}
}

// TestMergeImmediate_StaleBaseNonTransientRebaseFailureBlocksMerge verifies
// that a non-transient Rebase error (not forge.ErrTransientPushFailure) short
// circuits the push-retry loop entirely — a single Rebase call, no retries —
// and still blocks the merge the same way the retries-exhausted case does.
// Passing a nil dispatcher also makes this the regression coverage for
// issue #1319's d != nil guard: an ErrMergeConflict with no Dispatcher
// available must stay terminal, not attempt ResolveConflict.
func TestMergeImmediate_StaleBaseNonTransientRebaseFailureBlocksMerge(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 2
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.RebaseErr = forge.ErrMergeConflict
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

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

// TestMergeImmediate_StaleBaseConflictResolvesViaDispatcher verifies that a
// genuine ErrMergeConflict surfaced by the stale-base preflight's rebase (as
// opposed to ErrTransientPushFailure, which the push-retry loop already
// handles) falls through to the same ResolveConflict dispatch the reactive
// conflict-retry loop uses, rather than hard-blocking the merge (issue
// #1319 — the preflight lost this fallback when #940 made it fatal).
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
	s := New(c, fc, fc)

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

// TestMergeImmediate_StaleBaseConflictDemotesToDraftAndRestoresOnGreen
// verifies that a genuine ErrMergeConflict surfaced by the stale-base
// preflight's rebase flips the PR to draft before the conflict-resolve
// dispatch, and flips it back to ready once the resolved head re-confirms
// green — before the merge that lands it (issue #1863).
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
	s := New(c, fc, fc)

	err := s.mergeImmediate("1", 0, testPR, df)

	if err != nil {
		t.Fatalf("mergeImmediate: unexpected error: %v", err)
	}
	want := []string{"MarkDraft:" + testPR, "MarkReady:" + testPR, "Merge:" + testPR}
	if !slices.Equal(fc.LandingCallLog, want) {
		t.Errorf("LandingCallLog = %v, want %v", fc.LandingCallLog, want)
	}
}

// TestMergeImmediate_StaleBaseTransientPushFailureDoesNotDemote verifies
// that a transient push failure during the stale-base preflight's rebase —
// not a content conflict — never demotes the PR to draft (issue #1863),
// even though it retries and eventually succeeds.
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
	s := New(c, fc, fc)

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

// TestMergeImmediate_StaleBaseMarkDraftFailureIsBestEffort verifies that a
// MarkDraft error on the stale-base preflight's conflict site is logged to
// the console but never blocks the conflict-resolve/rewait/merge landing
// path (issue #1863) — matching the reactive conflict-retry loop's own
// best-effort contract.
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
	s := New(c, fc, fc)

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

// TestMergeImmediate_StaleBaseConflictResolveFailureBlocksMerge verifies
// that when the stale-base preflight's ResolveConflict dispatch itself
// fails, the merge is blocked with an errLandingNeverGreen-wrapped error
// rather than the raw ErrMergeConflict, and Merge is never attempted.
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
	s := New(c, fc, fc)

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

// TestMergeImmediate_StaleBaseConflictResolveRewaitFailsBlocksMerge verifies
// that when the stale-base preflight's ResolveConflict succeeds but the
// re-wait for green after its force-push never confirms, the merge is
// blocked rather than falling through to Merge on an unconfirmed head.
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
	s := New(c, fc, fc)

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

// TestMergeImmediate_StaleBaseSkippedWhenRebaseDisabled verifies that
// MaxRebaseAttempts=0 disables the stale-base preflight outright even with the
// PreflightStaleBase flag on — the forge reports the PR behind its base, yet
// Rebase is never called and mergeImmediate falls straight through to the
// normal Merge attempt. NeedsUpdate=true so the !stale short circuit can't
// hide the MaxRebaseAttempts disjunct.
func TestMergeImmediate_StaleBaseSkippedWhenRebaseDisabled(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 0
	c.PreflightStaleBase = true
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

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

// TestMergeImmediate_StaleBaseSkippedWhenPreflightOff verifies the default
// (ADR 0027): with PreflightStaleBase off, a green PR that is behind its base
// merges as-is. NeedsUpdate is never even queried (no wasted compare-API
// round-trip) and Rebase is never called, even though MaxRebaseAttempts would
// otherwise allow it — only a genuine conflict on the Merge attempt triggers a
// rebase, and there is none here.
func TestMergeImmediate_StaleBaseSkippedWhenPreflightOff(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	// c.PreflightStaleBase left false (the default).
	fc := forge.NewFake()
	fc.SetNeedsUpdate(testPR, true)
	// A NeedsUpdate call would fault here; the preflight must not make one.
	fc.NeedsUpdateErr = errors.New("NeedsUpdate must not be called when the preflight is off")
	fc.MergeErrs = []error{nil}
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

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

// TestApplyMergeMode_Immediate verifies that immediate mode calls fc.Merge.
func TestApplyMergeMode_Immediate(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

	err := s.applyMergeMode("1", 0, testPR, nil)
	if err != nil {
		t.Errorf("applyMergeMode immediate: unexpected error: %v", err)
	}
	if fc.Merged != testPR {
		t.Errorf("immediate mode must call Merge; fc.Merged=%q", fc.Merged)
	}
}

// TestApplyMergeMode_Manual verifies that manual mode does not call fc.Merge.
func TestApplyMergeMode_Manual(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "manual"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

	err := s.applyMergeMode("1", 0, testPR, nil)
	if err != nil {
		t.Errorf("applyMergeMode manual: unexpected error: %v", err)
	}
	if fc.Merged != "" {
		t.Errorf("manual mode must not call Merge; fc.Merged=%q", fc.Merged)
	}
}

// TestApplyMergeMode_Auto_EnqueuesAutoMerge verifies that auto mode calls
// EnqueueAutoMerge and does not call fc.Merge.
func TestApplyMergeMode_Auto_EnqueuesAutoMerge(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "auto"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc)

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

// TestApplyMergeMode_Auto_PushOnlyForgeReturnsError verifies that MERGE_MODE=auto
// against a push-only Code Forge (no PRForge — e.g. CODE_FORGE=git reaching
// applyMergeMode via recover/selective dispatch, which do not run the
// run()-only auto-merge preflight) returns an actionable error instead of
// nil-dereferencing the absent PRForge.
func TestApplyMergeMode_Auto_PushOnlyForgeReturnsError(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "auto"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	s := New(c, fc, fc.AsPushOnly())

	err := s.applyMergeMode("1", 0, testPR, nil)
	if err == nil {
		t.Fatal("applyMergeMode auto on a push-only forge: want error, got nil")
	}
}

// TestApplyMergeMode_Auto_EnqueueFailureFallsBack verifies that when
// EnqueueAutoMerge fails, applyMergeMode returns nil (no agent-failed) and
// posts a warning comment to the issue.
func TestApplyMergeMode_Auto_EnqueueFailureFallsBack(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "auto"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-complete"}})
	fc.EnqueueAutoMergeErr = fmt.Errorf("gh pr merge --auto: permission denied")
	s := New(c, fc, fc)

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
