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

// Queue is the session's thread-safe operator queue, backing the
// operator-queue Discoverer the continuous engine drains. Unlike Model.Picks,
// which is a pure snapshot, Queue is mutated directly by Add, Remove, and
// Discover, because those calls come from outside the pure core.
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
// format from (issue #1500).
func (q *Queue) Add(p Pick) {
	q.mu.Lock()
	defer q.mu.Unlock()
	p.QueuedAt = time.Now()
	q.picks = append(q.picks, p)
}

// Remove drops the queued or held pick numbered num, reporting whether one
// was removed. A pick already claiming, running, or settled is left alone;
// the operator decides whether a held pick's failed blocker is worth
// unpicking (#650), and Discover never decides it for them.
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

// hasQueuedForKinds reports whether any pick at PickQueued has an
// effectiveKind in kinds. Drain continues its loop on that answer (issue
// #1708); checking for any kind instead would spin forever on a pick whose
// kind has no wired launch stack, since no Discover call ever claims it.
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

// PendingCount reports how many picks of kind are still at PickQueued.
// PickHeld is excluded: its declared blockers are not all satisfied, so it is
// not ready to dispatch. This is a pure read, so a stale-drain report can
// call it without Discover's claim side effect (#2678/#2939).
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
// picks of kind in order, holds an unready one at PickHeld and moves on so it
// never stalls a later ready pick, and returns the first successful claim as a
// single-issue batch. Scanning one kind at a time is required: the console
// drains once per kind (issue #1708), each with that kind's own tracker.
func (q *Queue) Discover(tracker forge.IssueTracker, cf forge.CodeForge, failedLabel string, kind Kind) (waves.Batch, error) {
	// caps is resolved fresh per call, never cached on Queue (issue #2946):
	// tracker varies per stack (work vs research), so a cached value could
	// carry one stack's tracker state into the other's picks. Zero-value
	// backend.Descriptor rows are fine, as in engine.go's drainMaxJobs, because
	// Status only reads caps' PRForge and LandingContainmentQuery.
	caps := forge.ResolveCapabilities(cf, tracker, backend.Descriptor{}, backend.Descriptor{})
	for _, pick := range q.claimable() {
		if pick.effectiveKind() != kind {
			continue
		}
		readiness, _ := waves.NewReadiness(tracker, []waves.Issue{{Number: pick.Number, Title: pick.Title}})
		if readiness.Failed[pick.Number] {
			// A transient DepsOf failure looks identical to confirmed zero
			// blockers in edges alone, so hold rather than launch: a blocked
			// pick must never claim on a tracker hiccup (#752).
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
		// This transition is the real claim (#706), which is why
		// runContinuousQueue's own Claim (launcher.go, issue #2938) is a
		// documented no-op.
		if err := tracker.TransitionState(pick.Number, forge.Dispatchable, forge.InProgress); err != nil {
			q.dissolve(pick.Number, err.Error())
			continue
		}
		q.setState(pick.Number, PickRunning, "")
		// Edges/Sources/Failed left nil, not zero-value maps, matching the
		// fallback below and runContinuousDispatch's sibling Discoverer
		// (#903). The engine's own blocker gate is then a no-op, so nothing
		// gates twice.
		return waves.Batch{Issues: []waves.Issue{{Number: pick.Number, Title: pick.Title}}}, nil
	}
	return waves.Batch{}, nil
}

// Empty reports whether the queue has no pick left to launch. A held pick
// counts as non-empty: its blocker may have cleared out-of-band, so it still
// needs a launch attempt on the next call (#650).
func (q *Queue) Empty() bool {
	return len(q.claimable()) == 0
}

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
// badge and failed as Reason. failed covers every declared blocker carrying
// the Failed label even when it reads ready, and it shows on the row without
// dissolving the pick, because the Console never auto-unpicks (#650).
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

// refList formats a blocker-ref list for display, e.g. "#41 (native), #43 (body)".
func refList(nums []string, sources map[string]forge.DepSource) string {
	refs := make([]string, len(nums))
	for i, n := range nums {
		refs[i] = forge.Ref(n, sources[n])
	}
	return strings.Join(refs, ", ")
}

// tryMarkClaiming marks the pick numbered num PickClaiming and reports
// success, only if it still holds at PickQueued or PickHeld. That closes the
// window between Discover's readiness snapshot and its tracker claim, so a
// concurrent Unpick always wins (#650). It scans back-to-front like setState,
// so a duplicate number targets the newest row, not the terminal one.
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

// setState updates the newest pick numbered num in place. A terminated pick's
// row (ADR 0024, issue #649) is never removed, so a re-pick appends a second
// row for the same number and only the newest is the live claim, which is why
// the scan runs back-to-front. BlockedBy is cleared here because it is
// PickHeld-only data setHeld sets directly.
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

func (q *Queue) dissolve(num, reason string) {
	q.setState(num, PickDissolved, reason)
}
