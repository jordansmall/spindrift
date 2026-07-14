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

func hasLabel(iss forge.Issue, label string) bool {
	for _, l := range iss.Labels {
		if l == label {
			return true
		}
	}
	return false
}
