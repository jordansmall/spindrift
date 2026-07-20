package reconcile_test

import (
	"errors"
	"slices"
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

	res, err := reconcile.Run(f, f, fakeLiveness{})
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

	res, err := reconcile.Run(f, f, fakeLiveness{})
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

	res, err := reconcile.Run(f, f, fakeLiveness{})
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

	if _, err := reconcile.Run(f, f, fakeLiveness{}); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	res, err := reconcile.Run(f, f, fakeLiveness{})
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

	res, err := reconcile.Run(f, f, fakeLiveness{})
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

	res, err := reconcile.Run(f, f, fakeLiveness{})
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

	res, err := reconcile.Run(f, f, fakeLiveness{})
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

	res, err := reconcile.Run(f, f, fakeLiveness{})
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

	if _, err := reconcile.Run(f, f, fakeLiveness{}); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	res, err := reconcile.Run(f, f, fakeLiveness{})
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

	res, err := reconcile.Run(it, f, fakeLiveness{})
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

	res, err := reconcile.Run(f, cf, fakeLiveness{})
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

// TestRun_ClosesLocalLandingVerifiedMerged verifies Reconcile closes an open
// issue whose recorded landing verifies as merged into the local Code
// Forge's Integration branch (CODE_FORGE=local, ADR 0033) — the no-PR
// counterpart of TestRun_ClosesIssueWithMergedLanding, checked via
// LandingVerifier rather than PRForge.
func TestRun_ClosesLocalLandingVerifiedMerged(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "integration/1694@abc123"})
	f.SetVerifyLanding("integration/1694@abc123", true, nil)
	cf := f.AsLocal()

	res, err := reconcile.Run(f, cf, fakeLiveness{})
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

// TestRun_LeavesLocalLandingOpenWhenNotVerifiedMerged verifies Reconcile
// leaves an issue open when its recorded local landing does not verify as
// merged — a conflicting land (ADR 0033: "a conflicting merge leaves the
// seam unlanded and blocked") or a malformed landing ref both surface here
// identically as VerifyLanding reporting merged=false.
func TestRun_LeavesLocalLandingOpenWhenNotVerifiedMerged(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "agent/issue-42"})
	f.SetVerifyLanding("agent/issue-42", false, nil)
	cf := f.AsLocal()

	res, err := reconcile.Run(f, cf, fakeLiveness{})
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
	if len(f.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none", f.CloseIssueCalls)
	}
}

// TestRun_SkipsLocalIssueWithNoLanding verifies Reconcile leaves a local
// issue with no recorded landing untouched, never calling VerifyLanding — a
// local landing has no branch-discovery fallback (unlike the PRForge path):
// settle is the only writer of the landing: field.
func TestRun_SkipsLocalIssueWithNoLanding(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen})
	cf := f.AsLocal()

	res, err := reconcile.Run(f, cf, fakeLiveness{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 0 {
		t.Errorf("Closed = %v, want none", res.Closed)
	}
	if len(f.VerifyLandingCalls) != 0 {
		t.Errorf("VerifyLandingCalls = %v, want none", f.VerifyLandingCalls)
	}
}

// TestRun_SecondSweepLocalLandingIsNoOp verifies a second Run over an
// already-closed local landing closes nothing further, mirroring
// TestRun_SecondSweepIsNoOp for the LandingVerifier path.
func TestRun_SecondSweepLocalLandingIsNoOp(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "integration/1694@abc123"})
	f.SetVerifyLanding("integration/1694@abc123", true, nil)
	cf := f.AsLocal()

	if _, err := reconcile.Run(f, cf, fakeLiveness{}); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	res, err := reconcile.Run(f, cf, fakeLiveness{})
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

// TestRun_PropagatesLocalLandingVerifyError verifies Reconcile surfaces a
// genuine VerifyLanding error (a local-git failure, not the normal
// merged=false "not landed yet" outcome) rather than swallowing it.
func TestRun_PropagatesLocalLandingVerifyError(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "integration/1694@abc123"})
	wantErr := errors.New("local: repo unreadable")
	f.SetVerifyLanding("integration/1694@abc123", false, wantErr)
	cf := f.AsLocal()

	_, err := reconcile.Run(f, cf, fakeLiveness{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want it to wrap %v", err, wantErr)
	}
	if len(f.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none", f.CloseIssueCalls)
	}
}

// fakeLiveness scripts LivenessProbe per issue number for tests. Zero value
// never triggers a reset by itself: LogStale defaults to false (not stale)
// and ContainerLive defaults to (false, false) (not live, not reachable) —
// tests opt in per issue number to the death-signal values they want to
// assert against.
type fakeLiveness struct {
	stale     map[string]bool
	live      map[string]bool
	reachable map[string]bool
}

func (f fakeLiveness) LogStale(num string) bool { return f.stale[num] }

func (f fakeLiveness) ContainerLive(num string) (live, reachable bool) {
	return f.live[num], f.reachable[num]
}

