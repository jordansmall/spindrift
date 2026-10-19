package settle

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// A merge failure after CI reaches green leaves the issue at agent-complete,
// never agent-failed.
func TestSelfHeal_MergeFailureAfterGreenKeepsComplete(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 0
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErr = errors.New("required review missing")
	s := newTestSettle(c, fc, fc)

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual (CI green, merge failed)", landing)
	}
	assertLatchedLeftOpen(t, s, "1")
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after green+merge-failure; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed after merge failure on green PR; labels=%v", iss.Labels)
	}
}

// A PR touching a guarded path is never merged, whatever MERGE_MODE says. The
// launcher instead posts a comment naming the matched paths and the knob, and
// leaves the issue at agent-complete like a manual-mode green PR.
func TestSelfHeal_MergeGuardHit_DowngradesToManual(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MergeGuardPaths = ".github/**,**/CLAUDE.md"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetPRFiles(testPR, []string{"src/main.go", ".github/workflows/ci.yml"})
	s := newTestSettle(c, fc, fc)

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual (merge guard hit)", landing)
	}
	assertLatchedLeftOpen(t, s, "1")
	if fc.Merged != "" {
		t.Errorf("merge guard must prevent Merge from being called; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after a guard-downgraded green PR; labels=%v", iss.Labels)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("expected exactly one guard comment, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
	body := fc.CommentCalls[0].Body
	if !strings.Contains(body, ".github/workflows/ci.yml") {
		t.Errorf("comment must name the matched path; body=%q", body)
	}
	if !strings.Contains(body, "MERGE_GUARD_PATHS") {
		t.Errorf("comment must name the knob that triggered it; body=%q", body)
	}
}

// The guard covers .forgejo/ workflow paths just as it covers .github/: it
// protects CI config on whichever forge backend the repo runs on.
func TestSelfHeal_MergeGuardHit_ForgejoPath(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MergeGuardPaths = ".github/**,.forgejo/**"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetPRFiles(testPR, []string{"src/main.go", ".forgejo/workflows/ci.yml"})
	s := newTestSettle(c, fc, fc)

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual (merge guard hit on .forgejo/ path)", landing)
	}
	if fc.Merged != "" {
		t.Errorf("merge guard must prevent Merge from being called; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after a guard-downgraded green PR; labels=%v", iss.Labels)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("expected exactly one guard comment, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
	body := fc.CommentCalls[0].Body
	if !strings.Contains(body, ".forgejo/workflows/ci.yml") {
		t.Errorf("comment must name the matched path; body=%q", body)
	}
	if !strings.Contains(body, "MERGE_GUARD_PATHS") {
		t.Errorf("comment must name the knob that triggered it; body=%q", body)
	}
}

// The guard fires under MERGE_MODE=auto, not just immediate.
func TestSelfHeal_MergeGuardHit_AutoMode(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "auto"
	c.MergeGuardPaths = ".github/**"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetPRFiles(testPR, []string{".github/workflows/ci.yml"})
	s := newTestSettle(c, fc, fc)

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual for a guard-hit auto-mode PR", landing)
	}
	assertLatchedLeftOpen(t, s, "1")
	if len(fc.EnqueueAutoMergeCalls) != 0 {
		t.Errorf("guard hit must prevent EnqueueAutoMerge; calls=%v", fc.EnqueueAutoMergeCalls)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("expected exactly one guard comment, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
}

// A green PR touching no guarded path proceeds exactly as it would with no
// guard set.
func TestSelfHeal_MergeGuardMiss_MergesNormally(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MergeGuardPaths = ".github/**,**/CLAUDE.md"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetPRFiles(testPR, []string{"src/main.go"})
	s := newTestSettle(c, fc, fc)

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	if landing != landingMerged {
		t.Errorf("selfHeal = %v, want landingMerged for a non-guarded green PR", landing)
	}
	if fc.Merged != testPR {
		t.Errorf("expected Merge to be called; fc.Merged=%q", fc.Merged)
	}
	if len(fc.CommentCalls) != 0 {
		t.Errorf("no guard comment expected on a miss; got %+v", fc.CommentCalls)
	}
}

// When the changed-file list cannot be read at all, selfHeal fails safe: no
// merge, a precautionary comment, and the issue stays at agent-complete rather
// than silently falling through to MERGE_MODE.
func TestSelfHeal_MergeGuardCheckError_FailsSafe(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MergeGuardPaths = ".github/**"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.PRFilesErr = errors.New("gh api pulls files: 403 Forbidden")
	s := newTestSettle(c, fc, fc)

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual (guard check errored)", landing)
	}
	assertLatchedLeftOpen(t, s, "1")
	if fc.Merged != "" {
		t.Errorf("a guard-check error must prevent Merge from being called; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after a guard-check error on a green PR; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed after a guard-check error; labels=%v", iss.Labels)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("expected exactly one precautionary comment, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
}

// A merge-guard check error still flips the PR out of draft before downgrading
// to manual. MarkReady runs unconditionally on green so a human reviewing the
// fail-safe hand-off can see and merge the PR.
func TestSelfHeal_MergeGuardCheckError_FlipsReadyBeforeHandoff(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MergeGuardPaths = ".github/**"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.PRFilesErr = errors.New("gh api pulls files: 403 Forbidden")
	s := newTestSettle(c, fc, fc)

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)

	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual (guard check errored)", landing)
	}
	if len(fc.MarkReadyCalls) != 1 {
		t.Errorf("guard-check error must still call MarkReady exactly once; calls=%v", fc.MarkReadyCalls)
	}
}

// A failed conflict-resolve dispatch leaves the head in an unresolved-conflict
// state, never green, so the issue ends agent-failed (issue #758).
func TestSelfHeal_ConflictResolveFailure_EndsFailed(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	c.MaxFixAttempts = 2
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrMergeConflict
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)
	d := dispatch.NewFake()
	d.ResolveConflictErr = errors.New("conflict-resolve box exited 1")

	landing, reason := s.selfHeal(d, "1", 0, testPR, nil)

	if landing != landingFailed {
		t.Errorf("selfHeal = %v, want landingFailed (conflict-resolve dispatch failed)", landing)
	}
	if !strings.Contains(reason, "conflict-resolve dispatch failed") {
		t.Errorf("selfHeal reason = %q, want a substring containing %q", reason, "conflict-resolve dispatch failed")
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed after a failed conflict-resolve dispatch; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must NOT carry agent-complete after a failed conflict-resolve dispatch; labels=%v", iss.Labels)
	}
	if len(d.FixCalls) != 0 {
		t.Errorf("a failed conflict-resolve must not dispatch a fix pass; FixCalls=%+v", d.FixCalls)
	}
}

// A conflict-resolve dispatch skipped because the issue's own Box is already
// live (runner.ErrAlreadyRunning) is not a failed resolve: the live run owns the
// issue, so selfHeal abandons without a Failed transition or comment (#3655).
func TestSelfHeal_ConflictResolveAlreadyRunning_ReportsAbandonedNotFailed(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrMergeConflict
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)
	d := dispatch.NewFake()
	d.ResolveConflictErr = fmt.Errorf("resolve conflict: %w", runner.ErrAlreadyRunning)

	landing, reason := s.selfHeal(d, "1", 0, testPR, nil)

	if landing != landingAbandoned {
		t.Errorf("selfHeal = %v, want landingAbandoned", landing)
	}
	if reason != "" {
		t.Errorf("selfHeal reason = %q, want empty", reason)
	}
	assertNoTransitionOrComment(t, fc)
	assertClaimUntouched(t, fc)
}

