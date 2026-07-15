package settle

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// TestSelfHeal_MergeFailureAfterGreenKeepsComplete verifies that a merge
// failure after CI reaches green leaves the issue at agent-complete (not
// agent-failed) and returns (ok=true, merged=false).
func TestSelfHeal_MergeFailureAfterGreenKeepsComplete(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 0
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	// CI is green but merge fails.
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErr = errors.New("required review missing")
	s := New(c, fc, fc)

	landing := s.selfHeal(dispatch.NewFake(), "1", testPR)
	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual (CI green, merge failed)", landing)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after green+merge-failure; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed after merge failure on green PR; labels=%v", iss.Labels)
	}
}

// TestSelfHeal_MergeGuardHit_DowngradesToManual verifies that a PR touching a
// guarded path is never merged — regardless of MERGE_MODE — and instead posts
// a comment naming the matched path(s) and the knob, leaving the issue at
// agent-complete exactly like a manual-mode green PR.
func TestSelfHeal_MergeGuardHit_DowngradesToManual(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MergeGuardPaths = ".github/**,**/CLAUDE.md"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetPRFiles(testPR, []string{"src/main.go", ".github/workflows/ci.yml"})
	s := New(c, fc, fc)

	landing := s.selfHeal(dispatch.NewFake(), "1", testPR)
	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual (merge guard hit)", landing)
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
	if !strings.Contains(body, ".github/workflows/ci.yml") {
		t.Errorf("comment must name the matched path; body=%q", body)
	}
	if !strings.Contains(body, "MERGE_GUARD_PATHS") {
		t.Errorf("comment must name the knob that triggered it; body=%q", body)
	}
}

