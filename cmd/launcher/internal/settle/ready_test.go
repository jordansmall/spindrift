package settle

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

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

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
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

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
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

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
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

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
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

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
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

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
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

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)

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
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrMergeConflict
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)
	d := dispatch.NewFake()
	d.ResolveConflictErr = errors.New("conflict-resolve box exited 1")

	landing, reason := s.selfHeal(d, "1", 0, testPR)

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

	landing, reason := s.selfHeal(d, "1", 0, testPR)

	if landing != landingAbandoned {
		t.Errorf("selfHeal = %v, want landingAbandoned", landing)
	}
	if reason != "" {
		t.Errorf("selfHeal reason = %q, want empty", reason)
	}
	assertNoTransitionOrComment(t, fc)
	assertClaimUntouched(t, fc)
}

// When the post-force-push re-wait ends in genuine red CI or a timeout, the
// issue ends agent-failed: the force-pushed head never produced a green PR
// (issue #758).
func TestSelfHeal_RewaitAfterForcePush_NeverGreen_EndsFailed(t *testing.T) {
	cases := []struct {
		name        string
		rebaseErr   error
		resolveErr  error
		pollTimeout int
		checkStates []forge.RollupState
	}{
		{
			name: "rewait after a plain rebase ends genuine red",
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
			checkStates: []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := baseConfig()
			c.MergeMode = "immediate"
			c.MaxRebaseAttempts = 3
			c.MergePollTimeout = tc.pollTimeout
			fc := forge.NewFake(testDispatchLabels)
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
			fc.MergeErrs = []error{forge.ErrMergeConflict}
			fc.RebaseErr = tc.rebaseErr
			fc.SetCheckStates(testPR, tc.checkStates)
			s := newTestSettle(c, fc, fc)
			d := dispatch.NewFake()
			d.ResolveConflictErr = tc.resolveErr

			landing, reason := s.selfHeal(d, "1", 0, testPR)

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
		})
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
	}{
		{name: "green", g: gateGreen, wantNil: true},
		{name: "abandoned", g: gateAbandoned, wantErr: errAbandoned},
		{name: "terminal", g: gateTerminal, wantErr: errLandingNeverGreen},
		{name: "red-retry", g: gateRedRetry, wantErr: errLandingNeverGreen},
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

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)

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

	landing, _ := s.selfHeal(d, "1", 0, testPR)

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

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)

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

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)

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

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)

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

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)

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

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)

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

			landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, branch)

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

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, branch)

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
	if reason != ReasonTuningProvenance {
		t.Errorf("latched reason = %q, want %q", reason, ReasonTuningProvenance)
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
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
	assertTuningHeld(t, s, fc, landing)
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") || containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("held issue must be agent-complete, not agent-failed; labels=%v", iss.Labels)
	}
}

func TestSelfHeal_TuningLabel_HoldsAutoMerge(t *testing.T) {
	s, fc := tuningTestSettle("auto", true, []string{"agent-in-progress", "agent-tuning-finding"}, "")
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
	assertTuningHeld(t, s, fc, landing)
}

// A tracker with no label registry cannot carry the label, so the body's
// chore=tuning marker is the provenance.
func TestSelfHeal_TuningMarker_HoldsOnLabelessTracker(t *testing.T) {
	s, fc := tuningTestSettle("immediate", false, []string{"agent-in-progress"}, tuningBody)
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
	assertTuningHeld(t, s, fc, landing)
}

// A label-less tracker that still reports the label (jira applies labels
// without registering them) holds on the label alone, marker or not.
func TestSelfHeal_TuningLabel_HoldsOnLabelessTracker(t *testing.T) {
	s, fc := tuningTestSettle("immediate", false, []string{"agent-in-progress", "agent-tuning-finding"}, "")
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
	assertTuningHeld(t, s, fc, landing)
}

// Removing the label is the override: with a label registry the body marker
// alone must not hold, so the PR merges normally.
func TestSelfHeal_TuningLabelRemoved_MarkerAloneDoesNotHold(t *testing.T) {
	s, fc := tuningTestSettle("immediate", true, []string{"agent-in-progress"}, tuningBody)
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
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
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
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
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
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
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
	assertTuningHeld(t, s, fc, landing)
}

// A configured merge guard the PR's files miss must not pre-empt the tuning
// hold: the guard check passes and the provenance check still runs after it.
func TestSelfHeal_TuningLabel_HoldsWithMergeGuardMiss(t *testing.T) {
	s, fc := tuningTestSettle("immediate", true, []string{"agent-in-progress", "agent-tuning-finding"}, "")
	s.cfg.MergeGuardPaths = ".github/**"
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
	assertTuningHeld(t, s, fc, landing)
}

// A failed PR comment must not lift the hold.
func TestSelfHeal_TuningHold_CommentPRErrorStillHolds(t *testing.T) {
	s, fc := tuningTestSettle("immediate", true, []string{"agent-in-progress", "agent-tuning-finding"}, "")
	fc.CommentPRErr = errors.New("gh pr comment: 502")
	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual", landing)
	}
	if fc.Merged != "" {
		t.Errorf("a held PR must not merge; fc.Merged=%q", fc.Merged)
	}
}
