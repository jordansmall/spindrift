package console

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/waves"
)

// Queue is the session's thread-safe operator queue: the live backing store
// for the operator-queue Discoverer the continuous engine drains through.
// Unlike Model.Picks — a pure snapshot Update applies for View to render —
// Queue is mutated directly by Add, Remove, and Discover, since those calls
// come from outside the pure core (the run loop and a background engine
// invocation).
type Queue struct {
	mu    sync.Mutex
	picks []Pick
}

// NewQueue returns an empty Queue.
func NewQueue() *Queue {
	return &Queue{}
}

// Add appends a queued pick, stamping QueuedAt to now — the single choke
// point every pick lands through, so Age (refreshPickDecorations) always has a real
// source moment to format from (issue #1500).
func (q *Queue) Add(p Pick) {
	q.mu.Lock()
	defer q.mu.Unlock()
	p.QueuedAt = time.Now()
	q.picks = append(q.picks, p)
}

// Remove drops the queued or held pick numbered num, if any, reporting
// whether one was removed. It only ever removes a pick still holding at
// PickQueued or PickHeld — a pick already claiming, running, or settled is
// left alone; the operator decides whether a held pick's failed blocker is
// worth unpicking (#650), Discover never does it for them.
func (q *Queue) Remove(num string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, p := range q.picks {
		if p.Number == num && (p.State == PickQueued || p.State == PickHeld) {
			q.picks = append(q.picks[:i], q.picks[i+1:]...)
			return true
		}
	}
	return false
}

// hasQueued reports whether any pick still holds at PickQueued.
func (q *Queue) hasQueued() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, p := range q.picks {
		if p.State == PickQueued {
			return true
		}
	}
	return false
}

// Snapshot returns a copy of the queue's current picks, in pick order.
func (q *Queue) Snapshot() []Pick {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Pick, len(q.picks))
	copy(out, q.picks)
	return out
}

// Discover is the waves.Discoverer this queue backs. It walks queued and
// held picks in order; a pick whose declared blockers (waves.BuildEdges,
// waves.BlockerStatus — the same machinery headless waves use, no second
// parser) are not all satisfied holds at PickHeld with BlockedBy naming
// them, and Discover moves on to the next candidate rather than launching
// it, so an earlier held pick never stalls a later ready one. A pick whose
// DepsOf call itself failed (transient tracker hiccup — rate limit, timeout,
// flaky API call) holds too, with a reason distinguishing it from a real
// open blocker, rather than launching: BuildEdges' best-effort skip makes a
// failed lookup indistinguishable from "confirmed zero blockers" unless
// Discover checks the failed set explicitly (#752). A ready
// pick's atomic Dispatchable->InProgress claim, once it races (another
// loop, a closed issue, a relabel), dissolves that pick with the reason and
// Discover moves on too, so a stale queue can only produce a failed claim,
// never a wrong dispatch. tryMarkClaiming re-checks the pick is still
// PickQueued or PickHeld right before that claim, so a concurrent Unpick
// landing anywhere in the readiness check above is never raced into a
// launch. The first pick that claims successfully is returned as a
// single-issue batch (edges and sources always empty — Discover already
// resolved this pick's own readiness and rendered BlockedBy itself via
// setHeld below, so the engine's own blocker gate has nothing left to
// check); a refill with nothing launchable returns no issues, which may
// still have moved one or more picks onto PickHeld.
func (q *Queue) Discover(tracker forge.IssueTracker, cf forge.CodeForge, failedLabel string) ([]waves.Issue, map[string][]string, waves.Sources, error) {
	for _, pick := range q.claimable() {
		result, _ := waves.BuildEdges(tracker, []waves.Issue{{Number: pick.Number, Title: pick.Title}})
		edges, sources := result.Edges, result.Sources
		if result.Failed[pick.Number] {
			// A transient DepsOf failure looks identical to "confirmed zero
			// blockers" in edges alone — hold rather than launch, since a
			// genuinely-blocked pick must never claim on a tracker hiccup
			// (#752).
			q.setState(pick.Number, PickHeld, "blocker check failed, will retry")
			continue
		}
		ready, failed, unready := waves.BlockerStatus(waves.Config{FailedLabel: failedLabel}, tracker, cf, pick.Number, edges)
		if !ready {
			q.setHeld(pick.Number, unready, failed, sources[pick.Number])
			continue
		}
		if !q.tryMarkClaiming(pick.Number) {
			continue // removed (Unpick) between the readiness snapshot and this claim
		}
		// This is the claim launcher.go's zero-value waves.Config
		// (Label==InProgressLabel, both "") relies on Queue.Discover having
		// already done (#706): it's why claimIssue (waves/engine.go) skips
		// a second Dispatchable->InProgress transition for this pick.
		if err := tracker.TransitionState(pick.Number, forge.Dispatchable, forge.InProgress); err != nil {
			q.dissolve(pick.Number, err.Error())
			continue
		}
		q.setState(pick.Number, PickRunning, "")
		// nil, not the zero-value maps: matches the no-launchable-candidate
		// fallback below, and main.go's runContinuousDispatch's sibling
		// Discoverer (#903).
		return []waves.Issue{{Number: pick.Number, Title: pick.Title}}, nil, nil, nil
	}
	return nil, nil, nil, nil
}

