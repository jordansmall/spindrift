package console

import (
	"sync"

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

// Add appends a queued pick.
func (q *Queue) Add(p Pick) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.picks = append(q.picks, p)
}

// Remove drops the queued pick numbered num, if any, reporting whether one
// was removed. It only ever removes a pick still holding at PickQueued — a
// pick already claiming, running, or settled is left alone.
func (q *Queue) Remove(num string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, p := range q.picks {
		if p.Number == num && p.State == PickQueued {
			q.picks = append(q.picks[:i], q.picks[i+1:]...)
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

// Discover is the waves.Discoverer this queue backs. It claims the
// front-most still-queued pick — the atomic Dispatchable->InProgress
// transition — and returns it as a single-issue batch for the continuous
// engine to launch. A claim that races (another loop, a closed issue, a
// relabel) dissolves that pick with the reason and moves on to the next
// queued one, so a stale queue can only produce a failed claim, never a
// wrong dispatch. Edges are always empty: a single operator-picked issue
// carries no in-batch blocker graph.
func (q *Queue) Discover(tracker forge.IssueTracker) ([]waves.Issue, map[string][]string, error) {
	for {
		pick, ok := q.nextQueued()
		if !ok {
			return nil, nil, nil
		}
		if err := tracker.TransitionState(pick.Number, forge.Dispatchable, forge.InProgress); err != nil {
			q.dissolve(pick.Number, err.Error())
			continue
		}
		q.setState(pick.Number, PickRunning, "")
		return []waves.Issue{{Number: pick.Number, Title: pick.Title}}, map[string][]string{}, nil
	}
}

// nextQueued returns the front-most PickQueued pick, if any, and marks it
// PickClaiming.
func (q *Queue) nextQueued() (Pick, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, p := range q.picks {
		if p.State == PickQueued {
			q.picks[i].State = PickClaiming
			return q.picks[i], true
		}
	}
	return Pick{}, false
}

// setState updates the pick numbered num in place.
func (q *Queue) setState(num string, state PickState, reason string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.picks {
		if q.picks[i].Number == num {
			q.picks[i].State = state
			q.picks[i].Reason = reason
			return
		}
	}
}

// dissolve marks the pick numbered num dissolved with reason.
func (q *Queue) dissolve(num, reason string) {
	q.setState(num, PickDissolved, reason)
}
