package reconcile_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/reconcile"
	"spindrift.dev/launcher/internal/testutil"
)

// capsFor resolves forge.Capabilities the same way production's newReadContext
// does (issue #2946), so no test has to hand-list which optional interfaces its
// particular *forge.Fake shape implements.
func capsFor(it forge.IssueTracker, cf forge.CodeForge) forge.Capabilities {
	return forge.ResolveCapabilities(cf, it, backend.Descriptor{}, backend.Descriptor{})
}

// ADR 0029's core close-on-merge behavior: a merged landing PR closes its
// open issue.
func TestRun_ClosesIssueWithMergedLanding(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	res, err := reconcile.Run(f, f, fakeLiveness{}, capsFor(f, f), selfScope)
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

// An open landing PR, green-and-mergeable or in approval limbo, is not
// reconcile's call to act on.
func TestRun_LeavesOpenLandingPRUntouched(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PROpen)

	res, err := reconcile.Run(f, f, fakeLiveness{}, capsFor(f, f), selfScope)
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

// With no recorded landing there is nothing to check the forge against yet.
func TestRun_SkipsIssueWithNoLanding(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen})

	res, err := reconcile.Run(f, f, fakeLiveness{}, capsFor(f, f), selfScope)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 0 {
		t.Errorf("Closed = %v, want none", res.Closed)
	}
}

// Idempotency: an already-closed issue no longer appears in ListOpenIssues,
// so a second sweep never reprocesses it.
func TestRun_SecondSweepIsNoOp(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	if _, err := reconcile.Run(f, f, fakeLiveness{}, capsFor(f, f), selfScope); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	res, err := reconcile.Run(f, f, fakeLiveness{}, capsFor(f, f), selfScope)
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

// ADR 0029 branch discovery: no landing was recorded because the Box died
// before its outcome line was parsed, so Reconcile finds the PR by agent
// branch, records it, and closes the issue once that PR has merged.
func TestRun_DiscoversMergedLandingByBranchAndCloses(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen})
	branch := f.AgentBranch("42")
	f.SetPR(branch, forge.PR{URL: "https://github.com/o/r/pull/7"})
	f.SetPRState("https://github.com/o/r/pull/7", forge.PRMerged)

	res, err := reconcile.Run(f, f, fakeLiveness{}, capsFor(f, f), selfScope)
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

// ADR 0029 branch discovery records a discovered PR's landing even while that
// PR is still open, leaving the issue itself for a later sweep.
func TestRun_DiscoversOpenLandingByBranchAndLeavesIssueOpen(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen})
	branch := f.AgentBranch("42")
	f.SetPR(branch, forge.PR{URL: "https://github.com/o/r/pull/7"})

	res, err := reconcile.Run(f, f, fakeLiveness{}, capsFor(f, f), selfScope)
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

// The branch-discovery path feeds the same abandoned check as a pre-recorded
// landing, so a discovered PR closed without merging flags the issue too.
func TestRun_DiscoversClosedUnmergedLandingByBranchAndFlagsAbandoned(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen})
	branch := f.AgentBranch("42")
	f.SetPR(branch, forge.PR{URL: "https://github.com/o/r/pull/7"})
	f.SetPRState("https://github.com/o/r/pull/7", forge.PRClosed)

	res, err := reconcile.Run(f, f, fakeLiveness{}, capsFor(f, f), selfScope)
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

// ADR 0029: a landing PR closed without merging means a human rejected it, so
// Reconcile flags the issue abandoned rather than closing it or leaving it
// open forever.
func TestRun_FlagsAbandonedWhenLandingPRClosedUnmerged(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRClosed)

	res, err := reconcile.Run(f, f, fakeLiveness{}, capsFor(f, f), selfScope)
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

// Unlike a close, an abandon leaves the issue open and so still in
// ListOpenIssues. Idempotency here rests on Reconcile skipping an
// already-abandoned issue, not on it dropping out of the open list.
func TestRun_SecondSweepDoesNotReflagAbandoned(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRClosed)

	if _, err := reconcile.Run(f, f, fakeLiveness{}, capsFor(f, f), selfScope); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	res, err := reconcile.Run(f, f, fakeLiveness{}, capsFor(f, f), selfScope)
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