// TestRun_ResetsOrphanedInProgressIssue verifies Reconcile resets an
// InProgress issue to Dispatchable when the full composite death signal
// holds: no PR/branch for its agent branch, a stale Box log, and (runtime
// reachable) no live container.
func TestRun_ResetsOrphanedInProgressIssue(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	lp := fakeLiveness{
		stale:     map[string]bool{"42": true},
		reachable: map[string]bool{"42": true},
	}

	res, err := reconcile.Run(f, f, lp)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Reset) != 1 || res.Reset[0] != "42" {
		t.Errorf("Reset = %v, want [42]", res.Reset)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !slices.Contains(iss.Labels, "dispatchable") || slices.Contains(iss.Labels, "in-progress") {
		t.Errorf("Labels = %v, want dispatchable in place of in-progress", iss.Labels)
	}
}

// TestRun_ResetsOrphanedInProgressIssue_UnreachableRuntime verifies Reconcile
// still resets when the container runtime could not be queried: an
// unreachable runtime is no evidence of a live container, so it must not
// withhold a reset the log/PR signal otherwise supports.
func TestRun_ResetsOrphanedInProgressIssue_UnreachableRuntime(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	lp := fakeLiveness{stale: map[string]bool{"42": true}} // reachable defaults to false

	res, err := reconcile.Run(f, f, lp)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Reset) != 1 || res.Reset[0] != "42" {
		t.Errorf("Reset = %v, want [42]", res.Reset)
	}
}

// TestRun_LeavesInProgressUntouched_WhenPRExistsForBranch verifies Reconcile
// never resets an InProgress issue whose agent branch already carries a PR
// (any state) — evidence a runner touched the branch, even if that PR later
// closed unmerged.
func TestRun_LeavesInProgressUntouched_WhenPRExistsForBranch(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	f.SetPR(f.AgentBranch("42"), forge.PR{URL: "https://github.com/o/r/pull/9"})
	f.SetPRState("https://github.com/o/r/pull/9", forge.PRClosed)
	lp := fakeLiveness{stale: map[string]bool{"42": true}, reachable: map[string]bool{"42": true}}

	res, err := reconcile.Run(f, f, lp)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Reset) != 0 {
		t.Errorf("Reset = %v, want none — a PR exists for the branch", res.Reset)
	}
	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !slices.Contains(iss.Labels, "in-progress") {
		t.Errorf("Labels = %v, want in-progress untouched", iss.Labels)
	}
}

// TestRun_LeavesInProgressUntouched_WhenBranchExistsNoPR verifies Reconcile
// never resets an InProgress issue whose agent branch was pushed but has no
// PR yet — the die-after-push-before-PR window the composite gate must not
// silently re-dispatch over.
func TestRun_LeavesInProgressUntouched_WhenBranchExistsNoPR(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	f.SetBranchExists(f.AgentBranch("42"), true)
	lp := fakeLiveness{stale: map[string]bool{"42": true}, reachable: map[string]bool{"42": true}}

	res, err := reconcile.Run(f, f, lp)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Reset) != 0 {
		t.Errorf("Reset = %v, want none — the agent branch already exists", res.Reset)
	}
	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !slices.Contains(iss.Labels, "in-progress") {
		t.Errorf("Labels = %v, want in-progress untouched", iss.Labels)
	}
}

// TestRun_LeavesInProgressUntouched_WhenLogFresh verifies Reconcile never
// resets an InProgress issue whose Box log is not stale — a live or
// recently active Box still owns it.
func TestRun_LeavesInProgressUntouched_WhenLogFresh(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	lp := fakeLiveness{reachable: map[string]bool{"42": true}} // stale defaults to false

	res, err := reconcile.Run(f, f, lp)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Reset) != 0 {
		t.Errorf("Reset = %v, want none — the log is fresh", res.Reset)
	}
}

// TestRun_LeavesInProgressUntouched_WhenContainerLive verifies Reconcile
// never resets an InProgress issue whose Box container is still running,
// even with no PR/branch recorded yet and a stale log — the container is the
// most direct evidence a live runner still owns the issue.
func TestRun_LeavesInProgressUntouched_WhenContainerLive(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	lp := fakeLiveness{
		stale:     map[string]bool{"42": true},
		live:      map[string]bool{"42": true},
		reachable: map[string]bool{"42": true},
	}

	res, err := reconcile.Run(f, f, lp)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Reset) != 0 {
		t.Errorf("Reset = %v, want none — the container is still live", res.Reset)
	}
}

// TestRun_ResetIsIdempotent verifies a second sweep after a reset does
// nothing further — the issue is Dispatchable now, so ListIssues(InProgress)
// no longer surfaces it.
func TestRun_ResetIsIdempotent(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	lp := fakeLiveness{stale: map[string]bool{"42": true}, reachable: map[string]bool{"42": true}}

	if _, err := reconcile.Run(f, f, lp); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	res, err := reconcile.Run(f, f, lp)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(res.Reset) != 0 {
		t.Errorf("second sweep Reset = %v, want none", res.Reset)
	}
	if len(f.TransitionStateCalls) != 1 {
		t.Errorf("TransitionStateCalls = %v, want exactly 1 across both sweeps", f.TransitionStateCalls)
	}
}

// TestRun_NeverMergesOrPushes verifies a Reconcile sweep that closes an
// issue leaves the Code Forge's landing-path methods (Merge, Rebase,
// EnqueueAutoMerge, MarkReady) untouched — reconcile is observational only.
func TestRun_NeverMergesOrPushes(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	if _, err := reconcile.Run(f, f, fakeLiveness{}); err != nil {
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
