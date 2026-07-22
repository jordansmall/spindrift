package console

import (
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestPickIssue_PromotesAndReturnsQueuedMsg verifies PickIssue promotes num
// through the Untriaged->Dispatchable transition and wraps the result into a
// PickQueuedMsg Update can apply directly — the Pick record's kind defaults
// to work when the caller doesn't override it (#646 AC7).
func TestPickIssue_PromotesAndReturnsQueuedMsg(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing"})

	msg := PickIssue(f, "42", "fix the thing", KindWork)

	queued, ok := msg.(PickQueuedMsg)
	if !ok {
		t.Fatalf("PickIssue() = %T, want PickQueuedMsg", msg)
	}
	if queued.Number != "42" || queued.Title != "fix the thing" || queued.Kind != KindWork {
		t.Errorf("PickQueuedMsg = %+v, want {42 fix the thing work}", queued)
	}

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatal(err)
	}
	if !hasLabel(iss, "ready-for-agent") {
		t.Errorf("issue #42 labels = %v, want ready-for-agent promoted onto it", iss.Labels)
	}
}

// TestPickIssue_TransitionErr_ReturnsDissolvedMsg verifies a promotion that
// races (issue closed, relabeled, or claimed by another loop) surfaces as
// PickDissolvedMsg with the tracker's error as the reason, rather than a
// silently-queued pick the tracker never actually recorded.
func TestPickIssue_TransitionErr_ReturnsDissolvedMsg(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing"})
	f.TransitionStateErr = errBoom

	msg := PickIssue(f, "42", "fix the thing", KindWork)

	dissolved, ok := msg.(PickDissolvedMsg)
	if !ok {
		t.Fatalf("PickIssue() = %T, want PickDissolvedMsg", msg)
	}
	if dissolved.Number != "42" || dissolved.Reason == "" {
		t.Errorf("PickDissolvedMsg = %+v, want #42 with a reason", dissolved)
	}
}

// TestPickIssue_LeavesIssueDispatchable_NeverInProgress verifies a pick
// stops at the promotion step — the issue is Dispatchable, never
// InProgress, until something actually claims and launches it (#646 AC3).
func TestPickIssue_LeavesIssueDispatchable_NeverInProgress(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing"})

	PickIssue(f, "42", "fix the thing", KindWork)

	iss, err := f.Issue("42")
	if err != nil {
		t.Fatal(err)
	}
	if hasLabel(iss, "agent-in-progress") {
		t.Errorf("issue #42 labels = %v, want no in-progress label from a pick alone", iss.Labels)
	}
}

// TestPickIssue_AlreadyInProgress_ReturnsDissolvedMsg_NoTransition verifies a
// pick on an issue already claimed by a live Box is rejected outright —
// never relabeled Dispatchable on top of its existing InProgress label,
// which would let a second Box's claim succeed for the same issue (#707).
func TestPickIssue_AlreadyInProgress_ReturnsDissolvedMsg_NoTransition(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-in-progress"}})

	msg := PickIssue(f, "42", "fix the thing", KindWork)

	dissolved, ok := msg.(PickDissolvedMsg)
	if !ok {
		t.Fatalf("PickIssue() = %T, want PickDissolvedMsg", msg)
	}
	const wantReason = "issue #42 is already in progress"
	if dissolved.Number != "42" || dissolved.Reason != wantReason {
		t.Errorf("PickDissolvedMsg = %+v, want #42 with reason %q", dissolved, wantReason)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none — an InProgress issue must never be relabeled", f.TransitionStateCalls)
	}
	iss, err := f.Issue("42")
	if err != nil {
		t.Fatal(err)
	}
	if hasLabel(iss, "ready-for-agent") {
		t.Errorf("issue #42 labels = %v, want ready-for-agent never added", iss.Labels)
	}
}

// TestPickIssue_AlreadyComplete_ReturnsDissolvedMsg_NoTransition mirrors
// TestPickIssue_AlreadyInProgress_ReturnsDissolvedMsg_NoTransition for the other
// terminal state a stray pick must never relabel out of (#707).
func TestPickIssue_AlreadyComplete_ReturnsDissolvedMsg_NoTransition(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", Complete: "agent-complete"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-complete"}})

	msg := PickIssue(f, "42", "fix the thing", KindWork)

	dissolved, ok := msg.(PickDissolvedMsg)
	if !ok {
		t.Fatalf("PickIssue() = %T, want PickDissolvedMsg", msg)
	}
	const wantReason = "issue #42 is already complete"
	if dissolved.Number != "42" || dissolved.Reason != wantReason {
		t.Errorf("PickDissolvedMsg = %+v, want #42 with reason %q", dissolved, wantReason)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none — a Complete issue must never be relabeled", f.TransitionStateCalls)
	}
	iss, err := f.Issue("42")
	if err != nil {
		t.Fatal(err)
	}
	if hasLabel(iss, "ready-for-agent") {
		t.Errorf("issue #42 labels = %v, want ready-for-agent never added", iss.Labels)
	}
}

