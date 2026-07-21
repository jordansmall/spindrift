package settle

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// TestSettle_LocalForge_BlockedPostsNoteAsComment verifies that a local
// Dispatch's blocked status posts the outcome's note field as a comment via
// Comment(num, note) — the host-mediated substitute for the gh issue comment
// call the Box's issue-prompt runs under github (ADR 0032, issue #1692). The
// tracker's LandingRecorder-implementing shape (plain forge.NewFake()) is
// this test suite's "local" convention (see AsLocal/AsNoLandingRecorder).
func TestSettle_LocalForge_BlockedPostsNoteAsComment(t *testing.T) {
	const issNum = "42"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	d := dispatch.NewFake()
	result := dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: issNum, Landing: "agent/issue-42", Status: "blocked", Note: "push rejected; PR opened as draft"},
	}

	s := New(baseConfig(), fc, fc)
	s.Settle(d, issNum, 0, result)

	var noteCalls []forge.CommentCall
	for _, c := range fc.CommentCalls {
		if c.Body == result.Outcome.Note {
			noteCalls = append(noteCalls, c)
		}
	}
	if len(noteCalls) != 1 {
		t.Fatalf("want 1 comment posting the blocked note, got %d (all calls: %+v)", len(noteCalls), fc.CommentCalls)
	}
	if noteCalls[0].Num != issNum {
		t.Errorf("comment Num: got %q, want %q", noteCalls[0].Num, issNum)
	}
}

// TestSelfHeal_LocalForge_MergeConflictThenRebaseSucceedsRetriesWithoutPanic
// asserts that when MaxRebaseAttempts > 0 (the schema default is 3, not
// baseConfig's 0), a merge conflict followed by a clean Rebase doesn't crash:
// mergeImmediate's reactive retry loop unconditionally re-waits for CI on
// every successful Rebase (rewaitAfterForcePush -> gateToGreen ->
// s.pr.CheckState), but a push-only forge (s.pr == nil, git and local alike)
// has no CI to wait for. Concurrent seams landing onto the same Integration
// branch make this conflict-then-clean-rebase sequence a routine occurrence
// under CODE_FORGE=local specifically, not a rare edge case.
func TestSelfHeal_LocalForge_MergeConflictThenRebaseSucceedsRetriesWithoutPanic(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.MaxRebaseAttempts = 3
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	branch := "agent/issue-1"
	s := New(c, fc, fc.AsLocal())

	landing := s.selfHeal(dispatch.NewFake(), "1", 0, branch)

	if landing != landingMerged {
		t.Errorf("selfHeal = %v, want landingMerged after the rebase-then-retry succeeds", landing)
	}
	if len(fc.RebasedURLs) != 1 || fc.RebasedURLs[0] != branch {
		t.Errorf("expected exactly one Rebase(%q), got %v", branch, fc.RebasedURLs)
	}
	if fc.Merged != branch {
		t.Errorf("expected the retried Merge(%q) to have succeeded; fc.Merged=%q", branch, fc.Merged)
	}
}

// TestSelfHeal_LocalForge_MergeConflictAfterRelayBlocksNotFails asserts a
// merge conflict (the Integration branch has diverged since the bundle was
// built) blocks the seam the same way a missing bundle does, but only after
// the relay itself has already succeeded — the conflict is a property of the
// merge, not the relay (ADR 0033: "a conflicting merge leaves the seam
// unlanded and blocked").
func TestSelfHeal_LocalForge_MergeConflictAfterRelayBlocksNotFails(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErr = forge.ErrMergeConflict
	branch := "agent/issue-1"
	s := New(c, fc, fc.AsLocal())

	landing := s.selfHeal(dispatch.NewFake(), "1", 0, branch)

	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual on a merge conflict", landing)
	}
	if len(fc.RelayBundleCalls) != 1 {
		t.Errorf("expected the relay to run once ahead of the failed merge attempt, got %d", len(fc.RelayBundleCalls))
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after a blocked merge; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed after a blocked merge; labels=%v", iss.Labels)
	}
}

// TestSelfHeal_LocalForge_RelaysBundleBeforeMergeAndRecordsLandingRef asserts
// the CODE_FORGE=local landing path (ADR 0033): before Merge is attempted,
// the Box's outbox bundle is relayed in via the forge's optional
// forge.BundleRelay hook; once merged, the richer forge.LandingRef value —
// not the raw branch name — is what gets recorded as the issue's landing:.
func TestSelfHeal_LocalForge_RelaysBundleBeforeMergeAndRecordsLandingRef(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.LandingRefValue = "integration/1694@abc123"
	branch := "agent/issue-1"
	s := New(c, fc, fc.AsLocal())

	landing := s.selfHeal(dispatch.NewFake(), "1", 0, branch)

	if landing != landingMerged {
		t.Fatalf("selfHeal = %v, want landingMerged", landing)
	}
	if len(fc.RelayBundleCalls) != 1 {
		t.Fatalf("expected exactly one RelayBundle call, got %d: %+v", len(fc.RelayBundleCalls), fc.RelayBundleCalls)
	}
	if got, want := fc.RelayBundleCalls[0], (forge.RelayBundleCall{OutboxDir: "/outbox/1", Ref: branch}); got != want {
		t.Errorf("RelayBundle call = %+v, want %+v", got, want)
	}
	if fc.Merged != branch {
		t.Errorf("expected Merge(%q) after relay; fc.Merged=%q", branch, fc.Merged)
	}
	if len(fc.RecordLandingCalls) == 0 {
		t.Fatal("expected RecordLanding to be called")
	}
	if got, want := fc.RecordLandingCalls[len(fc.RecordLandingCalls)-1], (forge.RecordLandingCall{Num: "1", Landing: "integration/1694@abc123"}); got != want {
		t.Errorf("final RecordLanding call = %+v, want %+v", got, want)
	}
}

