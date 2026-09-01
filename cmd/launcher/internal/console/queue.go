package console

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/localloop"
	"spindrift.dev/launcher/internal/waves"
)

// Queue is the session's thread-safe operator queue: the live backing store for
// the operator-queue Discoverer the continuous engine drains through. Unlike
// Model.Picks — a pure snapshot Update applies for View to render — Queue is
// mutated directly, since Add, Remove, and Discover are called from outside the
// pure core.
type Queue struct {
	mu    sync.Mutex
	picks []Pick
}

// NewQueue returns an empty Queue.
func NewQueue() *Queue {
	return &Queue{}
}

// Add appends a queued pick, stamping QueuedAt to now. It is the single choke
// point every pick lands through, so Age always has a real source moment to
// format from.
func (q *Queue) Add(p Pick) {
	q.mu.Lock()
	defer q.mu.Unlock()
	p.QueuedAt = time.Now()
	q.picks = append(q.picks, p)
}

// Remove drops the queued or held pick numbered num, reporting whether one was
// removed. A pick already claiming, running, or settled is left alone: the
// operator, never Discover, decides whether a held pick's failed blocker is
// worth unpicking.
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

// hasQueuedForKinds reports whether any pick still holds at PickQueued whose
// effectiveKind is one of kinds — drain's loop-continuation gate. A plain "any
// kind" check would spin forever on a pick whose kind has no wired launch stack
// (no Discover call ever claims it); scoping to the kinds drain is actually
// servicing leaves such a pick stranded at PickQueued instead.
func (q *Queue) hasQueuedForKinds(kinds []Kind) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, p := range q.picks {
		if p.State != PickQueued {
			continue
		}
		for _, k := range kinds {
			if p.effectiveKind() == k {
				return true
			}
		}
	}
	return false
}

// PendingCount reports how many picks of kind are at PickQueued -- ready to
// launch, waiting for a slot. PickHeld is excluded: its blockers are not all
// satisfied. Unlike Discover this is a pure read, so a stale-drain report can
// call it without triggering a claim.
func (q *Queue) PendingCount(kind Kind) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, p := range q.picks {
		if p.State == PickQueued && p.effectiveKind() == kind {
			n++
		}
	}
	return n
}

// Snapshot returns a copy of the queue's current picks, in pick order.
func (q *Queue) Snapshot() []Pick {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Pick, len(q.picks))
	copy(out, q.picks)
	return out
}

// Discover is the waves.Discoverer this queue backs. It walks queued and held
// picks in order and returns the first that claims successfully as a
// single-issue batch; a refill with nothing launchable returns no issues, though
// it may still have moved picks onto PickHeld.
//
// A pick whose blockers are unsatisfied holds at PickHeld with BlockedBy naming
// them and Discover moves on, so an earlier held pick never stalls a later ready
// one. A pick whose DepsOf call itself failed holds too, with a distinguishing
// reason: NewReadiness's best-effort skip makes a failed lookup
// indistinguishable from "confirmed zero blockers" unless the failed set is
// checked explicitly. A ready pick whose Dispatchable->InProgress claim races
// (another loop, a closed issue, a relabel) dissolves with the reason, so a
// stale queue can only produce a failed claim, never a wrong dispatch.
// tryMarkClaiming re-checks the pick right before the claim, so a concurrent
// Unpick landing anywhere in the readiness check is never raced into a launch.
//
// The returned batch's Edges/Sources are always empty: Discover already resolved
// this pick's readiness, and the engine's downstream blocker gate no-ops against
// an empty graph, so nothing gates twice.
//
// kind restricts the scan to picks whose effectiveKind matches. The console's
// per-kind drain calls Discover once per kind with that kind's own tracker, so a
// pick of the other kind is skipped in place rather than claimed on the wrong
// tracker.
func (q *Queue) Discover(tracker forge.IssueTracker, cf forge.CodeForge, failedLabel string, kind Kind) (waves.Batch, error) {
	// Resolved fresh per call rather than cached on Queue: tracker varies per
	// stack (work vs research), so a cached value could carry one stack's
	// tracker-side state into the other's picks. Zero-value backend.Descriptor
	// rows are fine -- Status reads only the PRForge/LandingContainmentQuery
	// handles.
	caps := forge.ResolveCapabilities(cf, tracker, backend.Descriptor{}, backend.Descriptor{})
	for _, pick := range q.claimable() {
		if pick.effectiveKind() != kind {
			continue
		}
		readiness, _ := waves.NewReadiness(tracker, []waves.Issue{{Number: pick.Number, Title: pick.Title}})
		if readiness.Failed[pick.Number] {
			// A transient DepsOf failure looks identical to "confirmed zero
			// blockers" in edges alone — hold rather than launch, so a
			// genuinely-blocked pick never claims on a tracker hiccup.
			q.setState(pick.Number, PickHeld, "blocker check failed, will retry")
			continue
		}
		cfg := waves.Config{FailedLabel: failedLabel}
		cfg.SeedScopeOf = localloop.SeedScopeResolver(tracker, caps)
		ready, failed, unready := readiness.Status(cfg, tracker, cf, caps, pick.Number)
		if !ready {
			q.setHeld(pick.Number, unready, failed, readiness.Sources[pick.Number])
			continue
		}
		if !q.tryMarkClaiming(pick.Number) {
			continue // removed (Unpick) between the readiness snapshot and this claim
		}
		// This transition IS the real claim, which is why
		// runContinuousQueue's own Claim is a documented no-op.
		if err := tracker.TransitionState(pick.Number, forge.Dispatchable, forge.InProgress); err != nil {
			q.dissolve(pick.Number, err.Error())
			continue
		}
		q.setState(pick.Number, PickRunning, "")
		return waves.Batch{Issues: []waves.Issue{{Number: pick.Number, Title: pick.Title}}}, nil
	}
	return waves.Batch{}, nil
}

// Empty reports whether the queue has no pick left to launch — none at
// PickQueued or PickHeld. A held pick deliberately counts as non-empty: its
// blocker may have cleared out-of-band, so it still needs a launch attempt on
// the next call.
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

// setHeld marks the pick numbered num held, rendering unready as the BlockedBy
// badge. failed carries every declared blocker with the Failed label, whether or
// not it is also in unready (a closed blocker can be Failed-labeled and still
// read ready), and renders as Reason — surfaced on the row without dissolving
// the pick, since the Console never auto-unpicks.
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

// tryMarkClaiming marks the pick numbered num PickClaiming and reports success,
// but only if it is still present at PickQueued or PickHeld. That closes the
// window between Discover's readiness snapshot and its tracker claim, so a
// concurrent Unpick always wins and Unpick's "zero Issue Tracker calls, never
// launches" guarantee holds. Scans back-to-front like setState.
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

// setState updates the newest pick numbered num in place. It scans back-to-front
// because a number can appear more than once: a terminated pick's row (ADR 0024)
// is never removed, so a later re-pick appends a second row: the newest is the
// live claim, older ones are terminal. BlockedBy is always cleared — it is
// PickHeld-only data setHeld sets directly, so no other transition may carry a
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
