package reconcile_test

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/reconcile"
)

// TestRun_ClosesIssueWithMergedLanding verifies Reconcile closes an open
// issue whose recorded landing PR has merged (ADR 0029's core close-on-merge
// behavior).
func TestRun_ClosesIssueWithMergedLanding(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	res, err := reconcile.Run(f, f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != "42" {
		t.Errorf("Closed = %v, want [42]", res.Closed)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.State != forge.IssueClosed {
		t.Errorf("State = %v, want IssueClosed", iss.State)
	}
}

// TestRun_LeavesOpenLandingPRUntouched verifies Reconcile leaves an issue
// alone when its recorded landing PR is still open — green-and-mergeable or
// in approval limbo, either way not reconcile's call to act on.
func TestRun_LeavesOpenLandingPRUntouched(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PROpen)

	res, err := reconcile.Run(f, f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 0 {
		t.Errorf("Closed = %v, want none", res.Closed)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.State != forge.IssueOpen {
		t.Errorf("State = %v, want unchanged IssueOpen", iss.State)
	}
}

// TestRun_SkipsIssueWithNoLanding verifies Reconcile leaves an issue with no
// recorded landing untouched — nothing to check the forge against yet.
func TestRun_SkipsIssueWithNoLanding(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen})

	res, err := reconcile.Run(f, f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 0 {
		t.Errorf("Closed = %v, want none", res.Closed)
	}
}

// TestRun_SecondSweepIsNoOp verifies a second Run over the same state closes
// nothing further — idempotency: an already-closed issue no longer appears
// in ListOpenIssues, so it is never reprocessed.
func TestRun_SecondSweepIsNoOp(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	if _, err := reconcile.Run(f, f); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	res, err := reconcile.Run(f, f)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(res.Closed) != 0 {
		t.Errorf("second sweep Closed = %v, want none", res.Closed)
	}
	if len(f.CloseIssueCalls) != 1 {
		t.Errorf("CloseIssueCalls = %v, want exactly 1 call across both sweeps", f.CloseIssueCalls)
	}
}

// TestRun_DiscoversMergedLandingByBranchAndCloses verifies Reconcile
// discovers an issue's PR by its agent branch when no landing was recorded
// (the box died before its outcome line was parsed), records the landing,
// and closes the issue when the discovered PR is merged (ADR 0029 branch
// discovery).
func TestRun_DiscoversMergedLandingByBranchAndCloses(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen})
	branch := f.AgentBranch("42")
	f.SetPR(branch, forge.PR{URL: "https://github.com/o/r/pull/7"})
	f.SetPRState("https://github.com/o/r/pull/7", forge.PRMerged)

	res, err := reconcile.Run(f, f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != "42" {
		t.Errorf("Closed = %v, want [42]", res.Closed)
	}
	if len(f.RecordLandingCalls) != 1 || f.RecordLandingCalls[0] != (forge.RecordLandingCall{Num: "42", Landing: "https://github.com/o/r/pull/7"}) {
		t.Errorf("RecordLandingCalls = %v, want one call recording the discovered PR", f.RecordLandingCalls)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.State != forge.IssueClosed {
		t.Errorf("State = %v, want IssueClosed", iss.State)
	}
}

// TestRun_DiscoversOpenLandingByBranchAndLeavesIssueOpen verifies Reconcile
// records a discovered branch PR's landing even when that PR is still open,
// but does not close the issue — an open PR, green or in approval limbo, is
// left for a later sweep (ADR 0029 branch discovery).
func TestRun_DiscoversOpenLandingByBranchAndLeavesIssueOpen(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen})
	branch := f.AgentBranch("42")
	f.SetPR(branch, forge.PR{URL: "https://github.com/o/r/pull/7"})

	res, err := reconcile.Run(f, f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 0 {
		t.Errorf("Closed = %v, want none", res.Closed)
	}
	if len(f.RecordLandingCalls) != 1 || f.RecordLandingCalls[0] != (forge.RecordLandingCall{Num: "42", Landing: "https://github.com/o/r/pull/7"}) {
		t.Errorf("RecordLandingCalls = %v, want one call recording the discovered PR", f.RecordLandingCalls)
	}
	if len(f.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none", f.CloseIssueCalls)
	}
}

// TestRun_DiscoversClosedUnmergedLandingByBranchAndFlagsAbandoned verifies
// Reconcile flags an issue abandoned when the PR it discovers by branch (no
// landing was recorded) turns out to have been closed without merging — the
// discovery path feeds the same abandoned check as a pre-recorded landing.
func TestRun_DiscoversClosedUnmergedLandingByBranchAndFlagsAbandoned(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen})
	branch := f.AgentBranch("42")
	f.SetPR(branch, forge.PR{URL: "https://github.com/o/r/pull/7"})
	f.SetPRState("https://github.com/o/r/pull/7", forge.PRClosed)

	res, err := reconcile.Run(f, f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Abandoned) != 1 || res.Abandoned[0] != "42" {
		t.Errorf("Abandoned = %v, want [42]", res.Abandoned)
	}
	if len(f.RecordLandingCalls) != 1 || f.RecordLandingCalls[0] != (forge.RecordLandingCall{Num: "42", Landing: "https://github.com/o/r/pull/7"}) {
		t.Errorf("RecordLandingCalls = %v, want one call recording the discovered PR", f.RecordLandingCalls)
	}
	if len(f.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none", f.CloseIssueCalls)
	}
}

