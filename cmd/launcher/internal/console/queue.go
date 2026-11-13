package console

import (
	"errors"
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
// Discover, because those calls come from outside the pure core. Close latches
// it shut for good: a console quit drops what has not claimed and nothing new
// may claim afterwards.
type Queue struct {
	mu     sync.Mutex
	picks  []Pick
	closed bool
}

// NewQueue returns an empty Queue.
func NewQueue() *Queue {
	return &Queue{}
}

// Add appends a queued pick, stamping QueuedAt to now. It is the single choke
// point every pick lands through, so Age always has a real source moment to
// format from (issue #1500). It is a no-op once the queue is closed: a late
// pick would make Empty false while Discover claims nothing, spinning drain.
func (q *Queue) Add(p Pick) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
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
		if p.Number == num && p.State.unclaimed() {
			q.picks = append(q.picks[:i], q.picks[i+1:]...)
			return true
		}
	}
	return false
}

// Close drops every queued or held pick and latches the queue shut, so no
// Discover claims again, even a held pick whose blocker clears later. Picks
// already claiming or running are left alone. It makes no tracker call: an
// unclaimed pick never left its dispatchable state. Idempotent.
func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closeLocked()
}

// CloseIfIdle is Close, but only when no pick is claiming or running, reporting
// whether it closed. The check and the close share one q.mu hold, so a claim
// cannot be promoted in between and leave a Box the quit never asked about.
func (q *Queue) CloseIfIdle() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, p := range q.picks {
		if p.State.live() {
			return false
		}
	}
	q.closeLocked()
	return true
}

func (q *Queue) closeLocked() {
	q.closed = true
	kept := q.picks[:0]
	for _, p := range q.picks {
		if !p.State.unclaimed() {
			kept = append(kept, p)
		}
	}
	q.picks = kept
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
func (q *Queue) Discover(tracker forge.IssueTracker, cf forge.CodeForge, failedLabel, branchPrefix string, kind Kind) (waves.Batch, error) {
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
			q.hold(pick.Number, kind, "", "blocker check failed, will retry")
			continue
		}
		cfg := waves.Config{FailedLabel: failedLabel, BranchPrefix: branchPrefix}
		cfg.SeedScopeOf = localloop.SeedScopeResolver(tracker, caps)
		ready, failed, unready := readiness.Status(cfg, tracker, caps, pick.Number)
		if !ready {
			q.setHeld(pick.Number, kind, unready, failed, readiness.Sources[pick.Number])
			continue
		}
		if !q.tryMarkClaiming(pick.Number, kind) {
			continue // removed (Unpick) between the readiness snapshot and this claim
		}
		// This transition is the real claim (#706), which is why
		// runContinuousQueue's own Claim (launcher.go, issue #2938) is a
		// documented no-op.
		if err := tracker.TransitionState(pick.Number, forge.Dispatchable, forge.InProgress); err != nil {
			forge.CommentRefusal(tracker, pick.Number, err)
			reason := err.Error()
			if errors.Is(err, forge.ErrAlreadyClaimed) {
				reason = "skipped: already claimed"
			}
			q.dissolve(pick.Number, kind, reason)
			continue
		}
		if !q.promoteUnlessClosed(pick.Number, kind) {
			// Close landed inside the claiming window: nothing new may launch,
			// so hand the issue back rather than strand it at InProgress.
			reason := "dropped: session quit"
			if err := tracker.TransitionState(pick.Number, forge.InProgress, forge.Dispatchable); err != nil {
				forge.CommentRefusal(tracker, pick.Number, err)
				reason += " (claim revert failed: " + err.Error() + ")"
			}
			q.dissolve(pick.Number, kind, reason)
			return waves.Batch{}, nil
		}
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
		if p.State.unclaimed() {
			out = append(out, p)
		}
	}
	return out
}

// setHeld marks the pick numbered num of kind held, rendering unready as the
// BlockedBy badge and failed as Reason. failed is the unready blockers carrying
// the Failed label (a satisfied blocker never holds, #4911). Holding rather than
// dissolving the pick is deliberate: the Console never auto-unpicks (#650).
func (q *Queue) setHeld(num string, kind Kind, unready, failed []string, sources map[string]forge.DepSource) {
	reason := ""
	if len(failed) > 0 {
		reason = fmt.Sprintf("%s%s failed", blockerFailedPrefix, refList(failed, sources))
	}
	q.hold(num, kind, refList(unready, sources), reason)
}