// A tracker that does not implement IssueCloser (github/jira's shape) is a
// clean no-op, not an error, even with a merged landing PR.
func TestRun_NoOpForNonLocalTracker(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)
	it := f.AsNoLandingRecorder()

	res, err := reconcile.Run(it, f, fakeLiveness{}, capsFor(it, f), selfScope)
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

// A Code Forge that does not implement PRForge (the push-only git adapter's
// shape) has no PR merge state to check, so the sweep is a clean no-op.
func TestRun_NoOpForPushOnlyCodeForge(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "some-branch"})
	cf := f.AsPushOnly()

	res, err := reconcile.Run(f, cf, fakeLiveness{}, capsFor(f, cf), selfScope)
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

// CODE_FORGE=local, ADR 0033: the no-PR counterpart of
// TestRun_ClosesIssueWithMergedLanding, where the close check is
// LandingContained against the Integration branch rather than PRForge.
func TestRun_ClosesLocalLandingVerifiedMerged(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "integration/1694@abc123"})
	f.SetLandingContained("integration/1694@abc123", "42", true, nil)
	cf := f.AsLocal()

	res, err := reconcile.Run(f, cf, fakeLiveness{}, capsFor(f, cf), selfScope)
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

// ADR 0033: "a conflicting merge leaves the seam unlanded and blocked". That
// conflicting land reports contained=false from LandingContained for the
// already-upgraded IntegrationRef form, and the issue stays open.
func TestRun_LeavesLocalLandingOpenWhenNotVerifiedMerged(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "integration/1694@abc123"})
	f.SetLandingContained("integration/1694@abc123", "42", false, nil)
	cf := f.AsLocal()

	res, err := reconcile.Run(f, cf, fakeLiveness{}, capsFor(f, cf), selfScope)
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

// Issue #1809, the silent-stuck cluster this typed repair path replaces: for a
// LandingBranchRef (settle's pre-merge record) that is not yet contained,
// Reconcile prints a loud, branch-naming stuck verdict, never a silent no-op.
func TestRun_PrintsStuckVerdictForUnmergedBranchRefLanding(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "agent/issue-42"})
	f.SetLandingContained("agent/issue-42", "42", false, nil)
	cf := f.AsLocal()

	out := testutil.CaptureStdout(t, func() {
		res, err := reconcile.Run(f, cf, fakeLiveness{}, capsFor(f, cf), selfScope)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(res.Closed) != 0 {
			t.Errorf("Closed = %v, want none", res.Closed)
		}
	})
	if !strings.Contains(out, "agent/issue-42") {
		t.Errorf("stuck verdict must name the branch; got: %q", out)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.State != forge.IssueOpen {
		t.Errorf("State = %v, want unchanged IssueOpen", iss.State)
	}
}

// Issue #1811: Result carries a stuck LandingBranchRef's branch name keyed by
// issue number, so Surface can name "stuck landing" as a broad ticket's held
// gate without redoing the containment check Run just performed.
func TestRun_ReportsStuckBranchRefInResult(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "agent/issue-42"})
	f.SetLandingContained("agent/issue-42", "42", false, nil)
	cf := f.AsLocal()

	res, err := reconcile.Run(f, cf, fakeLiveness{}, capsFor(f, cf), selfScope)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := res.Stuck["42"], "agent/issue-42"; got != want {
		t.Errorf(`Stuck["42"] = %q, want %q`, got, want)
	}
}

