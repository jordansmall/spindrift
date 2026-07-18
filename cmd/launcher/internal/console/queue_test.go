package console

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestQueue_Discover_EmptyQueue_ReturnsNoIssues verifies Discover — the
// waves.Discoverer this queue backs — returns an empty batch when nothing
// is queued, rather than blocking or erroring.
func TestQueue_Discover_EmptyQueue_ReturnsNoIssues(t *testing.T) {
	q := NewQueue()
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})

	issues, edges, _, err := q.Discover(f, f, "")

	if err != nil || len(issues) != 0 || len(edges) != 0 {
		t.Errorf("Discover() = %v, %v, %v, want no issues", issues, edges, err)
	}
}

// TestQueue_Discover_SourcesNilAcrossPaths verifies the claim-success path
// and the no-launchable-candidate fallback path return the same nil-ness
// for sources (and edges), so a caller can't observe a spurious empty-vs-nil
// distinction between the two (#903).
func TestQueue_Discover_SourcesNilAcrossPaths(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})

	empty := NewQueue()
	_, emptyEdges, emptySources, err := empty.Discover(f, f, "")
	if err != nil {
		t.Fatalf("Discover (empty queue): %v", err)
	}
	if emptySources != nil {
		t.Errorf("empty queue sources = %#v, want nil", emptySources)
	}
	if emptyEdges != nil {
		t.Errorf("empty queue edges = %#v, want nil", emptyEdges)
	}

	claimed := NewQueue()
	claimed.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})
	claimedIssues, claimedEdges, claimedSources, err := claimed.Discover(f, f, "")
	if err != nil {
		t.Fatalf("Discover (claim success): %v", err)
	}
	if len(claimedIssues) != 1 {
		t.Fatalf("claim-success issues = %v, want one claimed issue", claimedIssues)
	}
	if claimedSources != nil {
		t.Errorf("claim-success sources = %#v, want nil", claimedSources)
	}
	if claimedEdges != nil {
		t.Errorf("claim-success edges = %#v, want nil", claimedEdges)
	}
}

// TestQueue_Empty reports whether the queue has any pick still eligible to
// launch (PickQueued or PickHeld) — the predicate tryLaunch (launcher.go)
// gates its drain spawn on (#754). A held pick counts as non-empty: its
// blocker may have cleared out-of-band, so it still needs a launch attempt
// (#650), unlike hasQueued which only reports PickQueued.
func TestQueue_Empty(t *testing.T) {
	tests := []struct {
		name  string
		picks []Pick
		want  bool
	}{
		{name: "no picks", picks: nil, want: true},
		{name: "queued pick", picks: []Pick{{Number: "42", State: PickQueued}}, want: false},
		{name: "held pick", picks: []Pick{{Number: "42", State: PickHeld}}, want: false},
		{name: "only settled pick", picks: []Pick{{Number: "42", State: PickSettled}}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := NewQueue()
			for _, p := range tt.picks {
				q.Add(p)
			}
			if got := q.Empty(); got != tt.want {
				t.Errorf("Empty() = %v, want %v", got, tt.want)
			}
		})
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

	issues, edges, _, err := q.Discover(f, f, "")

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

	issues, _, _, err := q.Discover(raceOnNum{Fake: f, racedNum: "42"}, f, "")

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

// TestQueue_Discover_DuplicateNumber_ClaimTargetsNewestRow verifies that
// when two picks share the same issue number (e.g. an old PickTerminated row
// ADR 0024's Terminate left behind, plus a fresh re-pick queued after it),
// Discover's claim updates the newest (most recently added) row to
// PickRunning, not the stale terminal one — a re-pick must actually track
// the new Dispatch, not silently corrupt an already-finished row while
// leaving the real claim stuck at PickClaiming forever.
func TestQueue_Discover_DuplicateNumber_ClaimTargetsNewestRow(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickTerminated, Reason: "terminated by operator"})
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})

	issues, _, _, err := q.Discover(f, f, "")

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(issues) != 1 || issues[0].Number != "42" {
		t.Fatalf("issues = %+v, want #42", issues)
	}

	snap := q.Snapshot()
	if snap[0].State != PickTerminated {
		t.Errorf("old row = %+v, want it left untouched at PickTerminated", snap[0])
	}
	if snap[1].State != PickRunning {
		t.Errorf("new row = %+v, want it to become PickRunning (the actual claim)", snap[1])
	}
}

// TestQueue_Discover_HoldsPickWithOpenBlocker verifies a pick whose declared
// blocker is not yet ready holds at PickHeld instead of launching — edge
// resolution reuses waves.BuildEdges/BlockerStatus, no second parser (#650).
func TestQueue_Discover_HoldsPickWithOpenBlocker(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress", Failed: "agent-failed"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "41", State: forge.IssueOpen})
	f.NativeDeps = map[string][]string{"42": {"41"}}

	issues, _, _, err := q.Discover(f, f, "agent-failed")

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("issues = %+v, want none (held)", issues)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none — a held pick never claims", f.TransitionStateCalls)
	}
	snap := q.Snapshot()[0]
	if snap.State != PickHeld {
		t.Errorf("state = %v, want held", snap.State)
	}
	if !strings.Contains(snap.BlockedBy, "#41") {
		t.Errorf("BlockedBy = %q, want it to name #41", snap.BlockedBy)
	}
	iss, err := f.Issue("42")
	if err != nil {
		t.Fatal(err)
	}
	if !hasLabel(iss, "ready-for-agent") {
		t.Errorf("issue #42 labels = %v, want ready-for-agent (still Dispatchable while held)", iss.Labels)
	}
}

