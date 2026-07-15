package console

import (
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

// TestPickIssue_TransitionErr_ReturnsFailedMsg verifies a promotion that
// races (issue closed, relabeled, or claimed by another loop) surfaces as
// PickFailedMsg with the tracker's error as the reason, rather than a
// silently-queued pick the tracker never actually recorded.
func TestPickIssue_TransitionErr_ReturnsFailedMsg(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing"})
	f.TransitionStateErr = errBoom

	msg := PickIssue(f, "42", "fix the thing", KindWork)

	failed, ok := msg.(PickFailedMsg)
	if !ok {
		t.Fatalf("PickIssue() = %T, want PickFailedMsg", msg)
	}
	if failed.Number != "42" || failed.Reason == "" {
		t.Errorf("PickFailedMsg = %+v, want #42 with a reason", failed)
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

// TestPickIssue_AlreadyInProgress_ReturnsFailedMsg_NoTransition verifies a
// pick on an issue already claimed by a live Box is rejected outright —
// never relabeled Dispatchable on top of its existing InProgress label,
// which would let a second Box's claim succeed for the same issue (#707).
func TestPickIssue_AlreadyInProgress_ReturnsFailedMsg_NoTransition(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-in-progress"}})

	msg := PickIssue(f, "42", "fix the thing", KindWork)

	failed, ok := msg.(PickFailedMsg)
	if !ok {
		t.Fatalf("PickIssue() = %T, want PickFailedMsg", msg)
	}
	if failed.Number != "42" || failed.Reason == "" {
		t.Errorf("PickFailedMsg = %+v, want #42 with a reason", failed)
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

// TestPickIssue_AlreadyComplete_ReturnsFailedMsg_NoTransition mirrors
// TestPickIssue_AlreadyInProgress_ReturnsFailedMsg_NoTransition for the other
// terminal state a stray pick must never relabel out of (#707).
func TestPickIssue_AlreadyComplete_ReturnsFailedMsg_NoTransition(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", Complete: "agent-complete"})
	f.SetIssue(forge.Issue{Number: "42", Title: "fix the thing", Labels: []string{"agent-complete"}})

	msg := PickIssue(f, "42", "fix the thing", KindWork)

	failed, ok := msg.(PickFailedMsg)
	if !ok {
		t.Fatalf("PickIssue() = %T, want PickFailedMsg", msg)
	}
	if failed.Number != "42" || failed.Reason == "" {
		t.Errorf("PickFailedMsg = %+v, want #42 with a reason", failed)
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

func hasLabel(iss forge.Issue, label string) bool {
	for _, l := range iss.Labels {
		if l == label {
			return true
		}
	}
	return false
}
