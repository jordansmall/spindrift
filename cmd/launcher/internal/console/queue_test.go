package console

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// Discover returns an empty batch when nothing is queued, rather than
// blocking or erroring.
func TestQueue_Discover_EmptyQueue_ReturnsNoIssues(t *testing.T) {
	q := NewQueue()
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})

	batch, err := q.Discover(f, f, "", KindWork)

	if err != nil || len(batch.Issues) != 0 || len(batch.Edges) != 0 {
		t.Errorf("Discover() = %v, %v, want no issues", batch, err)
	}
}

// The claim-success path and the no-launchable-candidate fallback path
// return the same nil-ness for sources, edges, and failed, so a caller
// cannot observe a spurious empty-vs-nil distinction (#903). Failed stays
// nil on both paths because Discover holds a pick whose DepsOf call fails
// rather than reporting it in Failed.
func TestQueue_Discover_BlockerFieldsNilAcrossPaths(t *testing.T) {
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})

	empty := NewQueue()
	emptyBatch, err := empty.Discover(f, f, "", KindWork)
	if err != nil {
		t.Fatalf("Discover (empty queue): %v", err)
	}
	if emptyBatch.Sources != nil {
		t.Errorf("empty queue sources = %#v, want nil", emptyBatch.Sources)
	}
	if emptyBatch.Edges != nil {
		t.Errorf("empty queue edges = %#v, want nil", emptyBatch.Edges)
	}
	if emptyBatch.Failed != nil {
		t.Errorf("empty queue failed = %#v, want nil", emptyBatch.Failed)
	}

	claimed := NewQueue()
	claimed.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})
	claimedBatch, err := claimed.Discover(f, f, "", KindWork)
	if err != nil {
		t.Fatalf("Discover (claim success): %v", err)
	}
	if len(claimedBatch.Issues) != 1 {
		t.Fatalf("claim-success issues = %v, want one claimed issue", claimedBatch.Issues)
	}
	if claimedBatch.Sources != nil {
		t.Errorf("claim-success sources = %#v, want nil", claimedBatch.Sources)
	}
	if claimedBatch.Edges != nil {
		t.Errorf("claim-success edges = %#v, want nil", claimedBatch.Edges)
	}
	if claimedBatch.Failed != nil {
		t.Errorf("claim-success failed = %#v, want nil", claimedBatch.Failed)
	}
}

// Queue.Discover wires cfg.SeedScopeOf from localloop.SeedScopeResolver
// (queue.go, #2135), so the seed-branch containment query runs at the
// queue-driven readiness path only when the forge implements
// forge.LandingContainmentQuery. Under a plain forge the resolver returns
// nil, SeedScopeOf stays nil, and blockerReady has no scope to check against.
func TestQueue_Discover_WiresSeedScopeContainmentOnlyUnderLocalForge(t *testing.T) {
	const landing = "integration/render-pipeline@abc123"

	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress", Failed: "agent-failed"})
	f.SetIssue(forge.Issue{Number: "41", Title: "first", State: forge.IssueOpen, Landing: landing})
	f.SetIssue(forge.Issue{Number: "42", Title: "then", Labels: []string{"ready-for-agent"}, Parent: "Render Pipeline"})
	f.NativeDeps = map[string][]string{"42": {"41"}}
	f.SetLandingContained(landing, "render-pipeline", true, nil)

	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "then", State: PickQueued})

	batch, err := q.Discover(f, f.AsLocal(), "agent-failed", KindWork)
	if err != nil {
		t.Fatalf("Discover (local forge): %v", err)
	}
	if len(f.LandingContainedCalls) == 0 {
		t.Fatal("LandingContainedCalls is empty, want the seed-branch containment query to have run under a local forge -- SeedScopeOf was not wired")
	}
	if len(batch.Issues) != 1 || batch.Issues[0].Number != "42" {
		t.Fatalf("Discover (local forge) issues = %v, want #42 claimed once its blocker's landing reads contained", batch.Issues)
	}

	f2 := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress", Failed: "agent-failed"})
	f2.SetIssue(forge.Issue{Number: "41", Title: "first", State: forge.IssueOpen, Landing: landing})
	f2.SetIssue(forge.Issue{Number: "42", Title: "then", Labels: []string{"ready-for-agent"}, Parent: "Render Pipeline"})
	f2.NativeDeps = map[string][]string{"42": {"41"}}
	f2.SetLandingContained(landing, "render-pipeline", true, nil)

	q2 := NewQueue()
	q2.Add(Pick{Number: "42", Title: "then", State: PickQueued})

	if _, err := q2.Discover(f2, f2, "agent-failed", KindWork); err != nil {
		t.Fatalf("Discover (plain forge): %v", err)
	}
	if len(f2.LandingContainedCalls) != 0 {
		t.Errorf("LandingContainedCalls = %v, want none -- SeedScopeOf must stay nil under a forge that isn't LandingContainmentQuery-shaped", f2.LandingContainedCalls)
	}
}