// hold marks the newest row numbered num of kind PickHeld with blockedBy and
// reason, only if it is still unclaimed. Like tryMarkClaiming it is guarded,
// because Discover holds after dropping the lock for tracker calls: an Unpick
// landing mid-check must win, not resurrect an older terminal row (#6267).
// Close drops every unclaimed row, so the guard also covers a closed queue.
func (q *Queue) hold(num string, kind Kind, blockedBy, reason string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := q.newestIndex(num, kind)
	if i < 0 || !q.picks[i].State.unclaimed() {
		return
	}
	q.picks[i].State = PickHeld
	q.picks[i].BlockedBy = blockedBy
	q.picks[i].Reason = reason
}

// refList formats a blocker-ref list for display, e.g. "#41 (native), #43 (body)".
func refList(nums []string, sources map[string]forge.DepSource) string {
	refs := make([]string, len(nums))
	for i, n := range nums {
		refs[i] = forge.Ref(n, sources[n])
	}
	return strings.Join(refs, ", ")
}

// tryMarkClaiming marks the pick numbered num of kind PickClaiming and reports
// success, only if it still holds at PickQueued or PickHeld. That closes the
// window between Discover's readiness snapshot and its tracker claim, so a
// concurrent Unpick always wins (#650). Like setState, it targets the newest
// row numbered num of kind, not an older terminal one.
func (q *Queue) tryMarkClaiming(num string, kind Kind) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	i := q.newestIndex(num, kind)
	if i < 0 || !q.picks[i].State.unclaimed() {
		return false
	}
	q.picks[i].State = PickClaiming
	q.picks[i].BlockedBy = ""
	return true
}

// promoteUnlessClosed marks the claiming pick numbered num of kind PickRunning
// unless the queue is closed, reporting whether it did. The check and the
// promotion share one q.mu hold, so a pick is either running before Close (and
// visible to what reads live picks after it) or reverted by the caller.
func (q *Queue) promoteUnlessClosed(num string, kind Kind) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	q.setStateLocked(num, kind, PickRunning, "")
	return true
}

// setState updates the newest pick numbered num of kind in place. A terminated
// pick's row (ADR 0024, issue #649) is never removed, so a re-pick appends a
// second row for the same number and only the newest of a kind is that kind's
// live claim; a row of the other kind may share the number (issue #4230).
// BlockedBy is cleared here because it is PickHeld-only data hold sets
// directly.
func (q *Queue) setState(num string, kind Kind, state PickState, reason string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.setStateLocked(num, kind, state, reason)
}

func (q *Queue) setStateLocked(num string, kind Kind, state PickState, reason string) {
	// Defence in depth: no caller passes an unclaimed state now that holds go
	// through hold, but on a closed queue newestIndex would land on an older
	// terminal row of the same number and make it claimable again.
	if q.closed && state.unclaimed() {
		return
	}
	if i := q.newestIndex(num, kind); i >= 0 {
		q.picks[i].State = state
		q.picks[i].Reason = reason
		q.picks[i].BlockedBy = ""
	}
}

// liveRow returns the newest PickRunning pick numbered num of any kind, else
// the newest pick numbered num of any kind. It is kind-blind on purpose:
// Launcher.Terminate calls it to learn the pick's kind, so it cannot be given
// one. Preferring the running row matches LiveIssues, which a newer queued row
// of another kind must not shadow.
func (q *Queue) liveRow(num string) (Pick, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	newest := -1
	for i := len(q.picks) - 1; i >= 0; i-- {
		if q.picks[i].Number != num {
			continue
		}
		if q.picks[i].State == PickRunning {
			return q.picks[i], true
		}
		if newest < 0 {
			newest = i
		}
	}
	if newest < 0 {
		return Pick{}, false
	}
	return q.picks[newest], true
}

// newestIndex returns the index of the newest pick numbered num of kind, or
// -1. Caller holds q.mu. It scans back-to-front because a terminated pick's row
// is never removed, so a re-pick appends a second row and only the newest is
// the live claim. It matches kind too because a work and a research row can
// share a number (issue #4230).
func (q *Queue) newestIndex(num string, kind Kind) int {
	for i := len(q.picks) - 1; i >= 0; i-- {
		if q.picks[i].Number == num && q.picks[i].effectiveKind() == kind {
			return i
		}
	}
	return -1
}

func (q *Queue) dissolve(num string, kind Kind, reason string) {
	q.setState(num, kind, PickDissolved, reason)
}