// TestPickIssue_ClosedIssue_ReturnsDissolvedMsg_NoTransition verifies a pick
// on an issue GitHub already closed is rejected outright, even when it still
// carries no dispatch label — a closed issue must never be promoted onto the
// dispatch lifecycle just because a dispatch label was never cleaned up
// (#1851).
func TestPickIssue_ClosedIssue_ReturnsDissolvedMsg_NoTransition(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", State: forge.IssueClosed})

	msg := PickIssue(f, "42", "fix the thing", KindWork)

	dissolved, ok := msg.(PickDissolvedMsg)
	if !ok {
		t.Fatalf("PickIssue() = %T, want PickDissolvedMsg", msg)
	}
	const wantReason = "issue #42 is already closed"
	if dissolved.Number != "42" || dissolved.Reason != wantReason {
		t.Errorf("PickDissolvedMsg = %+v, want #42 with reason %q", dissolved, wantReason)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none — a closed issue must never be relabeled", f.TransitionStateCalls)
	}
}

// TestPickIssue_IssueLookupErr_ReturnsDissolvedMsg verifies a tracker.Issue
// failure (network fault, deleted issue) surfaces as a PickDissolvedMsg
// carrying the tracker's error, rather than falling through to the
// InProgress/Complete checks with a zero-value Issue.
func TestPickIssue_IssueLookupErr_ReturnsDissolvedMsg(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.IssueErr = errBoom

	msg := PickIssue(f, "42", "fix the thing", KindWork)

	dissolved, ok := msg.(PickDissolvedMsg)
	if !ok {
		t.Fatalf("PickIssue() = %T, want PickDissolvedMsg", msg)
	}
	if dissolved.Number != "42" || dissolved.Reason != errBoom.Error() {
		t.Errorf("PickDissolvedMsg = %+v, want #42 with reason %q", dissolved, errBoom.Error())
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none — a failed lookup must never allow a pick through", f.TransitionStateCalls)
	}
}

// TestPickIssue_ResearchKind_UntriagedIssue_ReturnsQueuedMsg verifies a
// KindResearch pick on an untriaged Backlog issue queues instead of
// dissolving (#1742). Research's DispatchLabels leaves Complete unmapped
// (ADR 0022: a research dispatch reaches Complete via verdict labels, not
// a Complete label) — the double-box guard must recognize an unmapped
// state as "never present" rather than querying it and false-matching
// every open issue, which used to reject every research pick with a bogus
// "already complete" reason.
func TestPickIssue_ResearchKind_UntriagedIssue_ReturnsQueuedMsg(t *testing.T) {
	f := forge.NewFake(forge.ResearchDispatchLabels())
	f.SetIssue(forge.Issue{Number: "42", Title: "worth researching"})

	msg := PickIssue(f, "42", "worth researching", KindResearch)

	queued, ok := msg.(PickQueuedMsg)
	if !ok {
		t.Fatalf("PickIssue() = %+v, want PickQueuedMsg", msg)
	}
	if queued.Number != "42" || queued.Kind != KindResearch {
		t.Errorf("PickQueuedMsg = %+v, want {42 ... research}", queued)
	}
}

// TestPickAllReady_ReturnsOneMsgPerCurrentlyDispatchableIssue verifies
// PickAllReady picks exactly the issues currently Dispatchable on the
// tracker, and nothing else — an issue with no dispatch label yet is left
// alone (#647 AC3).
func TestPickAllReady_ReturnsOneMsgPerCurrentlyDispatchableIssue(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "43", Title: "also ready", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "44", Title: "not triaged yet"})

	msgs := PickAllReady(f)

	if len(msgs) != 2 {
		t.Fatalf("PickAllReady() = %+v, want 2 msgs (only the Dispatchable issues)", msgs)
	}
	first, ok := msgs[0].(PickQueuedMsg)
	if !ok || first.Number != "42" {
		t.Errorf("msgs[0] = %+v, want PickQueuedMsg for #42", msgs[0])
	}
	second, ok := msgs[1].(PickQueuedMsg)
	if !ok || second.Number != "43" {
		t.Errorf("msgs[1] = %+v, want PickQueuedMsg for #43", msgs[1])
	}
}

// TestPickAllReady_ListIssuesErr_ReturnsDissolvedMsg verifies a ListIssues
// failure surfaces to the operator as a PickDissolvedMsg instead of a silently
// dropped nil — the asymmetry with PickIssue's error handling (#728).
func TestPickAllReady_ListIssuesErr_ReturnsDissolvedMsg(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.ListIssuesErr = errBoom

	msgs := PickAllReady(f)

	if len(msgs) != 1 {
		t.Fatalf("PickAllReady() = %+v, want 1 msg", msgs)
	}
	dissolved, ok := msgs[0].(PickDissolvedMsg)
	if !ok || dissolved.Reason != errBoom.Error() {
		t.Errorf("msgs[0] = %+v, want PickDissolvedMsg with reason %q", msgs[0], errBoom.Error())
	}
}

