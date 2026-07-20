package main

import (
	"bytes"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/reconcile"
)

// fakeLiveness is a no-op reconcile.LivenessProbe by default: LogStale
// defaults to false (not stale) and ContainerLive always reports live=false,
// so it never triggers a reset on its own — exactly what the Closed-only
// tests in this file need from the seam.
type fakeLiveness struct {
	stale     map[string]bool
	reachable map[string]bool
}

func (f fakeLiveness) LogStale(num string) bool { return f.stale[num] }

func (f fakeLiveness) ContainerLive(num string) (live, reachable bool) {
	return false, f.reachable[num]
}

var _ reconcile.LivenessProbe = fakeLiveness{}

// TestRunReconcile_ClosesMergedLandingIssue verifies runReconcile drives the
// reconcile.Run seam against a local-tracker config and reports the closed
// issue number in its output.
func TestRunReconcile_ClosesMergedLandingIssue(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	var buf bytes.Buffer
	if err := runReconcile(f, f, fakeLiveness{}, "local", &buf); err != nil {
		t.Fatalf("runReconcile: %v", err)
	}
	if !strings.Contains(buf.String(), "42") {
		t.Errorf("want output to mention closed issue 42, got %q", buf.String())
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.State != forge.IssueClosed {
		t.Errorf("State = %v, want IssueClosed", iss.State)
	}
}

// TestRunReconcile_ReportsAbandonedIssue verifies runReconcile reports an
// issue flagged abandoned (its landing PR closed without merging) in its
// output, distinct from a closed issue (ADR 0029).
func TestRunReconcile_ReportsAbandonedIssue(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRClosed)

	var buf bytes.Buffer
	if err := runReconcile(f, f, fakeLiveness{}, "local", &buf); err != nil {
		t.Fatalf("runReconcile: %v", err)
	}
	if !strings.Contains(buf.String(), "abandoned") || !strings.Contains(buf.String(), "42") {
		t.Errorf("want output to mention abandoned issue 42, got %q", buf.String())
	}
}

// TestRunReconcile_NonLocalTrackerIsClearNoOp verifies runReconcile refuses
// cleanly (a plain message, not an error) for github/jira, and never touches
// the forge even when a merged landing PR exists to close against.
func TestRunReconcile_NonLocalTrackerIsClearNoOp(t *testing.T) {
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	var buf bytes.Buffer
	if err := runReconcile(f, f, fakeLiveness{}, "github", &buf); err != nil {
		t.Fatalf("runReconcile: %v", err)
	}
	if !strings.Contains(buf.String(), "nothing to do") {
		t.Errorf("want a clear no-op message, got %q", buf.String())
	}
	if len(f.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none for a github tracker", f.CloseIssueCalls)
	}
}

// TestRunReconcile_ReportsResetIssue verifies runReconcile reports an
// InProgress issue that reconcile.Run reset, alongside the (empty) closed
// report, so an operator running `spindrift reconcile` sees which issues
// came back to Dispatchable.
func TestRunReconcile_ReportsResetIssue(t *testing.T) {
	labels := forge.DispatchLabels{Dispatchable: "dispatchable", InProgress: "in-progress"}
	f := forge.NewFake(labels)
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Labels: []string{"in-progress"}})
	lp := fakeLiveness{stale: map[string]bool{"42": true}, reachable: map[string]bool{"42": true}}

	var buf bytes.Buffer
	if err := runReconcile(f, f, lp, "local", &buf); err != nil {
		t.Fatalf("runReconcile: %v", err)
	}
	if !strings.Contains(buf.String(), "reset 1 issue(s): 42") {
		t.Errorf("want output to report reset issue 42, got %q", buf.String())
	}
}

// --- reconcileAfterDispatch tests (dispatch's local-only auto-invoke) ---

// TestReconcileAfterDispatch_LocalTracker_ClosesMergedLanding verifies a
// dispatch run's final auto-invoke reaches the same reconcile.Run seam
// runReconcile drives, when the tracker is local.
func TestReconcileAfterDispatch_LocalTracker_ClosesMergedLanding(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "local"
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	var buf bytes.Buffer
	if err := reconcileAfterDispatch(c, f, f, fakeLiveness{}, &buf); err != nil {
		t.Fatalf("reconcileAfterDispatch: %v", err)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if iss.State != forge.IssueClosed {
		t.Errorf("State = %v, want IssueClosed", iss.State)
	}
}

// TestReconcileAfterDispatch_NonLocalTracker_SilentNoOp verifies a dispatch
// run's final auto-invoke does nothing — and prints nothing — for a
// github/jira tracker, unlike the standalone `spindrift reconcile` verb's
// explicit refusal message.
func TestReconcileAfterDispatch_NonLocalTracker_SilentNoOp(t *testing.T) {
	c := baseConfig()
	c.issueTracker = "github"
	f := forge.NewFake()
	f.SetIssue(forge.Issue{Number: "42", State: forge.IssueOpen, Landing: "https://github.com/o/r/pull/1"})
	f.SetPRState("https://github.com/o/r/pull/1", forge.PRMerged)

	var buf bytes.Buffer
	if err := reconcileAfterDispatch(c, f, f, fakeLiveness{}, &buf); err != nil {
		t.Fatalf("reconcileAfterDispatch: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("want no output for a non-local tracker, got %q", buf.String())
	}
	if len(f.CloseIssueCalls) != 0 {
		t.Errorf("CloseIssueCalls = %v, want none for a github tracker", f.CloseIssueCalls)
	}
}