// Empty reports false while any pick is still eligible to launch (PickQueued
// or PickHeld); tryLaunch in launcher.go gates its drain spawn on that
// predicate (#754). Queue.Empty's doc comment (#650) says why a held pick
// counts as non-empty, unlike hasQueued.
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

// PendingCount (#2678) counts only PickQueued picks whose effectiveKind
// matches: a held pick still has unsatisfied blockers, and running, settled,
// dissolved, terminated, or failed picks are never pending. It is a pure read
// with no claim side effect, which is why runContinuousQueue (#2939) reads it
// for the stale-drain report's heldBack number instead of calling Discover.
func TestQueue_PendingCount_CountsQueuedOnlyOfMatchingKind(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "1", State: PickQueued, Kind: KindWork})
	q.Add(Pick{Number: "2", State: PickHeld, Kind: KindWork})
	q.Add(Pick{Number: "3", State: PickRunning, Kind: KindWork})
	q.Add(Pick{Number: "4", State: PickSettled, Kind: KindWork})
	q.Add(Pick{Number: "5", State: PickQueued, Kind: KindResearch})
	q.Add(Pick{Number: "6", State: PickHeld, Kind: KindResearch})

	if got := q.PendingCount(KindWork); got != 1 {
		t.Errorf("PendingCount(KindWork) = %d, want 1 (queued #1 only; held #2 not ready)", got)
	}
	if got := q.PendingCount(KindResearch); got != 1 {
		t.Errorf("PendingCount(KindResearch) = %d, want 1 (queued #5 only; held #6 not ready)", got)
	}
}

// Discover performs the atomic Dispatchable to InProgress claim on the
// front-most queued pick, marks it running, and returns it as a single-issue
// batch: the launch half of the queued, claiming, running, settled
// progression (#646).
func TestQueue_Discover_ClaimsAndReturnsFrontQueuedPick(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})

	batch, err := q.Discover(f, f, "", KindWork)

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(batch.Issues) != 1 || batch.Issues[0].Number != "42" || batch.Issues[0].Title != "fix the thing" {
		t.Errorf("issues = %+v, want [{42 fix the thing}]", batch.Issues)
	}
	if len(batch.Edges) != 0 {
		t.Errorf("edges = %v, want empty", batch.Edges)
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

// A claim that fails (raced by another loop, the issue closed or relabeled)
// dissolves that pick with the reason and Discover falls through to the next
// queued pick, so a stale queue can only produce a failed claim, never a
// wrong dispatch (#646 AC6).
func TestQueue_Discover_RacedClaim_DissolvesAndTriesNext(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "raced", State: PickQueued})
	q.Add(Pick{Number: "43", Title: "next up", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "43", Labels: []string{"ready-for-agent"}})

	batch, err := q.Discover(raceOnNum{Fake: f, racedNum: "42"}, f, "", KindWork)

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(batch.Issues) != 1 || batch.Issues[0].Number != "43" {
		t.Errorf("issues = %+v, want #43 (the next queued pick)", batch.Issues)
	}

	snap := q.Snapshot()
	if snap[0].State != PickDissolved || snap[0].Reason != errBoom.Error() {
		t.Errorf("pick #42 = %+v, want dissolved with reason %q", snap[0], errBoom.Error())
	}
	if snap[1].State != PickRunning {
		t.Errorf("pick #43 = %+v, want running", snap[1])
	}
}

// When two picks share an issue number (a PickTerminated row ADR 0024's
// Terminate left behind, plus a fresh re-pick queued after it), the claim
// must update the newest row, or it corrupts an already-finished row and
// leaves the real claim stuck at PickClaiming forever.
func TestQueue_Discover_DuplicateNumber_ClaimTargetsNewestRow(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickTerminated, Reason: "terminated by operator"})
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})

	batch, err := q.Discover(f, f, "", KindWork)

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(batch.Issues) != 1 || batch.Issues[0].Number != "42" {
		t.Fatalf("issues = %+v, want #42", batch.Issues)
	}

	snap := q.Snapshot()
	if snap[0].State != PickTerminated {
		t.Errorf("old row = %+v, want it left untouched at PickTerminated", snap[0])
	}
	if snap[1].State != PickRunning {
		t.Errorf("new row = %+v, want it to become PickRunning (the actual claim)", snap[1])
	}
}

// The console's per-kind drain (#1708) shares one Queue, so a work-kind
// Discover must never claim a research-kind pick's tracker transition, or
// vice versa: the two kinds' claims belong on different tracker instances
// with different label families.
func TestQueue_Discover_SkipsPickOfOtherKind(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "research this", State: PickQueued, Kind: KindResearch})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})

	batch, err := q.Discover(f, f, "", KindWork)

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(batch.Issues) != 0 {
		t.Errorf("issues = %+v, want none — the queued pick is research-kind, not work", batch.Issues)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none — a different-kind pick must never claim", f.TransitionStateCalls)
	}
	if got := q.Snapshot()[0].State; got != PickQueued {
		t.Errorf("pick state = %v, want left at PickQueued", got)
	}
}