// Issue #1809's healing behavior for a seam whose post-merge landing upgrade
// never ran even though the merge succeeded: Reconcile upgrades a
// LandingBranchRef it confirms already landed to the rich IntegrationRef form
// and closes the seam through the normal close path.
func TestRun_HealsBranchRefLandingWhenAncestorOfIntegration(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "agent/issue-42"})
	f.SetLandingContained("agent/issue-42", "42", true, nil)
	f.SetIntegrationTip("42", "integration/42@abc123")
	cf := f.AsLocal()

	res, err := reconcile.Run(f, cf, fakeLiveness{}, capsFor(f, cf), selfScope)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != "42" {
		t.Errorf("Closed = %v, want [42]", res.Closed)
	}
	if len(f.RecordLandingCalls) != 1 || f.RecordLandingCalls[0] != (forge.RecordLandingCall{Num: "42", Landing: "integration/42@abc123"}) {
		t.Errorf("RecordLandingCalls = %v, want one call upgrading to the IntegrationRef", f.RecordLandingCalls)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.State != forge.IssueClosed {
		t.Errorf("State = %v, want IssueClosed", iss.State)
	}
}

// Issue #1819: Run resolves a LandingBranchRef's scope through the injected
// scopeFor callback instead of reaching into forge/local, so reconcile stays
// adapter-agnostic.
func TestRun_UsesInjectedParentResolverForBranchRef(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "agent/issue-42"})
	f.SetLandingContained("agent/issue-42", "custom-parent", true, nil)
	f.SetIntegrationTip("custom-parent", "integration/custom-parent@abc123")
	cf := f.AsLocal()

	res, err := reconcile.Run(f, cf, fakeLiveness{}, capsFor(f, cf), func(num string) forge.SeedScope {
		return forge.NewSeedScope("custom-parent", "integration/custom-parent")
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != "42" {
		t.Errorf("Closed = %v, want [42] -- Run must resolve the scope through the injected callback, not forge/local's own default", res.Closed)
	}
}

// Issue #1809 AC2: a landing that parses as a PR URL but reaches the local
// verification path prints a loud "unverifiable" line rather than passing as
// "not merged yet". Production never records that shape here, but the seam
// allows it.
func TestRun_PrintsUnverifiableForNonLocalLandingShape(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	cf := f.AsLocal()

	out := testutil.CaptureStdout(t, func() {
		res, err := reconcile.Run(f, cf, fakeLiveness{}, capsFor(f, cf), selfScope)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(res.Closed) != 0 {
			t.Errorf("Closed = %v, want none", res.Closed)
		}
	})
	if !strings.Contains(out, "unverifiable") {
		t.Errorf("want a loud unverifiable status line; got: %q", out)
	}
}

// Issue #2151: the local-forge discovery path wraps the agent branch as a
// BranchRef Landing and asks LandingContained. It stays silent when that
// branch is not yet contained, the common case, since most issues with no
// recorded landing genuinely have not landed.
func TestRun_SilentlyLeavesLocalIssueOpenWhenDiscoveredBranchNotContained(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen})
	cf := f.AsLocal()

	res, err := reconcile.Run(f, cf, fakeLiveness{}, capsFor(f, cf), selfScope)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 0 {
		t.Errorf("Closed = %v, want none", res.Closed)
	}
	if len(f.LandingContainedCalls) != 1 {
		t.Errorf("LandingContainedCalls = %v, want exactly 1 (the discovery attempt)", f.LandingContainedCalls)
	}
	if len(f.RecordLandingCalls) != 0 {
		t.Errorf("RecordLandingCalls = %v, want none", f.RecordLandingCalls)
	}
}

// Issue #2151, the no-PR counterpart of
// TestRun_DiscoversMergedLandingByBranchAndCloses: local discovery closes an
// issue with no recorded landing once its agent branch is contained in scope's
// Integration branch, recording the resolved IntegrationTip as the landing.
func TestRun_DiscoversLocalLandingByBranchAndCloses(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen})
	f.BranchPrefix = "agent/issue-"
	branch := f.AgentBranch("42")
	f.SetLandingContained(branch, "42", true, nil)
	f.SetIntegrationTip("42", "integration/42@abc123")
	cf := f.AsLocal()

	res, err := reconcile.Run(f, cf, fakeLiveness{}, capsFor(f, cf), selfScope)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 1 || res.Closed[0] != "42" {
		t.Errorf("Closed = %v, want [42]", res.Closed)
	}
	if len(f.RecordLandingCalls) != 1 || f.RecordLandingCalls[0] != (forge.RecordLandingCall{Num: "42", Landing: "integration/42@abc123"}) {
		t.Errorf("RecordLandingCalls = %v, want one call recording the discovered landing", f.RecordLandingCalls)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.State != forge.IssueClosed {
		t.Errorf("State = %v, want IssueClosed", iss.State)
	}
}