// When the post-force-push re-wait ends in genuine red CI with no fix budget
// left, or times out even with budget to spare, the issue ends agent-failed
// without a fix pass: the force-pushed head never produced a green PR (issues
// #758, #5095).
func TestSelfHeal_RewaitAfterForcePush_NeverGreen_EndsFailed(t *testing.T) {
	cases := []struct {
		name        string
		rebaseErr   error
		resolveErr  error
		pollTimeout int
		maxFix      int
		checkStates []forge.RollupState
	}{
		{
			name: "rewait after a plain rebase ends genuine red with no fix budget",
			// Rebase succeeds outright, so mergeImmediate goes straight to the
			// post-force-push re-wait without a conflict-resolve.
			rebaseErr:   nil,
			pollTimeout: 100,
			checkStates: []forge.RollupState{
				forge.StateSuccess, forge.StateSuccess, // initial gateToGreen
				forge.StateFailure, // rewait's gateToGreen: genuine red
			},
		},
		{
			name: "rewait after conflict-resolve times out",
			// Rebase itself conflicts, so mergeImmediate dispatches
			// conflict-resolve, which succeeds; the re-wait then times out with
			// no checks ever registering.
			rebaseErr:   forge.ErrMergeConflict,
			resolveErr:  nil,
			pollTimeout: 0,
			maxFix:      2,
			checkStates: []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.MergeMode = "immediate"
			c.MaxRebaseAttempts = 3
			c.MaxFixAttempts = tc.maxFix
			c.MergePollTimeout = tc.pollTimeout
			fc := forge.NewFake(testDispatchLabels)
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
			fc.MergeErrs = []error{forge.ErrMergeConflict}
			fc.RebaseErr = tc.rebaseErr
			fc.SetCheckStates(testPR, tc.checkStates)
			s := newTestSettle(c, fc, fc)
			d := dispatch.NewFake()
			d.ResolveConflictErr = tc.resolveErr

			landing, reason := s.selfHeal(d, "1", 0, testPR, nil)

			if landing != landingFailed {
				t.Errorf("selfHeal = %v, want landingFailed (force-pushed head never went green)", landing)
			}
			if !strings.Contains(reason, errLandingNeverGreen.Error()) {
				t.Errorf("selfHeal reason = %q, want a substring containing %q", reason, errLandingNeverGreen.Error())
			}
			iss, _ := fc.Issue("1")
			if !containsLabel(iss.Labels, "agent-failed") {
				t.Errorf("issue must carry agent-failed; labels=%v", iss.Labels)
			}
			if containsLabel(iss.Labels, "agent-complete") {
				t.Errorf("issue must NOT carry agent-complete; labels=%v", iss.Labels)
			}
			if len(d.FixCalls) != 0 {
				t.Errorf("no fix pass may run here; FixCalls=%+v", d.FixCalls)
			}
		})
	}
}

// A post-force-push re-wait that ends genuinely red dispatches the red-CI fix
// pass instead of settling Failed (issue #5095); the head the fix turns green
// re-enters the merge path like a first-green head.
func TestSelfHeal_RewaitRed_DispatchesFixPassThenMerges(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	c.MaxFixAttempts = 2
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.SetCheckStates(testPR, []forge.RollupState{
		forge.StateSuccess, forge.StateSuccess, // initial gateToGreen
		forge.StateFailure,                     // rewait after the rebase force-push: red
		forge.StateSuccess, forge.StateSuccess, // after the fix pass
	})
	s := newTestSettle(c, fc, fc)
	d := dispatch.NewFake()

	landing, reason := s.selfHeal(d, "1", 0, testPR, nil)

	if landing != landingMerged {
		t.Errorf("selfHeal = %v (%q), want landingMerged", landing, reason)
	}
	if len(d.FixCalls) != 1 || d.FixCalls[0].Pass != 1 {
		t.Errorf("want exactly fix pass 1; FixCalls=%+v", d.FixCalls)
	}
	if fc.Merged != testPR {
		t.Errorf("fixed head must be merged; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") || containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-complete only; labels=%v", iss.Labels)
	}
}

