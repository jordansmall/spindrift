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
