package console

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestQueue_Discover_EmptyQueue_ReturnsNoIssues verifies Discover — the
// waves.Discoverer this queue backs — returns an empty batch when nothing
// is queued, rather than blocking or erroring.
func TestQueue_Discover_EmptyQueue_ReturnsNoIssues(t *testing.T) {
	q := NewQueue()
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})

	issues, edges, err := q.Discover(f)

	if err != nil || len(issues) != 0 || len(edges) != 0 {
		t.Errorf("Discover() = %v, %v, %v, want no issues", issues, edges, err)
	}
}

// TestQueue_Discover_ClaimsAndReturnsFrontQueuedPick verifies Discover
// performs the atomic Dispatchable->InProgress claim on the front-most
// queued pick, marks it running, and returns it as a single-issue batch —
// the launch half of "queued -> claiming -> running -> settled" (#646).
func TestQueue_Discover_ClaimsAndReturnsFrontQueuedPick(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})

	issues, edges, err := q.Discover(f)

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(issues) != 1 || issues[0].Number != "42" || issues[0].Title != "fix the thing" {
		t.Errorf("issues = %+v, want [{42 fix the thing}]", issues)
	}
	if len(edges) != 0 {
		t.Errorf("edges = %v, want empty", edges)
	}
	if len(f.TransitionStateCalls) != 1 {
		t.Fatalf("TransitionStateCalls = %+v, want one claim call", f.TransitionStateCalls)
	}
	call := f.TransitionStateCalls[0]
	if call.Num != "42" || call.From != forge.Dispatchable || call.To != forge.InProgress {
		t.Errorf("TransitionStateCalls[0] = %+v, want claim 42: Dispatchable->InProgress", call)
	}
	if got := q.Snapshot()[0].State; got != PickRunning {
		t.Errorf("pick state = %v, want running", got)
	}
}

// TestQueue_Discover_RacedClaim_DissolvesAndTriesNext verifies a claim that
// fails (raced by another loop, the issue closed, or relabeled) dissolves
// that pick with the reason and Discover falls through to the next queued
// pick — a stale queue can only produce a failed claim, never a wrong
// dispatch (#646 AC6).
func TestQueue_Discover_RacedClaim_DissolvesAndTriesNext(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "raced", State: PickQueued})
	q.Add(Pick{Number: "43", Title: "next up", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "43", Labels: []string{"ready-for-agent"}})

	issues, _, err := q.Discover(raceOnNum{Fake: f, racedNum: "42"})

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(issues) != 1 || issues[0].Number != "43" {
		t.Errorf("issues = %+v, want #43 (the next queued pick)", issues)
	}

	snap := q.Snapshot()
	if snap[0].State != PickDissolved || snap[0].Reason != errBoom.Error() {
		t.Errorf("pick #42 = %+v, want dissolved with reason %q", snap[0], errBoom.Error())
	}
	if snap[1].State != PickRunning {
		t.Errorf("pick #43 = %+v, want running", snap[1])
	}
}

// raceOnNum wraps a *forge.Fake so TransitionState fails for exactly one
// issue number, simulating another loop winning the claim race for it while
// every other issue's claim still succeeds normally.
type raceOnNum struct {
	*forge.Fake
	racedNum string
}

func (r raceOnNum) TransitionState(num string, from, to forge.DispatchState) error {
	_ = r.Fake.TransitionState(num, from, to) // still records the call
	if num == r.racedNum {
		return errBoom
	}
	return nil
}