// The post-force-push fix pass draws from the same MaxFixAttempts budget as the
// pre-merge red loop, not a fresh allowance (issue #5095).
func TestSelfHeal_RewaitRed_SharesFixBudget(t *testing.T) {
	cases := []struct {
		name       string
		maxFix     int
		states     []forge.RollupState
		wantFixes  int
		wantInNote string
	}{
		{
			name:   "pre-merge fix spent the whole budget",
			maxFix: 1,
			states: []forge.RollupState{
				forge.StateFailure,                     // pre-merge red
				forge.StateSuccess, forge.StateSuccess, // after fix 1
				forge.StateFailure, // rewait after the force-push: red again
			},
			wantFixes:  1,
			wantInNote: "1 of 1 fix pass(es) used, 0 on the force-pushed head",
		},
		{
			name:   "no fix budget at all",
			maxFix: 0,
			states: []forge.RollupState{
				forge.StateSuccess, forge.StateSuccess,
				forge.StateFailure,
			},
			wantFixes:  0,
			wantInNote: "no fix passes allowed (MAX_FIX_ATTEMPTS=0)",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.MergeMode = "immediate"
			c.MaxRebaseAttempts = 3
			c.MaxFixAttempts = tc.maxFix
			fc := forge.NewFake(testDispatchLabels)
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
			fc.MergeErrs = []error{forge.ErrMergeConflict}
			fc.SetCheckStates(testPR, tc.states)
			s := newTestSettle(c, fc, fc)
			d := dispatch.NewFake()

			landing, reason := s.selfHeal(d, "1", 0, testPR, nil)

			if landing != landingFailed {
				t.Errorf("selfHeal = %v, want landingFailed", landing)
			}
			if !strings.Contains(reason, errLandingNeverGreen.Error()) {
				t.Errorf("reason = %q, want it to wrap %q", reason, errLandingNeverGreen.Error())
			}
			if len(d.FixCalls) != tc.wantFixes {
				t.Errorf("FixCalls = %+v, want %d", d.FixCalls, tc.wantFixes)
			}
			iss, _ := fc.Issue("1")
			if !containsLabel(iss.Labels, "agent-failed") || containsLabel(iss.Labels, "agent-complete") {
				t.Errorf("issue must carry agent-failed only; labels=%v", iss.Labels)
			}
			found := false
			for _, cc := range fc.CommentCalls {
				if strings.Contains(cc.Body, tc.wantInNote) {
					found = true
				}
			}
			if !found {
				t.Errorf("no comment names %q; comments=%+v", tc.wantInNote, fc.CommentCalls)
			}
		})
	}
}

// When the force-pushed head's own fix pass spends the last of the budget and
// the head is still red, the issue ends Failed/landing-failed (not the generic
// fix-exhausted class) with a comment naming the passes the force-pushed head
// consumed (issue #5095).
func TestSelfHeal_RewaitRed_FixOnForcePushedHeadStillRed_EndsLandingFailed(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	c.MaxFixAttempts = 1
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.SetCheckStates(testPR, []forge.RollupState{
		forge.StateSuccess, forge.StateSuccess, // initial gateToGreen
		forge.StateFailure, // rewait after the rebase force-push: red
		forge.StateFailure, // still red after fix pass 1
	})
	s := newTestSettle(c, fc, fc)
	d := dispatch.NewFake()

	landing, reason := s.selfHeal(d, "1", 0, testPR, nil)

	if landing != landingFailed {
		t.Errorf("selfHeal = %v, want landingFailed", landing)
	}
	if !strings.Contains(reason, errLandingNeverGreen.Error()) {
		t.Errorf("reason = %q, want it to wrap %q", reason, errLandingNeverGreen.Error())
	}
	if len(d.FixCalls) != 1 {
		t.Errorf("FixCalls = %+v, want exactly 1", d.FixCalls)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-failed") || containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-failed only; labels=%v", iss.Labels)
	}
	found := false
	for _, cc := range fc.CommentCalls {
		if strings.Contains(cc.Body, "landing failed") && strings.Contains(cc.Body, "1 of 1 fix pass(es) used, 1 on the force-pushed head") {
			found = true
		}
		if strings.Contains(cc.Body, "after 1 of 1") {
			t.Errorf("comment doubles %q: %q", "after", cc.Body)
		}
	}
	if !found {
		t.Errorf("no landing-failed comment names the force-pushed head's fix passes; comments=%+v", fc.CommentCalls)
	}
	if fc.Merged != "" {
		t.Errorf("a still-red head must not merge; fc.Merged=%q", fc.Merged)
	}
}

// A timeout, conflict-resolve dispatch failure or relay failure is not a red
// head: the landing-failed comment keeps its original wording, with no fix-pass
// count (issue #5095).
func TestSelfHeal_NonRedNeverGreen_CommentHasNoFixCount(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	c.MaxFixAttempts = 2
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrMergeConflict
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)
	d := dispatch.NewFake()
	d.ResolveConflictErr = errors.New("conflict-resolve box exited 1")

	landing, reason := s.selfHeal(d, "1", 0, testPR, nil)

	if landing != landingFailed {
		t.Fatalf("selfHeal = %v, want landingFailed", landing)
	}
	want := "landing failed: " + reason + " — no green PR exists at the current head"
	found := false
	for _, cc := range fc.CommentCalls {
		if cc.Body == want {
			found = true
		}
	}
	if !found {
		t.Errorf("want comment %q; comments=%+v", want, fc.CommentCalls)
	}
}

// A failed relay after a successful conflict-resolve leaves the head
// unresolved, never green: Failed/landing-failed without spending a fix pass
// even though budget remains (issue #5095).
func TestSelfHeal_RelayAfterConflictResolveFailure_EndsFailedWithoutFixPass(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	c.MaxFixAttempts = 2
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrMergeConflict
	fc.RelayBundleErr = errors.New("bundle missing")
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc.AsGithubReadOnly())
	d := dispatch.NewFake()

	landing, reason := s.selfHeal(d, "1", 0, testPR, nil)

	if landing != landingFailed {
		t.Errorf("selfHeal = %v, want landingFailed", landing)
	}
	if !strings.Contains(reason, "relay after conflict-resolve failed") {
		t.Errorf("reason = %q, want the relay failure named", reason)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-failed") || containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-failed only; labels=%v", iss.Labels)
	}
	if len(d.FixCalls) != 0 {
		t.Errorf("a failed relay must not dispatch a fix pass; FixCalls=%+v", d.FixCalls)
	}
}