// TestQueue_Discover_FailedBlockerSurfacedPickStaysHeld verifies a blocker
// that lands Failed is surfaced on the held row (Reason) rather than
// dissolving the pick — the Console never auto-unpicks; the operator
// decides whether to wait or unpick (#650).
func TestQueue_Discover_FailedBlockerSurfacedPickStaysHeld(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress", Failed: "agent-failed"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "41", State: forge.IssueOpen, Labels: []string{"agent-failed"}})
	f.NativeDeps = map[string][]string{"42": {"41"}}

	issues, _, _, err := q.Discover(f, f, "agent-failed")

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("issues = %+v, want none (still held)", issues)
	}
	snap := q.Snapshot()[0]
	if snap.State != PickHeld {
		t.Errorf("state = %v, want held, not dissolved — the Console never auto-unpicks", snap.State)
	}
	if !strings.Contains(snap.Reason, "#41") || !strings.Contains(snap.Reason, "failed") {
		t.Errorf("Reason = %q, want it to name #41 as a failed blocker", snap.Reason)
	}
	if !strings.Contains(snap.BlockedBy, "#41") {
		t.Errorf("BlockedBy = %q, want it to also name #41 — View, not setHeld, is responsible for deduplicating the two (issue #755)", snap.BlockedBy)
	}
}

// TestQueue_Discover_UnpickDuringClaimCheck_NeverLaunches verifies an Unpick
// that lands in the window between Discover reading a pick as a candidate
// and claiming it never lets that claim through — Unpick's "zero Issue
// Tracker calls, never launches" guarantee holds even when it races
// Discover's own blocker-readiness check (#650).
func TestQueue_Discover_UnpickDuringClaimCheck_NeverLaunches(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})

	issues, _, _, err := q.Discover(removeOnDepsOf{Fake: f, q: q, num: "42"}, f, "")

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("issues = %+v, want none — the pick was unpicked mid-check", issues)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none — an unpicked issue is never claimed", f.TransitionStateCalls)
	}
	if snap := q.Snapshot(); len(snap) != 0 {
		t.Errorf("Snapshot = %+v, want empty — Remove already dropped #42", snap)
	}
}

// TestQueue_Discover_HoldsPickOnDepsOfFailure verifies a pick whose DepsOf
// call fails holds at PickHeld with a reason distinguishing it from a real
// open blocker, rather than launching on a transient tracker hiccup (rate
// limit, timeout, flaky API call) — #752.
func TestQueue_Discover_HoldsPickOnDepsOfFailure(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})

	issues, _, _, err := q.Discover(failDepsOf{Fake: f, num: "42"}, f, "")

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("issues = %+v, want none — a DepsOf failure must hold, not launch", issues)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none — a DepsOf failure must never claim", f.TransitionStateCalls)
	}
	snap := q.Snapshot()[0]
	if snap.State != PickHeld {
		t.Errorf("state = %v, want held", snap.State)
	}
	if !strings.Contains(snap.Reason, "retry") {
		t.Errorf("Reason = %q, want it to explain the pick will be retried", snap.Reason)
	}
}

// TestQueue_Discover_HoldsPickOnDepsOfFailureWithRealBlocker verifies the
// hold fires even when the pick has a real, registered blocker that a
// healthy DepsOf call would have surfaced — proving the failure path holds
// because DepsOf errored, not merely because the pick happens to have zero
// blockers — #1104.
func TestQueue_Discover_HoldsPickOnDepsOfFailureWithRealBlocker(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "41", State: forge.IssueOpen})
	f.NativeDeps = map[string][]string{"42": {"41"}}

	issues, _, _, err := q.Discover(failDepsOf{Fake: f, num: "42"}, f, "")

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(issues) != 0 {
		t.Errorf("issues = %+v, want none — DepsOf failure must hold even with a real blocker registered", issues)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none — a DepsOf failure must never claim", f.TransitionStateCalls)
	}
	snap := q.Snapshot()[0]
	if snap.State != PickHeld {
		t.Errorf("state = %v, want held", snap.State)
	}
	if !strings.Contains(snap.Reason, "retry") {
		t.Errorf("Reason = %q, want it to explain the pick will be retried, not name #41 as an open blocker", snap.Reason)
	}
	if strings.Contains(snap.Reason, "41") {
		t.Errorf("Reason = %q, want no mention of #41 — this hold is about the DepsOf failure, not the blocker", snap.Reason)
	}
}

// failDepsOf wraps a *forge.Fake so DepsOf errors for num, simulating a
// transient tracker failure.
type failDepsOf struct {
	*forge.Fake
	num string
}

func (r failDepsOf) DepsOf(num string) ([]forge.Dependency, error) {
	if num == r.num {
		return nil, errBoom
	}
	return r.Fake.DepsOf(num)
}

// removeOnDepsOf wraps a *forge.Fake so its first DepsOf call for num
// synchronously Removes that pick from q — simulating an operator's Unpick
// landing in Discover's window between reading a pick as a candidate and
// claiming it.
type removeOnDepsOf struct {
	*forge.Fake
	q   *Queue
	num string
}

func (r removeOnDepsOf) DepsOf(num string) ([]forge.Dependency, error) {
	if num == r.num {
		r.q.Remove(num)
	}
	return r.Fake.DepsOf(num)
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