// TestPickAllReady_MakesExactlyOneListIssuesCall verifies the bulk pick skips
// PickIssue's per-issue terminal-state re-verification — every issue in the
// loop already came from the Dispatchable snapshot, so InProgress/Complete
// are guaranteed false (#707's mutual-exclusivity contract) and re-checking
// them wastes 2 ListIssues round-trips per issue for nothing (#987).
func TestPickAllReady_MakesExactlyOneListIssuesCall(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "43", Title: "also ready", Labels: []string{"ready-for-agent"}})

	msgs := PickAllReady(f)

	if len(msgs) != 2 || msgs[0] == nil || msgs[1] == nil {
		t.Fatalf("PickAllReady() = %+v, want 2 msgs (both issues still picked, not just skipped)", msgs)
	}
	if len(f.TransitionStateCalls) != 2 {
		t.Errorf("TransitionStateCalls = %+v, want 2 (both issues still promoted)", f.TransitionStateCalls)
	}
	if len(f.ListIssuesCalls) != 1 {
		t.Errorf("ListIssuesCalls = %+v, want exactly 1 (the Dispatchable snapshot, no redundant per-issue re-verification)", f.ListIssuesCalls)
	}
}

// TestPickIssue_InProgressListAtPageLimit_TargetMissing_FailsSafe verifies
// that when the InProgress list issueInState consults hits ResultPageLimit
// and num isn't in the page, PickIssue fails safe (dissolves the pick)
// instead of concluding num isn't InProgress and re-opening the #707
// double-box hole — a real ListIssues page cap could be hiding num beyond
// the boundary.
func TestPickIssue_InProgressListAtPageLimit_TargetMissing_FailsSafe(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing"})
	for i := 0; i < forge.ResultPageLimit; i++ {
		f.SetIssue(forge.Issue{Number: strconv.Itoa(1000 + i), Title: "other", Labels: []string{"agent-in-progress"}})
	}

	msg := PickIssue(f, "42", "fix the thing", KindWork)

	dissolved, ok := msg.(PickDissolvedMsg)
	if !ok {
		t.Fatalf("PickIssue() = %T, want PickDissolvedMsg", msg)
	}
	if dissolved.Number != "42" || !strings.Contains(dissolved.Reason, "truncated") {
		t.Errorf("PickDissolvedMsg = %+v, want #42 with a truncation reason", dissolved)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none — a page-limited list must never allow a pick through", f.TransitionStateCalls)
	}
}

// TestPickIssue_CompleteListAtPageLimit_TargetMissing_FailsSafe mirrors
// TestPickIssue_InProgressListAtPageLimit_TargetMissing_FailsSafe for the
// Complete check issueInState runs second — a page-limited Complete list
// must fail safe exactly like a page-limited InProgress one.
func TestPickIssue_CompleteListAtPageLimit_TargetMissing_FailsSafe(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", Complete: "agent-complete"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing"})
	for i := 0; i < forge.ResultPageLimit; i++ {
		f.SetIssue(forge.Issue{Number: strconv.Itoa(1000 + i), Title: "other", Labels: []string{"agent-complete"}})
	}

	msg := PickIssue(f, "42", "fix the thing", KindWork)

	dissolved, ok := msg.(PickDissolvedMsg)
	if !ok {
		t.Fatalf("PickIssue() = %T, want PickDissolvedMsg", msg)
	}
	if dissolved.Number != "42" || !strings.Contains(dissolved.Reason, "truncated") {
		t.Errorf("PickDissolvedMsg = %+v, want #42 with a truncation reason", dissolved)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none — a page-limited list must never allow a pick through", f.TransitionStateCalls)
	}
}

// TestPickIssue_TargetFoundWithinFullPage_ReturnsDissolvedMsg verifies a full
// page never shadows num when it IS on that page — the fail-safe error only
// fires on a miss, so a match found within a page at the cap still reports
// InProgress correctly, same as a match on a small page.
func TestPickIssue_TargetFoundWithinFullPage_ReturnsDissolvedMsg(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-in-progress"}})
	for i := 0; i < forge.ResultPageLimit-1; i++ {
		f.SetIssue(forge.Issue{Number: strconv.Itoa(1000 + i), Title: "other", Labels: []string{"agent-in-progress"}})
	}

	msg := PickIssue(f, "42", "fix the thing", KindWork)

	dissolved, ok := msg.(PickDissolvedMsg)
	if !ok {
		t.Fatalf("PickIssue() = %T, want PickDissolvedMsg — num is on the full page and already InProgress", msg)
	}
	if dissolved.Number != "42" || dissolved.Reason == "" {
		t.Errorf("PickDissolvedMsg = %+v, want #42 with a reason", dissolved)
	}
}

func hasLabel(iss forge.Issue, label string) bool {
	for _, l := range iss.Labels {
		if l == label {
			return true
		}
	}
	return false
}