// A red re-wait after a successful conflict-resolve gets the fix pass too, and
// the fixed head merges (issue #5095).
func TestSelfHeal_RewaitRedAfterConflictResolve_FixPassThenMerges(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	c.MaxFixAttempts = 2
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrMergeConflict
	fc.SetCheckStates(testPR, []forge.RollupState{
		forge.StateSuccess, forge.StateSuccess, // initial gateToGreen
		forge.StateFailure,                     // rewait after conflict-resolve: red
		forge.StateSuccess, forge.StateSuccess, // after the fix pass
	})
	s := newTestSettle(c, fc, fc)
	d := dispatch.NewFake()

	landing, reason := s.selfHeal(d, "1", 0, testPR, nil)

	if landing != landingMerged {
		t.Errorf("selfHeal = %v (%q), want landingMerged", landing, reason)
	}
	if len(d.ResolveConflictCalls) != 1 {
		t.Errorf("ResolveConflict called %d times, want 1", len(d.ResolveConflictCalls))
	}
	if len(d.FixCalls) != 1 || d.FixCalls[0].Pass != 1 {
		t.Errorf("want exactly fix pass 1; FixCalls=%+v", d.FixCalls)
	}
	if fc.Merged != testPR {
		t.Errorf("fixed head must be merged; fc.Merged=%q", fc.Merged)
	}
}

// rewaitAfterForcePush's gateResult-to-error mapping names gateTerminal and
// gateRedRetry explicitly rather than folding them into a catch-all default
// (issue #1175).
func TestRewaitGateResultErr_ExplicitCases(t *testing.T) {
	cases := []struct {
		name    string
		g       gateResult
		wantErr error
		wantNil bool
		notRed  bool // must not wrap errForcePushRed: only genuine red earns a fix pass
	}{
		{name: "green", g: gateGreen, wantNil: true},
		{name: "abandoned", g: gateAbandoned, wantErr: errAbandoned},
		{name: "terminal", g: gateTerminal, wantErr: errLandingNeverGreen, notRed: true},
		{name: "red-retry", g: gateRedRetry, wantErr: errLandingNeverGreen},
		{name: "red-retry marks the red cause", g: gateRedRetry, wantErr: errForcePushRed},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := rewaitGateResultErr(tc.g, "", testPR)
			if tc.wantNil {
				if err != nil {
					t.Errorf("rewaitGateResultErr(%v) = %v, want nil", tc.g, err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("rewaitGateResultErr(%v) = %v, want wrapping %v", tc.g, err, tc.wantErr)
			}
			if tc.notRed && errors.Is(err, errForcePushRed) {
				t.Errorf("rewaitGateResultErr(%v) = %v, must not wrap errForcePushRed", tc.g, err)
			}
		})
	}
}

// A gateResult outside the four known variants panics rather than silently
// mapping to errLandingNeverGreen, so a new variant must be handled explicitly
// (issue #1175).
func TestRewaitGateResultErr_UnhandledVariantPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("rewaitGateResultErr(unknown variant) did not panic")
		}
	}()
	rewaitGateResultErr(gateResult(99), "", testPR)
}

// A rebase conflict that exhausts MaxRebaseAttempts before ever force-pushing
// leaves the issue agent-complete: the pre-rebase head is still the last green
// PR, so this is the unresolvable-conflict merge failure ADR 0012 covers. It
// differs from issue #758, where a force-push did happen and the resulting head
// never re-confirmed green.
func TestSelfHeal_UnresolvableConflictNoForcePush_KeepsComplete(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 0
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)

	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual (unresolvable conflict, no force-push attempted)", landing)
	}
	if len(fc.RebasedURLs) != 0 {
		t.Errorf("Rebase must not be called once MaxRebaseAttempts is exhausted; calls=%d", len(fc.RebasedURLs))
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed; labels=%v", iss.Labels)
	}
}

// labelSnapshotDispatcher wraps a dispatch.Fake so its ResolveConflict call
// snapshots the issue's labels before delegating, capturing what the label
// looks like mid-landing-path (issue #757). terminate_test.go's
// terminatingDispatcher uses the same wrapper pattern.
type labelSnapshotDispatcher struct {
	*dispatch.Fake
	fc  *forge.Fake
	num string

	snapshot []string
}

func (d *labelSnapshotDispatcher) ResolveConflict(pr string) error {
	iss, _ := d.fc.Issue(d.num)
	d.snapshot = append([]string(nil), iss.Labels...)
	return d.Fake.ResolveConflict(pr)
}

// The InProgress to Complete swap is held until the landing path settles
// (issue #757): mid-conflict-resolve the issue must still carry
// agent-in-progress, because the label must not claim the agent has nothing
// left to do while a conflict-resolve box runs. Only after the retried merge
// succeeds does the issue swap to agent-complete, exactly once.
func TestSelfHeal_LabelStaysInProgressThroughConflictResolve(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.RebaseErr = forge.ErrMergeConflict
	// The fixture holds two states for the initial gateToGreen confirm and two
	// more for the post-force-push rewait's own confirm.
	fc.SetCheckStates(testPR, []forge.RollupState{
		forge.StateSuccess, forge.StateSuccess,
		forge.StateSuccess, forge.StateSuccess,
	})
	s := newTestSettle(c, fc, fc)
	d := &labelSnapshotDispatcher{Fake: dispatch.NewFake(), fc: fc, num: "1"}

	landing, _ := s.selfHeal(d, "1", 0, testPR, nil)

	if landing != landingMerged {
		t.Fatalf("selfHeal = %v, want landingMerged", landing)
	}
	if len(d.ResolveConflictCalls) != 1 {
		t.Fatalf("expected ResolveConflict to be called once, got %d", len(d.ResolveConflictCalls))
	}
	if !containsLabel(d.snapshot, "agent-in-progress") {
		t.Errorf("issue must still carry agent-in-progress during conflict-resolve; snapshot=%v", d.snapshot)
	}
	if containsLabel(d.snapshot, "agent-complete") {
		t.Errorf("issue must NOT carry agent-complete during conflict-resolve; snapshot=%v", d.snapshot)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after the landing path settles; labels=%v", iss.Labels)
	}
	completeSwaps := 0
	for _, call := range fc.TransitionStateCalls {
		if call.To == forge.Complete {
			completeSwaps++
		}
	}
	if completeSwaps != 1 {
		t.Errorf("expected exactly one InProgress->Complete swap, got %d: %+v", completeSwaps, fc.TransitionStateCalls)
	}
}