// A pick whose declared blocker is not yet ready holds at PickHeld instead of
// launching. Edge resolution reuses waves.NewReadiness/Status, with no second
// parser (#650).
func TestQueue_Discover_HoldsPickWithOpenBlocker(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress", Failed: "agent-failed"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "41", State: forge.IssueOpen})
	f.NativeDeps = map[string][]string{"42": {"41"}}

	batch, err := q.Discover(f, f, "agent-failed", KindWork)

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(batch.Issues) != 0 {
		t.Errorf("issues = %+v, want none (held)", batch.Issues)
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

// A blocker that lands Failed is surfaced on the held row (Reason) rather
// than dissolving the pick. The Console never auto-unpicks; the operator
// decides whether to wait or unpick (#650).
func TestQueue_Discover_FailedBlockerSurfacedPickStaysHeld(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress", Failed: "agent-failed"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "41", State: forge.IssueOpen, Labels: []string{"agent-failed"}})
	f.NativeDeps = map[string][]string{"42": {"41"}}

	batch, err := q.Discover(f, f, "agent-failed", KindWork)

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(batch.Issues) != 0 {
		t.Errorf("issues = %+v, want none (still held)", batch.Issues)
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

// setHeld builds the failed-blocker Reason from the same blockerFailedPrefix
// constant View's dedup guard checks against, so a later format change cannot
// drift the two apart silently (#1111).
func TestQueue_Discover_FailedBlockerReasonUsesSharedPrefix(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress", Failed: "agent-failed"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "41", State: forge.IssueOpen, Labels: []string{"agent-failed"}})
	f.NativeDeps = map[string][]string{"42": {"41"}}

	if _, err := q.Discover(f, f, "agent-failed", KindWork); err != nil {
		t.Fatalf("Discover: %v", err)
	}

	snap := q.Snapshot()[0]
	if !strings.HasPrefix(snap.Reason, blockerFailedPrefix) {
		t.Errorf("Reason = %q, want prefix %q", snap.Reason, blockerFailedPrefix)
	}
}

// An Unpick landing between Discover reading a pick as a candidate and
// claiming it never lets that claim through, so Unpick's "zero Issue Tracker
// calls, never launches" guarantee holds even against Discover's own
// blocker-readiness check (#650).
func TestQueue_Discover_UnpickDuringClaimCheck_NeverLaunches(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})

	batch, err := q.Discover(removeOnDepsOf{Fake: f, q: q, num: "42"}, f, "", KindWork)

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(batch.Issues) != 0 {
		t.Errorf("issues = %+v, want none — the pick was unpicked mid-check", batch.Issues)
	}
	if len(f.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls = %+v, want none — an unpicked issue is never claimed", f.TransitionStateCalls)
	}
	if snap := q.Snapshot(); len(snap) != 0 {
		t.Errorf("Snapshot = %+v, want empty — Remove already dropped #42", snap)
	}
}

// A pick whose DepsOf call fails holds at PickHeld with a reason distinct
// from a real open blocker, rather than launching on a transient tracker
// failure such as a rate limit or timeout (#752).
func TestQueue_Discover_HoldsPickOnDepsOfFailure(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})

	batch, err := q.Discover(failDepsOf{Fake: f, num: "42"}, f, "", KindWork)

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(batch.Issues) != 0 {
		t.Errorf("issues = %+v, want none — a DepsOf failure must hold, not launch", batch.Issues)
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

// The hold fires even when the pick has a real registered blocker a healthy
// DepsOf call would have reported, proving the failure path holds because
// DepsOf errored, not merely because the pick has zero blockers (#1104).
func TestQueue_Discover_HoldsPickOnDepsOfFailureWithRealBlocker(t *testing.T) {
	q := NewQueue()
	q.Add(Pick{Number: "42", Title: "fix the thing", State: PickQueued})
	f := forge.NewFake(forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"})
	f.SetIssue(forge.Issue{Number: "42", Labels: []string{"ready-for-agent"}})
	f.SetIssue(forge.Issue{Number: "41", State: forge.IssueOpen})
	f.NativeDeps = map[string][]string{"42": {"41"}}

	batch, err := q.Discover(failDepsOf{Fake: f, num: "42"}, f, "", KindWork)

	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(batch.Issues) != 0 {
		t.Errorf("issues = %+v, want none — DepsOf failure must hold even with a real blocker registered", batch.Issues)
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
// synchronously Removes that pick from q, simulating an operator's Unpick
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