// TestRun_FlagsAbandonedWhenLandingPRClosedUnmerged verifies Reconcile flags
// an issue abandoned — rather than closing it or leaving it open forever —
// when its recorded landing PR was closed without merging (a human rejected
// it, ADR 0029).
func TestRun_FlagsAbandonedWhenLandingPRClosedUnmerged(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRClosed)

	res, err := reconcile.Run(f, f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 0 {
		t.Errorf("Closed = %v, want none", res.Closed)
	}
	if len(res.Abandoned) != 1 || res.Abandoned[0] != "42" {
		t.Errorf("Abandoned = %v, want [42]", res.Abandoned)
	}
	if len(f.FlagAbandonedCalls) != 1 || f.FlagAbandonedCalls[0] != "42" {
		t.Errorf("FlagAbandonedCalls = %v, want [42]", f.FlagAbandonedCalls)
	}
	if len(f.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none", f.CloseIssueCalls)
	}
}

// TestRun_SecondSweepDoesNotReflagAbandoned verifies a second Run over an
// already-abandoned issue does not flag it again — unlike a close, an
// abandon leaves the issue open (and so still in ListOpenIssues), so
// idempotency here rests on Reconcile itself skipping an already-abandoned
// issue rather than on it dropping out of the open list.
func TestRun_SecondSweepDoesNotReflagAbandoned(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRClosed)

	if _, err := reconcile.Run(f, f); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	res, err := reconcile.Run(f, f)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(res.Abandoned) != 0 {
		t.Errorf("second sweep Abandoned = %v, want none", res.Abandoned)
	}
	if len(f.FlagAbandonedCalls) != 1 {
		t.Errorf("FlagAbandonedCalls = %v, want exactly 1 call across both sweeps", f.FlagAbandonedCalls)
	}
}

// TestRun_NoOpForNonLocalTracker verifies Reconcile is a clean no-op — not
// an error — against a tracker with no IssueCloser surface (github/jira's
// shape), even though a merged landing PR exists.
func TestRun_NoOpForNonLocalTracker(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)
	it := f.AsNoLandingRecorder()

	res, err := reconcile.Run(it, f)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 0 {
		t.Errorf("Closed = %v, want none", res.Closed)
	}
	if len(f.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none", f.CloseIssueCalls)
	}
}

// TestRun_NoOpForPushOnlyCodeForge verifies Reconcile is a clean no-op
// against a Code Forge with no PRForge surface (the push-only git adapter's
// shape) — there is no PR merge state to check.
func TestRun_NoOpForPushOnlyCodeForge(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "some-branch"})
	cf := f.AsPushOnly()

	res, err := reconcile.Run(f, cf)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 0 {
		t.Errorf("Closed = %v, want none", res.Closed)
	}
	if len(f.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none", f.CloseIssueCalls)
	}
}

// TestRun_NeverMergesOrPushes verifies a Reconcile sweep that closes an
// issue leaves the Code Forge's landing-path methods (Merge, Rebase,
// EnqueueAutoMerge, MarkReady) untouched — reconcile is observational only.
func TestRun_NeverMergesOrPushes(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	if _, err := reconcile.Run(f, f); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.Merged != "" {
		t.Errorf("Merged = %q, want reconcile to never merge", f.Merged)
	}
	if len(f.RebasedURLs) != 0 {
		t.Errorf("RebasedURLs = %v, want reconcile to never rebase", f.RebasedURLs)
	}
	if len(f.LandingCallLog) != 0 {
		t.Errorf("LandingCallLog = %v, want reconcile to never touch the landing path", f.LandingCallLog)
	}
}
