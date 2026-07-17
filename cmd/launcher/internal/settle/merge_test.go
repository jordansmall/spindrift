package settle

import (
	"errors"
	"fmt"
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