// TestSelfHeal_LocalForge_UsesPerIssueCodeForgeForMerge asserts that when
// Config.CodeForgeForIssue is set, mergeImmediate's RelayBundle/Merge calls
// land through the resolved-for-this-issue instance it returns, not the
// single cf New() received — CODE_FORGE=local's per-seam Integration branch
// keying (ADR 0033, issue #1734): a mixed-parent batch must merge each seam
// through its own resolved instance, not whichever one the run's shared cf
// happened to be constructed with.
func TestSelfHeal_LocalForge_UsesPerIssueCodeForgeForMerge(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	sharedFC := forge.NewFake(testDispatchLabels)
	sharedFC.SetIssue(forge.Issue{Number: "10", Labels: []string{"agent-in-progress"}})
	ownFC := forge.NewFake()
	c.CodeForgeForIssue = func(num string) forge.CodeForge {
		if num == "10" {
			return ownFC.AsLocal()
		}
		return sharedFC.AsLocal()
	}
	branch := "agent/issue-10"
	s := New(c, sharedFC, sharedFC.AsLocal())

	landing := s.selfHeal(dispatch.NewFake(), "10", 0, branch)

	if landing != landingMerged {
		t.Fatalf("selfHeal = %v, want landingMerged", landing)
	}
	if ownFC.Merged != branch {
		t.Errorf("expected the per-issue instance to receive Merge(%q); ownFC.Merged=%q", branch, ownFC.Merged)
	}
	if sharedFC.Merged != "" {
		t.Errorf("expected the shared cf to receive no Merge call; sharedFC.Merged=%q", sharedFC.Merged)
	}
	if len(ownFC.RelayBundleCalls) != 1 {
		t.Errorf("expected the per-issue instance to receive the RelayBundle call, got %d", len(ownFC.RelayBundleCalls))
	}
	if len(sharedFC.RelayBundleCalls) != 0 {
		t.Errorf("expected the shared cf to receive no RelayBundle call, got %d", len(sharedFC.RelayBundleCalls))
	}
}

// TestSelfHeal_LocalForge_LandingRefErrorStaysMergedWithoutRecording asserts
// LandingRef is a best-effort enrichment: a resolution failure after a
// successful merge must never turn an actual land into a failure — the seam
// stays landingMerged, it's only the richer landing: overwrite that's
// skipped (RecordLanding keeps whatever recordLanding wrote earlier from the
// outcome line's raw landing= field).
func TestSelfHeal_LocalForge_LandingRefErrorStaysMergedWithoutRecording(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.LandingRefErr = errors.New("integration branch vanished")
	branch := "agent/issue-1"
	s := New(c, fc, fc.AsLocal())

	landing := s.selfHeal(dispatch.NewFake(), "1", 0, branch)

	if landing != landingMerged {
		t.Fatalf("selfHeal = %v, want landingMerged despite the LandingRef failure", landing)
	}
	if fc.Merged != branch {
		t.Errorf("expected Merge(%q) to have succeeded; fc.Merged=%q", branch, fc.Merged)
	}
	if len(fc.RecordLandingCalls) != 0 {
		t.Errorf("expected no RecordLanding overwrite when LandingRef fails, got %+v", fc.RecordLandingCalls)
	}
}

// TestSelfHeal_LocalForge_NilOutboxDirFailsLoudly asserts a misconfigured
// Settle — a Code Forge implementing forge.BundleRelay but no OutboxDir
// resolver supplied — errors instead of silently relaying against an empty
// path, so a wiring bug surfaces immediately rather than as a confusing
// "bundle missing" note pointing at "/seam.bundle".
func TestSelfHeal_LocalForge_NilOutboxDirFailsLoudly(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	branch := "agent/issue-1"
	s := New(c, fc, fc.AsLocal())

	landing := s.selfHeal(dispatch.NewFake(), "1", 0, branch)

	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual when OutboxDir is unset", landing)
	}
	if len(fc.RelayBundleCalls) != 0 {
		t.Errorf("RelayBundle must not be called with no OutboxDir resolver, got %+v", fc.RelayBundleCalls)
	}
}

// TestSelfHeal_LocalForge_MissingBundleBlocksNotFails asserts a RelayBundle
// failure (missing/malformed bundle, ADR 0033) leaves the seam unlanded via
// the same merge-blocked-stays-complete posture an ordinary push failure
// already gets (TestSelfHeal_GitForge_PushFailureStaysCompleteNotFailed) —
// never demoted to agent-failed, and Merge itself is never attempted.
func TestSelfHeal_LocalForge_MissingBundleBlocksNotFails(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "immediate"
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.RelayBundleErr = errors.New("bundle missing")
	branch := "agent/issue-1"
	s := New(c, fc, fc.AsLocal())

	landing := s.selfHeal(dispatch.NewFake(), "1", 0, branch)

	if landing != landingManual {
		t.Errorf("selfHeal = %v, want landingManual when the bundle relay fails", landing)
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called when relay fails; fc.Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("issue must carry agent-complete after a blocked relay; labels=%v", iss.Labels)
	}
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("issue must NOT carry agent-failed after a blocked relay; labels=%v", iss.Labels)
	}
	if len(fc.CommentCalls) != 1 {
		t.Fatalf("expected exactly one merge-blocked comment, got %d: %+v", len(fc.CommentCalls), fc.CommentCalls)
	}
}