// The launcher flips a green PR out of draft with MarkReady after the merge
// guard passes and before applyMergeMode's immediate-mode Merge call (issue
// #1651). The flip is unconditional and idempotent, so this only asserts the
// call happened and preceded Merge.
func TestSelfHeal_MarksReadyBeforeMerge(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)

	if landing != landingMerged {
		t.Fatalf("selfHeal = %v, want landingMerged", landing)
	}
	if fc.Merged != testPR {
		t.Fatalf("expected Merge(%q); fc.Merged=%q", testPR, fc.Merged)
	}
	wantLog := []string{"MarkReady:" + testPR, "Merge:" + testPR}
	if !slices.Equal(fc.LandingCallLog, wantLog) {
		t.Fatalf("call order = %v, want %v (MarkReady must precede Merge)", fc.LandingCallLog, wantLog)
	}
}

// The same MarkReady ordering guarantee (issue #1651) holds under
// MERGE_MODE=auto: the flip precedes EnqueueAutoMerge.
func TestSelfHeal_MarksReadyBeforeEnqueueAutoMerge(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "auto"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)

	if landing != landingManual {
		t.Fatalf("selfHeal = %v, want landingManual (auto mode)", landing)
	}
	if len(fc.EnqueueAutoMergeCalls) != 1 || fc.EnqueueAutoMergeCalls[0] != testPR {
		t.Fatalf("expected EnqueueAutoMerge(%q); calls=%v", testPR, fc.EnqueueAutoMergeCalls)
	}
	wantLog := []string{"MarkReady:" + testPR, "EnqueueAutoMerge:" + testPR}
	if !slices.Equal(fc.LandingCallLog, wantLog) {
		t.Fatalf("call order = %v, want %v (MarkReady must precede EnqueueAutoMerge)", fc.LandingCallLog, wantLog)
	}
}

// The same MarkReady ordering guarantee (issue #1651) holds under
// MERGE_MODE=manual: a green draft PR is still flipped ready even though
// manual mode otherwise leaves the PR untouched.
func TestSelfHeal_MarksReadyBeforeManualHandoff(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "manual"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)

	if landing != landingManual {
		t.Fatalf("selfHeal = %v, want landingManual", landing)
	}
	if len(fc.MarkReadyCalls) != 1 || fc.MarkReadyCalls[0] != testPR {
		t.Fatalf("expected MarkReady(%q) exactly once; calls=%v", testPR, fc.MarkReadyCalls)
	}
	if fc.Merged != "" {
		t.Errorf("manual mode must not call Merge; fc.Merged=%q", fc.Merged)
	}
}

// A merge-guard hit still flips the PR out of draft before downgrading to
// manual. Without that unconditional MarkReady-at-green (issue #1651) the PR
// is stranded as a draft and no human can see or merge it despite the manual
// hand-off comment.
func TestSelfHeal_MergeGuardHit_FlipsReadyBeforeHandoff(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MergeGuardPaths = ".github/**"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetPRFiles(testPR, []string{".github/workflows/ci.yml"})
	s := newTestSettle(c, fc, fc)

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)

	if landing != landingManual {
		t.Fatalf("selfHeal = %v, want landingManual (guard hit)", landing)
	}
	if len(fc.MarkReadyCalls) != 1 {
		t.Errorf("guard hit must still call MarkReady exactly once; calls=%v", fc.MarkReadyCalls)
	}
}

// A MarkReady error is logged but never blocks the landing. The flip is
// best-effort, like EnqueueAutoMerge's own failure handling: a still-green PR
// merges even when the ready-flip failed.
func TestSelfHeal_MarkReadyFailureDoesNotBlockMerge(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MarkReadyErr = errors.New("HTTP 403: Resource not accessible by integration")
	s := newTestSettle(c, fc, fc)

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)

	if landing != landingMerged {
		t.Fatalf("selfHeal = %v, want landingMerged despite MarkReady failure", landing)
	}
	if fc.Merged != testPR {
		t.Errorf("expected Merge(%q) despite MarkReady failure; fc.Merged=%q", testPR, fc.Merged)
	}
}

// For a push-only Code Forge there is no CI or PR to watch, since the Box
// already pushed the branch. selfHeal skips the CI-wait/merge-gate, marks the
// issue Complete immediately, then applies MERGE_MODE against the push-only
// Merge.
func TestSelfHeal_GitForge_PushOnlyLanding(t *testing.T) {
	cases := []struct {
		name        string
		mergeMode   string
		wantLanding landingResult
	}{
		{name: "manual leaves the branch as pushed", mergeMode: "manual", wantLanding: landingManual},
		{name: "immediate pushes to the target branch", mergeMode: "immediate", wantLanding: landingMerged},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.MergeMode = tc.mergeMode
			fc := forge.NewFake(testDispatchLabels)
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
			branch := "agent/issue-1"
			s := newTestSettle(c, fc, fc.AsPushOnly())

			landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, branch, nil)

			if landing != tc.wantLanding {
				t.Errorf("selfHeal = %v, want %v", landing, tc.wantLanding)
			}
			wantMerged := tc.wantLanding == landingMerged
			if wantMerged && fc.Merged != branch {
				t.Errorf("expected Merge(%q); fc.Merged=%q", branch, fc.Merged)
			}
			if !wantMerged && fc.Merged != "" {
				t.Errorf("Merge must not be called; fc.Merged=%q", fc.Merged)
			}
			iss, _ := fc.Issue("1")
			if !containsLabel(iss.Labels, "agent-complete") {
				t.Errorf("issue must carry agent-complete; labels=%v", iss.Labels)
			}
		})
	}
}