// Mirrors TestRun_SecondSweepIsNoOp for the LandingContained path.
func TestRun_SecondSweepLocalLandingIsNoOp(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "integration/1694@abc123"})
	f.SetLandingContained("integration/1694@abc123", "42", true, nil)
	cf := f.AsLocal()

	if _, err := reconcile.Run(f, cf, fakeLiveness{}, capsFor(f, cf), selfScope); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	res, err := reconcile.Run(f, cf, fakeLiveness{}, capsFor(f, cf), selfScope)
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

// A genuine LandingContained error (a local-git failure) must propagate, not
// be swallowed like the normal contained=false "not landed yet" outcome.
func TestRun_PropagatesLocalLandingContainmentError(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "integration/1694@abc123"})
	wantErr := errors.New("local: repo unreadable")
	f.SetLandingContained("integration/1694@abc123", "42", false, wantErr)
	cf := f.AsLocal()

	_, err := reconcile.Run(f, cf, fakeLiveness{}, capsFor(f, cf), selfScope)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run error = %v, want it to wrap %v", err, wantErr)
	}
	if len(f.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none", f.CloseIssueCalls)
	}
}

// selfScope is a scopeFor stub for fixture issues with no parent frontmatter
// field, mirroring local.ResolveParent's own fallback where a parentless seam
// is its own broad ticket. The SetLandingContained and SetIntegrationTip
// fixtures keyed on an issue's own number then still match.
func selfScope(num string) forge.SeedScope { return forge.NewSeedScope(num, "integration/"+num) }

// fakeLiveness scripts LivenessProbe per issue number. Its zero value never
// triggers a reset by itself: LogStale is false and ContainerLive is (false,
// false), so each test opts in to the death-signal values it asserts against.
type fakeLiveness struct {
	stale     map[string]bool
	live      map[string]bool
	reachable map[string]bool
}

func (f fakeLiveness) LogStale(num string) bool { return f.stale[num] }

func (f fakeLiveness) ContainerLive(num string) (live, reachable bool) {
	return f.live[num], f.reachable[num]
}

// The full composite death signal: no PR or branch for the agent branch, a
// stale Box log, and no live container on a reachable runtime.
func TestRun_ResetsOrphanedInProgressIssue(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	lp := fakeLiveness{
		stale:     map[string]bool{"42": true},
		reachable: map[string]bool{"42": true},
	}

	res, err := reconcile.Run(f, f, lp, capsFor(f, f), selfScope)
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

// An unreachable container runtime is no evidence of a live container, so it
// must not withhold a reset the log and PR signal otherwise support.
func TestRun_ResetsOrphanedInProgressIssue_UnreachableRuntime(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	lp := fakeLiveness{stale: map[string]bool{"42": true}} // reachable defaults to false

	res, err := reconcile.Run(f, f, lp, capsFor(f, f), selfScope)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Reset) != 1 || res.Reset[0] != "42" {
		t.Errorf("Reset = %v, want [42]", res.Reset)
	}
}

