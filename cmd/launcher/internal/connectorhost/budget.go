package connectorhost

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// budget is one call's wall-clock allowance. It measures guest work: the clock
// stops while the guest waits on an http_request and the allowance grows with the
// response bytes the call delivered, which the guest must then decode.
type budget struct {
	cancel context.CancelCauseFunc

	mu      sync.Mutex
	timer   *time.Timer
	left    time.Duration // what remains while paused; stale while running
	started time.Time     // when the running timer was last armed
	paused  bool
	expired bool

	requests int // http_requests served or refused so far
	credited int // response bytes credited so far
}

// newBudget returns a context derived from parent, cancelled with ErrBudget once
// d of running time has passed. The caller must close the budget.
func newBudget(parent context.Context, d time.Duration) (context.Context, *budget) {
	ctx, cancel := context.WithCancelCause(parent)
	b := &budget{cancel: cancel, left: d, started: time.Now()}
	b.timer = time.AfterFunc(d, b.expire)
	return ctx, b
}

func (b *budget) expire() {
	b.cancel(ErrBudget)
}

// close stops the clock and cancels the context; a cause already set stands.
func (b *budget) close() {
	b.mu.Lock()
	b.timer.Stop()
	b.mu.Unlock()
	b.cancel(nil)
}

// pause stops the clock.
func (b *budget) pause() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.paused || b.expired {
		return
	}
	b.paused = true
	if !b.timer.Stop() {
		// The timer fired, so ErrBudget is cancelled or about to be; an expired
		// budget is not revived by a later resume.
		b.expired = true
		return
	}
	b.left -= time.Since(b.started)
}

// resume restarts the clock with credit added to what was left.
func (b *budget) resume(credit time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.paused || b.expired {
		return
	}
	b.paused = false
	b.left += credit
	if b.left <= 0 {
		b.expired = true
		b.cancel(ErrBudget)
		return
	}
	b.started = time.Now()
	b.timer.Reset(b.left)
}

// httpRequest serves one http_request through h with the clock stopped, then
// credits the response bytes it returned. It refuses the call past
// maxHTTPRequests, since the pause would otherwise let a looping guest hold
// the caller for as long as the host answers.
func (b *budget) httpRequest(ctx context.Context, h hostCalls, raw []byte, bufCap int) (int, []byte, http.Header, error) {
	b.mu.Lock()
	b.requests++
	over := b.requests > maxHTTPRequests
	b.mu.Unlock()
	if over {
		return 0, nil, nil, fmt.Errorf("%w: more than %d http_requests in one call", ErrHTTPRequestLimit, maxHTTPRequests)
	}
	b.pause()
	status, body, header, err := h.httpRequest(ctx, raw, bufCap)
	b.resume(b.credit(len(body)))
	return status, body, header, err
}

// credit returns the extra time n response bytes earn, within the cap on the
// bytes credited over the whole call.
func (b *budget) credit(n int) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	n = min(n, maxResponseCredit-b.credited)
	b.credited += n
	return time.Duration(n) * budgetPerMiB >> 20
}