// For a push-only Code Forge, a push failure under MERGE_MODE=immediate leaves
// the issue at agent-complete with a comment, never demoted to agent-failed,
// matching the github adapter's post-green merge-blocked contract (ADR 0012).
func TestSelfHeal_GitForge_PushFailureStaysCompleteNotFailed(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErr = errors.New("remote rejected: non-fast-forward")
	branch := "agent/issue-1"
	s := newTestSettle(c, fc, fc.AsPushOnly())

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, branch, nil)

	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual when the push fails", landing)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after a push failure; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed after a push failure; labels=%v", iss.Labels)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("expected exactly one merge-blocked comment, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
}

// assertLatchedLeftOpen ties landingManual's reasons to ReasonLeavesPROpen: a green PR
// handed off unmerged must latch a reason LateMerges will watch.
func assertLatchedLeftOpen(t *testing.T, s *Settle, num string) {
	t.Helper()
	s.settledMu.Lock()
	defer s.settledMu.Unlock()
	if r := s.settledLatch[num].reason; !ReasonLeavesPROpen(r) {
		t.Errorf("latched reason %q for a landingManual settle is not ReasonLeavesPROpen", r)
	}
}

// tuningTestSettle builds a green-PR Settle whose settled issue carries
// labels and body, on a tracker with or without a label registry.
func tuningTestSettle(mode string, labelRegistry bool, labels []string, body string) (*Settle, *forge.Fake) {
	c := baseConfig()
	c.MergeMode = mode
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: labels, Body: body})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetPRFiles(testPR, []string{"src/main.go"})
	s := newTestSettle(c, fc, fc)
	s.cfg.Capabilities.TrackerDescriptor.LabelRegistry = labelRegistry
	return s, fc
}

const tuningBody = "finding text\n<!-- spindrift-dedup: foo, chore=tuning -->"

func assertTuningHeld(t *testing.T, s *Settle, fc *forge.Fake, landing landingResult) {
	t.Helper()
	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual (tuning provenance hold)", landing)
	}
	s.settledMu.Lock()
	reason := s.settledLatch["1"].reason
	s.settledMu.Unlock()
	if reason != ReasonMergeByHand {
		t.Errorf("latched reason = %q, want %q", reason, ReasonMergeByHand)
	}
	assertLatchedLeftOpen(t, s, "1")
	if fc.Merged != "" {
		t.Errorf("a held PR must not merge; fc.Merged=%q", fc.Merged)
	}
	if len(fc.EnqueueAutoMergeCalls) != 0 {
		t.Errorf("a held PR must not enqueue auto-merge; calls=%v", fc.EnqueueAutoMergeCalls)
	}
	if len(fc.MarkReadyCalls) == 0 {
		t.Errorf("a held PR must still be flipped out of draft")
	}
	assertTuningPRComment(t, fc, "hold comment")
	body := fc.CommentPRCalls[0].Body
	for _, want := range []string{"agent-tuning-finding", "Removing"} {
		if !strings.Contains(body, want) {
			t.Errorf("hold comment must mention %q; body=%q", want, body)
		}
	}
}

// assertTuningPRComment pins that the one tuning comment landed on the PR,
// where a reviewer looking at the held PR will see it, and not on the issue.
func assertTuningPRComment(t *testing.T, fc *forge.Fake, what string) {
	t.Helper()
	if len(fc.CommentPRCalls) != 1 {
		t.Fatalf("expected exactly one %s on the PR, got %d: %+v", what, len(fc.CommentPRCalls), fc.CommentPRCalls)
	}
	if fc.CommentPRCalls[0].PR != testPR {
		t.Errorf("%s PR = %q, want %q", what, fc.CommentPRCalls[0].PR, testPR)
	}
	if len(fc.CommentCalls) != 0 {
		t.Errorf("%s must not be posted on the issue; got %+v", what, fc.CommentCalls)
	}
}

// A PR closing an issue wearing agent-tuning-finding is never merged by the
// host, whatever MERGE_MODE says and whichever paths it touches.
func TestSelfHeal_TuningLabel_HoldsImmediateMerge(t *testing.T) {
	s, fc := tuningTestSettle("immediate", true, []string{"agent-in-progress", "agent-tuning-finding"}, "")
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	assertTuningHeld(t, s, fc, landing)
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") || containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("held issue must be agent-complete, not agent-failed; labels=%v", iss.Labels)
	}
}

func TestSelfHeal_TuningLabel_HoldsAutoMerge(t *testing.T) {
	s, fc := tuningTestSettle("auto", true, []string{"agent-in-progress", "agent-tuning-finding"}, "")
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	assertTuningHeld(t, s, fc, landing)
}

// A tracker with no label registry cannot carry the label, so the body's
// chore=tuning marker is the provenance.
func TestSelfHeal_TuningMarker_HoldsOnLabelessTracker(t *testing.T) {
	s, fc := tuningTestSettle("immediate", false, []string{"agent-in-progress"}, tuningBody)
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	assertTuningHeld(t, s, fc, landing)
}

// A label-less tracker that still reports the label (jira applies labels
// without registering them) holds on the label alone, marker or not.
func TestSelfHeal_TuningLabel_HoldsOnLabelessTracker(t *testing.T) {
	s, fc := tuningTestSettle("immediate", false, []string{"agent-in-progress", "agent-tuning-finding"}, "")
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	assertTuningHeld(t, s, fc, landing)
}

// Removing the label is the override: with a label registry the body marker
// alone must not hold, so the PR merges normally.
func TestSelfHeal_TuningLabelRemoved_MarkerAloneDoesNotHold(t *testing.T) {
	s, fc := tuningTestSettle("immediate", true, []string{"agent-in-progress"}, tuningBody)
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	if landing != landingMerged {
		t.Errorf("selfHeal = %v, want landingMerged after the label was removed", landing)
	}
	if fc.Merged != testPR {
		t.Errorf("expected Merge to be called; fc.Merged=%q", fc.Merged)
	}
	if len(fc.CommentPRCalls) != 0 {
		t.Errorf("no hold comment expected; got %+v", fc.CommentPRCalls)
	}
}