// TestSelfHeal_MergeGuardHit_AutoMode verifies the guard fires under
// MERGE_MODE=auto too — the acceptance criterion covers both immediate and
// auto, since the guard downgrades regardless of mode.
func TestSelfHeal_MergeGuardHit_AutoMode(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "auto"
	c.MergeGuardPaths = ".github/**"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetPRFiles(testPR, []string{".github/workflows/ci.yml"})
	s := New(c, fc, fc)

	landing := s.selfHeal(dispatch.NewFake(), "1", testPR)
	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual for a guard-hit auto-mode PR", landing)
	}
	if len(fc.EnqueueAutoMergeCalls) != 0 {
		t.Errorf("guard hit must prevent EnqueueAutoMerge; calls=%v", fc.EnqueueAutoMergeCalls)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("expected exactly one guard comment, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
}

// TestSelfHeal_MergeGuardMiss_MergesNormally verifies that a green PR
// touching no guarded path proceeds exactly as it would with no guard set.
func TestSelfHeal_MergeGuardMiss_MergesNormally(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MergeGuardPaths = ".github/**,**/CLAUDE.md"
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetPRFiles(testPR, []string{"src/main.go"})
	s := New(c, fc, fc)

	landing := s.selfHeal(dispatch.NewFake(), "1", testPR)
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

// TestSelfHeal_MergeGuardCheckError_FailsSafe verifies that when the changed-
// file list cannot be read at all, selfHeal fails safe: no merge, a
// precautionary comment, and the issue stays at agent-complete (not
// agent-failed) rather than silently falling through to MERGE_MODE.
func TestSelfHeal_MergeGuardCheckError_FailsSafe(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MergeGuardPaths = ".github/**"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.PRFilesErr = errors.New("gh api pulls files: 403 Forbidden")
	s := New(c, fc, fc)

	landing := s.selfHeal(dispatch.NewFake(), "1", testPR)
	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual (guard check errored)", landing)
	}
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

// TestSelfHeal_ConflictResolveFailure_EndsFailed verifies that a failed
// conflict-resolve dispatch (the box exits non-zero, leaving the rebase
// conflict unresolved) ends the issue at agent-failed, not agent-complete
// (issue #758): the head is in an unresolved-conflict state, never green.
func TestSelfHeal_ConflictResolveFailure_EndsFailed(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrMergeConflict
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)
	d := dispatch.NewFake()
	d.ResolveConflictErr = errors.New("conflict-resolve box exited 1")

	landing := s.selfHeal(d, "1", testPR)

	if landing != landingFailed {
		t.Errorf("selfHeal = %v, want landingFailed (conflict-resolve dispatch failed)", landing)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must carry agent-failed after a failed conflict-resolve dispatch; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must NOT carry agent-complete after a failed conflict-resolve dispatch; labels=%v", iss.Labels)
	}
}

// TestSelfHeal_RewaitAfterForcePush_NeverGreen_EndsFailed verifies that when
// the post-force-push re-wait (after a plain rebase, or after an
// agent-resolved conflict) ends in genuine red CI or a timeout, the issue
// ends agent-failed, not agent-complete (issue #758): the force-pushed head
// never produced a green PR.
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
			// Rebase succeeds outright (no conflict), so mergeImmediate goes
			// straight to the post-force-push re-wait without a conflict-resolve.
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
			// conflict-resolve, which succeeds; the following re-wait then
			// times out with no checks ever registering.
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
			s := New(c, fc, fc)
			d := dispatch.NewFake()
			d.ResolveConflictErr = tc.resolveErr

			landing := s.selfHeal(d, "1", testPR)

			if landing != landingFailed {
				t.Errorf("selfHeal = %v, want landingFailed (force-pushed head never went green)", landing)
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

// TestSelfHeal_UnresolvableConflictNoForcePush_KeepsComplete verifies that a
// rebase conflict which exhausts MaxRebaseAttempts *before ever force-pushing*
// leaves the issue agent-complete, not agent-failed: the pre-rebase head is
// still the last green PR, so this is the "unresolvable conflict" merge
// failure ADR 0012 already covers — distinct from issue #758's force-pushed-
// head-never-went-green case, where a force-push (rebase or conflict-resolve)
// did happen and the resulting head never re-confirmed green.
func TestSelfHeal_UnresolvableConflictNoForcePush_KeepsComplete(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 0
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)

	landing := s.selfHeal(dispatch.NewFake(), "1", testPR)

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
// snapshots the issue's labels before delegating — capturing what the label
// looks like mid-landing-path (issue #757), the same wrapper pattern
// terminatingDispatcher uses in terminate_test.go.
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

// TestSelfHeal_LabelStaysInProgressThroughConflictResolve verifies that the
// InProgress->Complete swap is held until the landing path settles (issue
// #757): mid-conflict-resolve, the issue must still carry agent-in-progress,
// not agent-complete — the label must not claim the agent has nothing left
// to do while a conflict-resolve box is still running. Only after the retried
// merge succeeds does the issue swap to agent-complete, exactly once.
func TestSelfHeal_LabelStaysInProgressThroughConflictResolve(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict, nil}
	fc.RebaseErr = forge.ErrMergeConflict
	// 2 states for the initial gateToGreen confirm, 2 more for the
	// post-force-push rewait's own gateToGreen confirm.
	fc.SetCheckStates(testPR, []forge.RollupState{
		forge.StateSuccess, forge.StateSuccess,
		forge.StateSuccess, forge.StateSuccess,
	})
	s := New(c, fc, fc)
	d := &labelSnapshotDispatcher{Fake: dispatch.NewFake(), fc: fc, num: "1"}

	landing := s.selfHeal(d, "1", testPR)

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

// TestSelfHeal_GitForge_PushOnlyLanding verifies that for a push-only Code
// Forge, selfHeal skips the CI-wait/merge-gate entirely (there is no CI or PR
// to watch — the Box already pushed the branch) and instead marks the issue
// Complete immediately, then applies MERGE_MODE against the push-only Merge.
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
			s := New(c, fc, fc.AsPushOnly())

			landing := s.selfHeal(dispatch.NewFake(), "1", branch)

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

// TestSelfHeal_GitForge_PushFailureStaysCompleteNotFailed verifies that for a
// push-only Code Forge, a push failure under MERGE_MODE=immediate leaves the
// issue at agent-complete with a comment — never demoted to agent-failed —
// matching the github adapter's post-green merge-blocked contract (ADR 0012).
func TestSelfHeal_GitForge_PushFailureStaysCompleteNotFailed(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErr = errors.New("remote rejected: non-fast-forward")
	branch := "agent/issue-1"
	s := New(c, fc, fc.AsPushOnly())

	landing := s.selfHeal(dispatch.NewFake(), "1", branch)

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