// Empty reports whether the queue has no pick left to launch — none at
// PickQueued or PickHeld. tryLaunch (launcher.go) gates its drain spawn on
// this: unlike hasQueued, a held pick counts as non-empty, since its
// blocker may have cleared out-of-band and it still needs a launch attempt
// on the next call (#650).
func (q *Queue) Empty() bool {
	return len(q.claimable()) == 0
}

// claimable returns a snapshot, in queue order, of every pick still
// eligible to launch — queued or already held (re-evaluated every refill).
func (q *Queue) claimable() []Pick {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []Pick
	for _, p := range q.picks {
		if p.State == PickQueued || p.State == PickHeld {
			out = append(out, p)
		}
	}
	return out
}

// setHeld marks the pick numbered num held on unready, formatting it as the
// BlockedBy badge; failed — every declared blocker carrying the Failed
// label, whether or not it's also in unready (a closed blocker can be
// Failed-labeled and still read ready by BlockerReady's fallback) — renders
// as Reason, surfacing on the row without dissolving the pick, since the
// Console never auto-unpicks (#650).
func (q *Queue) setHeld(num string, unready, failed []string, sources map[string]forge.DepSource) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.picks {
		if q.picks[i].Number == num {
			q.picks[i].State = PickHeld
			q.picks[i].BlockedBy = refList(unready, sources)
			q.picks[i].Reason = ""
			if len(failed) > 0 {
				q.picks[i].Reason = fmt.Sprintf("%s%s failed", blockerFailedPrefix, refList(failed, sources))
			}
			return
		}
	}
}

// refList formats a blocker-ref list for operator-facing display, e.g. "#41
// (native), #43 (body)".
func refList(nums []string, sources map[string]forge.DepSource) string {
	refs := make([]string, len(nums))
	for i, n := range nums {
		refs[i] = forge.Ref(n, sources[n])
	}
	return strings.Join(refs, ", ")
}

// tryMarkClaiming marks the pick numbered num PickClaiming and reports
// success, but only if it is still present and still holding at PickQueued
// or PickHeld — closing the window between Discover's readiness snapshot
// and its actual tracker claim so a concurrent Unpick (Remove) always wins:
// a pick removed in that window is never claimed, matching Unpick's "zero
// Issue Tracker calls, never launches" guarantee (#650). Scans back-to-front
// like setState, so a duplicate number (a terminated row left behind by
// ADR-0024's Terminate, plus a fresh re-pick) targets the newest row, not
// the stale terminal one.
func (q *Queue) tryMarkClaiming(num string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := len(q.picks) - 1; i >= 0; i-- {
		if q.picks[i].Number == num {
			if q.picks[i].State != PickQueued && q.picks[i].State != PickHeld {
				return false
			}
			q.picks[i].State = PickClaiming
			q.picks[i].BlockedBy = ""
			return true
		}
	}
	return false
}

// setState updates the newest (most recently Add()ed) pick numbered num in
// place. Scanning back-to-front, rather than stopping at the first match,
// matters once a number can appear more than once — a terminated pick's row
// (ADR 0024, issue #649) is never removed, so a later re-pick appends a
// second row for the same number; the newest one is always the live claim,
// the older one(s) already terminal and never touched again. BlockedBy is
// always cleared here — it is PickHeld-only data setHeld sets directly, so
// any other transition (claiming, running, dissolved) must not carry a
// stale badge forward.
func (q *Queue) setState(num string, state PickState, reason string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := len(q.picks) - 1; i >= 0; i-- {
		if q.picks[i].Number == num {
			q.picks[i].State = state
			q.picks[i].Reason = reason
			q.picks[i].BlockedBy = ""
			return
		}
	}
}

// dissolve marks the pick numbered num dissolved with reason.
func (q *Queue) dissolve(num, reason string) {
	q.setState(num, PickDissolved, reason)
}