func TestSelfHeal_NoTuningProvenance_LabelessTrackerMergesNormally(t *testing.T) {
	s, fc := tuningTestSettle("immediate", false, []string{"agent-in-progress"}, "plain body\n<!-- spindrift-dedup: foo, chore=docs -->")
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	if landing != landingMerged {
		t.Errorf("selfHeal = %v, want landingMerged", landing)
	}
	if fc.Merged != testPR {
		t.Errorf("expected Merge to be called; fc.Merged=%q", fc.Merged)
	}
}

// An unreadable issue fails safe like an unreadable file list: held, with a
// precautionary comment, still agent-complete.
func TestSelfHeal_TuningCheckError_FailsSafe(t *testing.T) {
	s, fc := tuningTestSettle("immediate", true, []string{"agent-in-progress"}, "")
	fc.IssueErr = errors.New("gh issue view: 502")
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual (issue unreadable)", landing)
	}
	assertLatchedLeftOpen(t, s, "1")
	if fc.Merged != "" {
		t.Errorf("an unreadable issue must prevent Merge; fc.Merged=%q", fc.Merged)
	}
	fc.IssueErr = nil
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") || containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must be agent-complete, not agent-failed; labels=%v", iss.Labels)
	}
	assertTuningPRComment(t, fc, "precautionary comment")
	s.settledMu.Lock()
	reason := s.settledLatch["1"].reason
	s.settledMu.Unlock()
	if reason != ReasonMergeGuardCheckError {
		t.Errorf("latched reason = %q, want %q", reason, ReasonMergeGuardCheckError)
	}
}

// MERGE_MODE=manual would already leave the PR to a human, but the latched
// reason must still name tuning provenance, not plain manual.
func TestSelfHeal_TuningLabel_HoldsManualMerge(t *testing.T) {
	s, fc := tuningTestSettle("manual", true, []string{"agent-in-progress", "agent-tuning-finding"}, "")
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	assertTuningHeld(t, s, fc, landing)
}

// A configured merge guard the PR's files miss must not pre-empt the tuning
// hold: the guard check passes and the provenance check still runs after it.
func TestSelfHeal_TuningLabel_HoldsWithMergeGuardMiss(t *testing.T) {
	s, fc := tuningTestSettle("immediate", true, []string{"agent-in-progress", "agent-tuning-finding"}, "")
	s.cfg.MergeGuardPaths = ".github/**"
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	assertTuningHeld(t, s, fc, landing)
}

// A failed PR comment must not lift the hold.
func TestSelfHeal_TuningHold_CommentPRErrorStillHolds(t *testing.T) {
	s, fc := tuningTestSettle("immediate", true, []string{"agent-in-progress", "agent-tuning-finding"}, "")
	fc.CommentPRErr = errors.New("gh pr comment: 502")
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual", landing)
	}
	if fc.Merged != "" {
		t.Errorf("a held PR must not merge; fc.Merged=%q", fc.Merged)
	}
}

// Every MERGE_MODE holds a tuning PR, by label on a registry tracker and by
// the chore=tuning marker on a tracker without one: the issue ends
// agent-complete and the PR is flipped ready, never merged.
func TestSelfHeal_TuningHeldUnderEveryMergeMode(t *testing.T) {
	cases := []struct {
		name     string
		registry bool
		labels   []string
		body     string
	}{
		{"label on registry tracker", true, []string{"agent-in-progress", "agent-tuning-finding"}, ""},
		{"marker on registry-less tracker", false, []string{"agent-in-progress"}, tuningBody},
	}
	for _, mode := range []string{"immediate", "auto", "manual"} {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				s, fc := tuningTestSettle(mode, tc.registry, tc.labels, tc.body)
				landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
				assertTuningHeld(t, s, fc, landing)
				iss, _ := fc.Issue("1")
				if !containsLabel(iss.Labels, "agent-complete") || containsLabel(iss.Labels, "agent-failed") {
					t.Errorf("held issue must be agent-complete, not agent-failed; labels=%v", iss.Labels)
				}
			})
		}
	}
}

// The hold and precaution comments for tuning are pinned byte for byte: the
// wording is documented and reviewers key off it.
func TestTuningHoldComments_ExactWording(t *testing.T) {
	tuning := chore.Row{Name: "tuning", FindingLabel: "agent-tuning-finding", MergeByHand: true}
	wantHold := "merge held for tuning provenance: this PR closes a tuning finding (agent-tuning-finding) — not merging regardless of MERGE_MODE; review and merge by hand. Removing the agent-tuning-finding label from the issue is the override and lets a PR merge normally"
	if got := holdComment(tuning); got != wantHold {
		t.Errorf("holdComment = %q, want %q", got, wantHold)
	}
	wantErr := "tuning provenance: could not read the closing issue (boom) — downgrading to manual as a precaution; review and merge by hand"
	if got := holdCheckErrorComment([]chore.Row{tuning}, errors.New("boom")); got != wantErr {
		t.Errorf("holdCheckErrorComment = %q, want %q", got, wantErr)
	}
}

// withCatalogRows swaps the catalog settle consults for the test's duration.
func withCatalogRows(t *testing.T, rows ...chore.Row) {
	t.Helper()
	orig := catalogRows
	catalogRows = func() []chore.Row { return rows }
	t.Cleanup(func() { catalogRows = orig })
}

var (
	syntheticHold = chore.Row{Name: "synthetic", FindingLabel: "agent-synthetic-finding", MergeByHand: true}
	syntheticFree = chore.Row{Name: "freebie", FindingLabel: "agent-freebie-finding"}
)