// A PR on the agent branch in any state is evidence a runner touched that
// branch, so Reconcile never resets the issue even if the PR later closed
// unmerged.
func TestRun_LeavesInProgressUntouched_WhenPRExistsForBranch(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	f.SetPR(f.AgentBranch("42"), forge.PR{URL: "https://github.com/o/r/pull/9"})
	f.SetPRState("https://github.com/o/r/pull/9", forge.PRClosed)
	lp := fakeLiveness{stale: map[string]bool{"42": true}, reachable: map[string]bool{"42": true}}

	res, err := reconcile.Run(f, f, lp, capsFor(f, f), selfScope)
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

// The die-after-push-before-PR window: Reconcile must not silently
// re-dispatch over a pushed agent branch that has no PR yet.
func TestRun_LeavesInProgressUntouched_WhenBranchExistsNoPR(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	f.SetBranchExists(f.AgentBranch("42"), true)
	lp := fakeLiveness{stale: map[string]bool{"42": true}, reachable: map[string]bool{"42": true}}

	res, err := reconcile.Run(f, f, lp, capsFor(f, f), selfScope)
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

// A Box log that is not stale means a live or recently active Box still owns
// the issue.
func TestRun_LeavesInProgressUntouched_WhenLogFresh(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	lp := fakeLiveness{reachable: map[string]bool{"42": true}} // stale defaults to false

	res, err := reconcile.Run(f, f, lp, capsFor(f, f), selfScope)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Reset) != 0 {
		t.Errorf("Reset = %v, want none — the log is fresh", res.Reset)
	}
}

// A running Box container is the most direct evidence a live runner still owns
// the issue, even with no PR or branch recorded yet and a stale log.
func TestRun_LeavesInProgressUntouched_WhenContainerLive(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	lp := fakeLiveness{
		stale:     map[string]bool{"42": true},
		live:      map[string]bool{"42": true},
		reachable: map[string]bool{"42": true},
	}

	res, err := reconcile.Run(f, f, lp, capsFor(f, f), selfScope)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Reset) != 0 {
		t.Errorf("Reset = %v, want none — the container is still live", res.Reset)
	}
}

// After a reset the issue is Dispatchable, so ListIssues(InProgress) no longer
// returns it and a second sweep does nothing.
func TestRun_ResetIsIdempotent(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	lp := fakeLiveness{stale: map[string]bool{"42": true}, reachable: map[string]bool{"42": true}}

	if _, err := reconcile.Run(f, f, lp, capsFor(f, f), selfScope); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	res, err := reconcile.Run(f, f, lp, capsFor(f, f), selfScope)
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

// ADR 0039 Slice B: Reconcile leaves a Recoverable issue (a stranded
// CODE_FORGE=local push-only run promoted to Recoverable instead of Failed)
// completely untouched. Against a push-only local Code Forge, Run skips its
// only reset mechanism, the second InProgress sweep at its tail, because
// hasPR is false, so this test covers the local landing path itself.
func TestRun_Local_RecoverableIssueNeverReset(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress", Recoverable: "recoverable"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"recoverable"}, Landing: "integration/1694@abc123"})
	f.SetLandingContained("integration/1694@abc123", "42", false, nil)
	cf := f.AsLocal()

	res, err := reconcile.Run(f, cf, fakeLiveness{}, capsFor(f, cf), selfScope)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Closed) != 0 {
		t.Errorf("Closed = %v, want none — a Recoverable issue must never be silently closed", res.Closed)
	}
	if len(res.Reset) != 0 {
		t.Errorf("Reset = %v, want none — Reconcile must never reset a Recoverable issue to Dispatchable", res.Reset)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %v, want none", f.TransitionStateCalls)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.State != forge.IssueOpen {
		t.Errorf("State = %v, want unchanged IssueOpen", iss.State)
	}
	if !slices.Contains(iss.Labels, "recoverable") {
		t.Errorf("Labels = %v, want recoverable label to survive reconcile untouched", iss.Labels)
	}
	if slices.Contains(iss.Labels, "dispatchable") {
		t.Errorf("Labels = %v, must NOT carry dispatchable -- reconcile must never reset Recoverable", iss.Labels)
	}
}

// Reconcile is observational only: a sweep that closes an issue never calls
// Merge, Rebase, EnqueueAutoMerge or MarkReady.
func TestRun_NeverMergesOrPushes(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	if _, err := reconcile.Run(f, f, fakeLiveness{}, capsFor(f, f), selfScope); err != nil {
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