// The hold is a Chore row fact, not a tuning special case: any row marked
// MergeByHand holds the PR closing its finding, naming that row's label.
func TestSelfHeal_SyntheticMergeByHandRow_HoldsByLabel(t *testing.T) {
	withCatalogRows(t, syntheticHold, syntheticFree)
	s, fc := tuningTestSettle("immediate", true, []string{"agent-in-progress", "agent-synthetic-finding"}, "")
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	if landing != landingManual {
		t.Fatalf("selfHeal = %v, want landingManual", landing)
	}
	s.settledMu.Lock()
	reason := s.settledLatch["1"].reason
	s.settledMu.Unlock()
	if reason != ReasonMergeByHand {
		t.Errorf("latched reason = %q, want %q", reason, ReasonMergeByHand)
	}
	if fc.Merged != "" {
		t.Errorf("a held PR must not merge; fc.Merged=%q", fc.Merged)
	}
	assertTuningPRComment(t, fc, "hold comment")
	if body := fc.CommentPRCalls[0].Body; !strings.Contains(body, "agent-synthetic-finding") {
		t.Errorf("hold comment must name the row's label; body=%q", body)
	}
}

func TestSelfHeal_SyntheticMergeByHandRow_HoldsByMarkerOnLabelessTracker(t *testing.T) {
	withCatalogRows(t, syntheticHold)
	body := "finding\n<!-- spindrift-dedup: foo, chore=synthetic -->"
	s, fc := tuningTestSettle("immediate", false, []string{"agent-in-progress"}, body)
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	if landing != landingManual || fc.Merged != "" {
		t.Errorf("selfHeal = %v, Merged=%q; want landingManual and no merge", landing, fc.Merged)
	}
	// The tuning marker is no longer special once tuning is off the catalog.
	s, fc = tuningTestSettle("immediate", false, []string{"agent-in-progress"}, tuningBody)
	if landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil); landing != landingMerged || fc.Merged != testPR {
		t.Errorf("selfHeal = %v, Merged=%q; want landingMerged for a marker no MergeByHand row owns", landing, fc.Merged)
	}
}

// A Chore whose finding label does not set MergeByHand merges normally, and
// with no MergeByHand row at all the closing issue is never even read.
func TestSelfHeal_NonMergeByHandRow_DoesNotHold(t *testing.T) {
	withCatalogRows(t, syntheticFree)
	s, fc := tuningTestSettle("immediate", true, []string{"agent-in-progress", "agent-freebie-finding"}, "")
	fc.IssueErr = errors.New("gh issue view: 502")
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)
	if landing != landingMerged || fc.Merged != testPR {
		t.Errorf("selfHeal = %v, Merged=%q; want landingMerged", landing, fc.Merged)
	}
	if len(fc.CommentPRCalls) != 0 {
		t.Errorf("no hold comment expected; got %+v", fc.CommentPRCalls)
	}
}

// Each force-pushed head that goes red gets its own count in the failure
// comment: a first head fixed to green and merged-retried must not leak its
// pass into the second head's "K on the force-pushed head" (issue #5095).
func TestSelfHeal_RewaitRed_SecondForcePushEpisodeCountsOnlyItsOwnPasses(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	c.MaxFixAttempts = 2
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict, forge.ErrMergeConflict}
	fc.SetCheckStates(testPR, []forge.RollupState{
		forge.StateSuccess, forge.StateSuccess, // initial gateToGreen
		forge.StateFailure,                     // rewait after the first force-push: red
		forge.StateSuccess, forge.StateSuccess, // fix pass 1 turns it green
		forge.StateFailure, // rewait after the second force-push: red
		forge.StateFailure, // still red after fix pass 2
	})
	s := newTestSettle(c, fc, fc)
	d := dispatch.NewFake()

	landing, reason := s.selfHeal(d, "1", 0, testPR, nil)

	if landing != landingFailed {
		t.Fatalf("selfHeal = %v (%q), want landingFailed", landing, reason)
	}
	if len(d.FixCalls) != 2 {
		t.Errorf("FixCalls = %+v, want 2", d.FixCalls)
	}
	want := "2 of 2 fix pass(es) used, 1 on the force-pushed head"
	found := false
	for _, cc := range fc.CommentCalls {
		if strings.Contains(cc.Body, want) {
			found = true
		}
	}
	if !found {
		t.Errorf("no comment names %q; comments=%+v", want, fc.CommentCalls)
	}
}

// The proactive stale-base rebase's re-wait is a force-push source too: a red
// result there takes the shared-budget fix pass, and the fixed head merges
// (issue #5095).
func TestSelfHeal_StaleBasePreflightRed_DispatchesFixPassThenMerges(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	c.MaxFixAttempts = 2
	c.PreflightStaleBase = true
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetNeedsUpdate(testPR, true)
	fc.SetCheckStates(testPR, []forge.RollupState{
		forge.StateSuccess, forge.StateSuccess, // initial gateToGreen
		forge.StateFailure,                     // rewait after the preflight rebase: red
		forge.StateSuccess, forge.StateSuccess, // after the fix pass
		forge.StateSuccess, forge.StateSuccess, // rewait after the next preflight rebase
	})
	s := newTestSettle(c, fc, fc)
	d := dispatch.NewFake()

	landing, reason := s.selfHeal(d, "1", 0, testPR, nil)

	if landing != landingMerged {
		t.Fatalf("selfHeal = %v (%q), want landingMerged", landing, reason)
	}
	if len(d.FixCalls) != 1 || d.FixCalls[0].Pass != 1 {
		t.Errorf("want exactly fix pass 1; FixCalls=%+v", d.FixCalls)
	}
	if fc.Merged != testPR {
		t.Errorf("fixed head must be merged; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") || containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-complete only; labels=%v", iss.Labels)
	}
}

// With several merge-by-hand rows the unreadable issue's Chore is unknown, so
// the precaution comment names them all.
func TestHoldCheckErrorComment_JoinsEveryRow(t *testing.T) {
	other := chore.Row{Name: "other", FindingLabel: "agent-other-finding", MergeByHand: true}
	want := "synthetic/other provenance: could not read the closing issue (boom) — downgrading to manual as a precaution; review and merge by hand"
	if got := holdCheckErrorComment([]chore.Row{syntheticHold, other}, errors.New("boom")); got != want {
		t.Errorf("holdCheckErrorComment = %q, want %q", got, want)
	}
}
