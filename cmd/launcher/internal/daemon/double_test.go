package daemon

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/report"
)

// scriptedRunner is the package's one scriptable Runner double. Most
// scripting fields are written once, before the pool starts, and read without
// mu; mu guards the call-recording state and the demand scripting, which
// tests change mid-run.
type scriptedRunner struct {
	// revisions/resolveAt/resolveErr: one revision per ResolveTip call's
	// resolve half, last value repeating once exhausted; resolveAt is a
	// 1-based call index that returns resolveErr instead (before the self
	// half is ever reached).
	revisions  []string
	resolveAt  int
	resolveErr error

	// selfPaths/selfErrAt/selfErr: the same convention as revisions, for
	// ResolveTip's self half, which is only served (and counted in
	// selfCalls) when a test scripts it: len(selfPaths) > 0, selfErrAt != 0,
	// or onSelf != nil.
	selfPaths []string
	selfErrAt int
	selfErr   error

	// moved: the same convention as revisions, for Tip.Moved; empty yields
	// false. Scripted independently of revisions, even in disagreement
	// (issue #3625): tip_moved follows the flag, never a revision diff.
	moved []bool

	// coalesceResolve, when set, makes resolveCount() count resolutions
	// rather than callers: a caller that finds a resolution already in flight
	// shares its Tip/error. Not a re-implementation of hostRunner.ResolveTip's
	// single-flight (no ctx-aware joiner exit, no TTL); the real semantics
	// are pinned in cmd/launcher/daemon/runner_test.go. Never flip it on for
	// an existing test: it changes which call index each caller lands on.
	coalesceResolve bool
	resolveFlight   *scriptedResolveFlight // guarded by mu

	// resolveHold/resolveJoins: installed by holdResolve to pin a coalesced
	// flight's leader while counting joiners; nil otherwise.
	resolveHold  chan struct{}
	resolveJoins chan struct{}

	// Result scripting, most specific winning: runErrAt (a 1-based *total*
	// call index; a held slot short-circuits even this), then byKind (per-kind
	// call index), then results in total call order. A test wanting per-slot
	// results pins the call order instead (holdSlots/releaseSlot or a
	// serialising hook), never a slot-keyed tier here.
	byKind   map[Kind][]ChildResult
	results  []ChildResult
	runErrAt int
	runErr   error
	// waitErrAt is runErrAt for the Wait half: a 1-based total call index
	// whose handle's Wait returns waitErr once the child has ended.
	waitErrAt int
	waitErr   error

	// Hooks fire before the scripted answer is computed, at each seam
	// crossing, so a test can cancel a context or read a clock at exactly
	// that instant. A non-nil error return short-circuits the seam with
	// that error and no scripted value.
	onResolve func(ctx context.Context, call int) error
	onSelf    func(ctx context.Context, call int, revision string) error
	onStart   func(ctx context.Context, req ChildRequest) error
	// onPrebuild answers Prebuild: call is its 1-based index. Unset means
	// success.
	onPrebuild func(ctx context.Context, call int, tip Tip) error
	// onSweep answers SweepLateMerges the way onPrebuild answers Prebuild,
	// after the revision is recorded; unset means success.
	onSweep func(ctx context.Context, revision string) error

	// Demand scripting: demand/demandErr are set (and may change) during a
	// test through setDemand/setDemandErr, so they live under mu with the
	// call counters. onDemand fires before the scripted answer, outside mu,
	// and may block to pin a probe's leader in place.
	demand      map[Kind]Demand
	demandErr   map[Kind]error
	demandCalls map[Kind]int
	demandFresh map[Kind][]bool // the fresh flag of each Demand call, in order
	onDemand    func(ctx context.Context, kind Kind)
	// outbox models a host-outbox-backed kind (setOutbox): its Demand names and counts
	// the keys not in the probe's inFlight; exclusive with demand for a kind
	// (the setters panic on both). demandInFlight records each call's inFlight.
	outbox         map[Kind][]dispatchkey.Key
	demandInFlight map[Kind][]dispatchkey.Set

	mu           sync.Mutex
	resolveCalls int
	selfCalls    int
	prebuildTips []Tip
	sweepRevs    []string
	runCalls     []runCall
	kindCalls    map[Kind]int
	inFlight     int
	peak         int
	onRecord     map[int]func(Record)

	// release/started: installed by holdSlots for tests that need to hold
	// several children open at once and release them one at a time. A nil
	// release (the default) means StartChild and Wait never block here at all.
	release map[int]chan ChildResult
	started chan int
	// handles: each held child's latest handle by slot, for a test to Wait on
	// again after the pool abandoned its wait.
	handles map[int]*scriptedHandle
	// inherits records InheritChild calls; exited maps a pid to the exit of a
	// child that finished while the handoff was in flight (InheritChild returns
	// an already-done handle for it); inheritErr fails the seam itself.
	inherits   []inheritCall
	exited     map[int]ChildResult
	pidSeq     int // last fake pid handed to a started child
	inheritErr error
}

// holdSlots switches the child into blocking mode for slots 0..n-1: each
// start announces its slot on started, then its Wait blocks until
// releaseSlot sends its result. Channels are sized for one wave, so a test
// spanning several waves must drain each before the next.
func (r *scriptedRunner) holdSlots(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	release := make(map[int]chan ChildResult, n)
	for s := 0; s < n; s++ {
		release[s] = make(chan ChildResult, 1)
	}
	r.release = release
	r.started = make(chan int, n)
}

// scriptedResolveFlight is one in-flight (or just-finished, until its
// leader clears r.resolveFlight) coalesced ResolveTip resolution — just
// enough state for resolveCount() to count resolutions rather than
// callers, not a mirror of hostRunner's tipFlight.
type scriptedResolveFlight struct {
	done chan struct{}
	tip  Tip
	err  error
}

// holdResolve parks a coalesced flight's leader on resolveHold until
// releaseResolve, giving joiners a real window to arrive. n sizes the joins
// channel awaitResolveJoins drains. It enforces coalesceResolve: on an
// uncoalesced runner the hold would sit unread and awaitResolveJoins would
// hang instead of failing at the setup mistake.
func (r *scriptedRunner) holdResolve(n int) {
	if !r.coalesceResolve {
		panic("scriptedRunner: holdResolve requires coalesceResolve")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.resolveHold = make(chan struct{})
	r.resolveJoins = make(chan struct{}, n)
}

// awaitResolveJoins blocks until n callers have joined the held leader's
// flight, failing the test with a clear message rather than hanging if
// fewer than n arrive within five seconds — same bound as awaitStart.
func (r *scriptedRunner) awaitResolveJoins(t *testing.T, n int) {
	t.Helper()
	r.mu.Lock()
	joins := r.resolveJoins
	r.mu.Unlock()
	for i := 0; i < n; i++ {
		select {
		case <-joins:
		case <-time.After(5 * time.Second):
			t.Fatalf("scriptedRunner: awaitResolveJoins timed out waiting for join %d/%d", i+1, n)
		}
	}
}

// releaseResolve lets a leader parked by holdResolve proceed. It fails the
// test rather than panicking on close(nil) without a matching holdResolve.
func (r *scriptedRunner) releaseResolve(t *testing.T) {
	t.Helper()
	r.mu.Lock()
	hold := r.resolveHold
	r.mu.Unlock()
	if hold == nil {
		t.Fatalf("scriptedRunner: releaseResolve: no hold installed (holdResolve?)")
	}
	close(hold)
}

// rearmResolve releases the currently held flight and arms the next round's
// hold in one critical section. releaseResolve then holdResolve would leave
// a window in which a fast slot becomes the next leader on the stale,
// already-closed hold and sails through unheld.
func (r *scriptedRunner) rearmResolve(t *testing.T, n int) {
	t.Helper()
	if !r.coalesceResolve {
		panic("scriptedRunner: rearmResolve requires coalesceResolve")
	}
	r.mu.Lock()
	hold := r.resolveHold
	r.resolveHold = make(chan struct{})
	r.resolveJoins = make(chan struct{}, n)
	r.mu.Unlock()
	if hold == nil {
		t.Fatalf("scriptedRunner: rearmResolve: no hold installed (holdResolve?)")
	}
	close(hold)
}

// releaseSlot hands slot's blocked child its result. It fails the
// test rather than sending to a nil channel for a slot outside holdSlots'
// range, since a nil send blocks forever with none of awaitStart/
// awaitSleep's five-second bound.
func (r *scriptedRunner) releaseSlot(t *testing.T, slot int, res ChildResult) {
	t.Helper()
	r.mu.Lock()
	ch := r.release[slot]
	r.mu.Unlock()
	if ch == nil {
		t.Fatalf("scriptedRunner: releaseSlot: no held slot %d (holdSlots range?)", slot)
	}
	ch <- res
}

// announceEachSlot installs an onStart that announces "issue-<slot>" through
// req.OnRecord.
func (r *scriptedRunner) announceEachSlot() {
	r.onStart = func(ctx context.Context, req ChildRequest) error {
		if req.OnRecord != nil {
			req.OnRecord(Record{Event: report.EventBox, Key: dispatchkey.Issue(fmt.Sprintf("issue-%d", req.Slot))})
		}
		return nil
	}
}

// awaitStart receives one slot number off started, failing the test with a
// clear message rather than hanging forever if no slot ever starts.
func (r *scriptedRunner) awaitStart(t *testing.T) int {
	t.Helper()
	r.mu.Lock()
	started := r.started
	r.mu.Unlock()
	select {
	case s := <-started:
		return s
	case <-time.After(5 * time.Second):
		t.Fatalf("scriptedRunner: awaitStart timed out waiting for a slot to start")
		return -1
	}
}

// fireOnRecord calls slot's most recently started child's
// OnRecord, simulating a live report-pipe record while that child is still
// in flight. Failing rather than silently no-op-ing on a nil hook catches a
// test that fires before the slot's child has actually started.
func (r *scriptedRunner) fireOnRecord(t *testing.T, slot int, rec Record) {
	t.Helper()
	r.mu.Lock()
	fn := r.onRecord[slot]
	r.mu.Unlock()
	if fn == nil {
		t.Fatalf("scriptedRunner: fireOnRecord: no OnRecord captured for slot %d", slot)
	}
	fn(rec)
}

// fireOnIssue is fireOnRecord for a "box" record on a bare issue number.
func (r *scriptedRunner) fireOnIssue(t *testing.T, slot int, issue string) {
	t.Helper()
	r.fireOnRecord(t, slot, Record{Event: report.EventBox, Key: dispatchkey.Issue(issue)})
}

func (r *scriptedRunner) resolveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.resolveCalls
}

func (r *scriptedRunner) selfCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.selfCalls
}

func (r *scriptedRunner) runCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.runCalls)
}

func (r *scriptedRunner) kindCount(k Kind) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.kindCalls[k]
}

// peakConcurrency is the high-water mark of children simultaneously started
// and not yet ended, tracked from StartChild until its Wait returns (not just
// the blocking holdSlots wait) so it reflects genuine overlap however the
// result is produced.
func (r *scriptedRunner) peakConcurrency() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak
}

// calls returns a copy of every recorded StartChild call, in order.
func (r *scriptedRunner) calls() []runCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]runCall, len(r.runCalls))
	copy(out, r.runCalls)
	return out
}

// ResolveTip dispatches straight to resolveTipOnce unless coalesceResolve is
// set: then the first caller leads the flight (parking on any holdResolve
// hold) and later arrivals join it, signalling resolveJoins and sharing its
// result, so resolveCalls counts only leaders.
func (r *scriptedRunner) ResolveTip(ctx context.Context) (Tip, error) {
	if !r.coalesceResolve {
		return r.resolveTipOnce(ctx)
	}

	r.mu.Lock()
	if flight := r.resolveFlight; flight != nil {
		joins := r.resolveJoins
		r.mu.Unlock()
		if joins != nil {
			joins <- struct{}{}
		}
		<-flight.done
		return flight.tip, flight.err
	}
	flight := &scriptedResolveFlight{done: make(chan struct{})}
	r.resolveFlight = flight
	hold := r.resolveHold
	r.mu.Unlock()

	if hold != nil {
		<-hold
	}

	tip, err := r.resolveTipOnce(ctx)
	flight.tip, flight.err = tip, err

	r.mu.Lock()
	r.resolveFlight = nil
	r.mu.Unlock()
	close(flight.done)

	return tip, err
}

// resolveTipOnce serves the resolve half first: resolveAt short-circuits
// with resolveErr before the self half (and selfCalls) is reached. The self
// half runs only when scripted. A scripted self failure still carries the
// resolved revision, wrapped as *SelfEvalError, so the pool's backoff event
// has a revision to report.
func (r *scriptedRunner) resolveTipOnce(ctx context.Context) (Tip, error) {
	r.mu.Lock()
	r.resolveCalls++
	call := r.resolveCalls
	r.mu.Unlock()

	if r.onResolve != nil {
		if err := r.onResolve(ctx, call); err != nil {
			return Tip{}, err
		}
	}

	if r.resolveAt != 0 && call == r.resolveAt {
		return Tip{}, r.resolveErr
	}
	// An empty revisions yields "" rather than panicking: most tests never
	// script a revision at all.
	revision := ""
	if len(r.revisions) > 0 {
		idx := call - 1
		if idx >= len(r.revisions) {
			idx = len(r.revisions) - 1
		}
		revision = r.revisions[idx]
	}
	// An empty moved yields false rather than panicking, same reasoning:
	// most tests never care whether the tip moved.
	moved := false
	if len(r.moved) > 0 {
		idx := call - 1
		if idx >= len(r.moved) {
			idx = len(r.moved) - 1
		}
		moved = r.moved[idx]
	}

	if len(r.selfPaths) == 0 && r.selfErrAt == 0 && r.onSelf == nil {
		return Tip{Revision: revision, Moved: moved}, nil
	}

	r.mu.Lock()
	r.selfCalls++
	selfCall := r.selfCalls
	r.mu.Unlock()

	if r.onSelf != nil {
		if err := r.onSelf(ctx, selfCall, revision); err != nil {
			return Tip{Revision: revision, Moved: moved}, &SelfEvalError{Err: err}
		}
	}

	if r.selfErrAt != 0 && selfCall == r.selfErrAt {
		return Tip{Revision: revision, Moved: moved}, &SelfEvalError{Err: r.selfErr}
	}
	if len(r.selfPaths) == 0 {
		return Tip{Revision: revision, Moved: moved}, nil
	}
	idx := selfCall - 1
	if idx >= len(r.selfPaths) {
		idx = len(r.selfPaths) - 1
	}
	return Tip{Revision: revision, Moved: moved, SelfPath: r.selfPaths[idx]}, nil
}

// pickScripted clamps idx to the last index of list ("last value repeats").
// An empty list yields the zero ChildResult: an all-unset scriptedRunner is
// a valid script.
func pickScripted(list []ChildResult, idx int) ChildResult {
	if len(list) == 0 {
		return ChildResult{}
	}
	if idx >= len(list) {
		idx = len(list) - 1
	}
	return list[idx]
}

// beginChild is the bookkeeping every child seam (StartChild, InheritChild)
// shares, so the two cannot drift: record appends the call under mu and
// returns its 1-based total; the helper then counts the kind, registers
// req.OnRecord and takes the child in flight. end undoes the in-flight count.
func (r *scriptedRunner) beginChild(req ChildRequest, record func() int) (total, kindIdx int, release map[int]chan ChildResult, started chan int, end func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	total = record()
	if r.kindCalls == nil {
		r.kindCalls = make(map[Kind]int)
	}
	kindIdx = r.kindCalls[req.Kind]
	r.kindCalls[req.Kind]++
	if r.onRecord == nil {
		r.onRecord = make(map[int]func(Record))
	}
	r.onRecord[req.Slot] = req.OnRecord
	r.inFlight++
	if r.inFlight > r.peak {
		r.peak = r.inFlight
	}
	end = func() {
		r.mu.Lock()
		r.inFlight--
		r.mu.Unlock()
	}
	return total, kindIdx, r.release, r.started, end
}

func (r *scriptedRunner) StartChild(ctx context.Context, req ChildRequest) (ChildHandle, error) {
	total, kindIdx, release, started, end := r.beginChild(req, func() int {
		r.runCalls = append(r.runCalls, runCall{Kind: req.Kind, Revision: req.Revision, Slot: req.Slot})
		return len(r.runCalls)
	})

	// mu must not be held across onStart: it would serialize slots that tests
	// need genuinely concurrent.
	if r.onStart != nil {
		if err := r.onStart(ctx, req); err != nil {
			end()
			return nil, err
		}
	}

	if release != nil {
		return r.heldHandle(req, release, started, end)
	}

	if r.runErrAt != 0 && total == r.runErrAt {
		end()
		return nil, r.runErr
	}
	return r.scriptedDone(req, total, kindIdx, end), nil
}

// heldHandle is the holdSlots child: it ends when its slot's result arrives.
func (r *scriptedRunner) heldHandle(req ChildRequest, release map[int]chan ChildResult, started chan int, end func()) (ChildHandle, error) {
	ch := release[req.Slot]
	if ch == nil {
		// A nil receive in Wait blocks forever, unlike awaitStart/
		// awaitSleep's explicit five-second bound; fail the call
		// instead of hanging the test on a slot outside holdSlots' range.
		end()
		return nil, fmt.Errorf("scriptedRunner: StartChild: slot %d out of holdSlots range", req.Slot)
	}
	if started != nil {
		started <- req.Slot
	}
	h := &scriptedHandle{end: end, release: ch, state: r.newChildState()}
	r.mu.Lock()
	if r.handles == nil {
		r.handles = make(map[int]*scriptedHandle)
	}
	r.handles[req.Slot] = h
	r.mu.Unlock()
	return h, nil
}

// scriptedDone is the already-finished child the byKind/results scripting
// picks for call number total (1-based) and per-kind index kindIdx.
func (r *scriptedRunner) scriptedDone(req ChildRequest, total, kindIdx int, end func()) *scriptedHandle {
	var waitErr error
	if r.waitErrAt != 0 && total == r.waitErrAt {
		waitErr = r.waitErr
	}
	if list, ok := r.byKind[req.Kind]; ok {
		return &scriptedHandle{end: end, res: pickScripted(list, kindIdx), done: true, err: waitErr, state: r.newChildState()}
	}
	return &scriptedHandle{end: end, res: pickScripted(r.results, total-1), done: true, err: waitErr, state: r.newChildState()}
}

// InheritChild rebuilds the handle of a child an earlier daemon image started.
// It registers req.OnRecord and counts in flight like StartChild, then picks
// the handle by pid: exited[pid] is a child that finished while the handoff was
// in flight; otherwise a held slot gets a held handle and anything else the
// next scripted result.
func (r *scriptedRunner) InheritChild(ctx context.Context, req ChildRequest, child ChildState) (ChildHandle, error) {
	total, kindIdx, release, started, end := r.beginChild(req, func() int {
		r.inherits = append(r.inherits, inheritCall{Kind: req.Kind, Revision: req.Revision, Slot: req.Slot, PID: child.PID, Stop: req.Stop, Abort: req.Abort, CtxErr: ctx.Err()})
		return len(r.runCalls) + len(r.inherits)
	})
	r.mu.Lock()
	exited, hasExit := r.exited[child.PID]
	inheritErr := r.inheritErr
	r.mu.Unlock()

	if inheritErr != nil {
		end()
		return nil, inheritErr
	}
	if hasExit {
		return &scriptedHandle{end: end, res: exited, done: true, state: child}, nil
	}
	if release != nil {
		h, err := r.heldHandle(req, release, started, end)
		if err != nil {
			return nil, err
		}
		h.(*scriptedHandle).state = child
		return h, nil
	}
	h := r.scriptedDone(req, total, kindIdx, end)
	h.state = child
	return h, nil
}

// newChildState gives a started child a distinct nonzero pid and report fd, so
// a test can check a handoff names the right child.
func (r *scriptedRunner) newChildState() ChildState {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pidSeq++
	return ChildState{PID: 1000 + r.pidSeq, ReportFD: 100 + r.pidSeq}
}

// inheritCalls returns a copy of every recorded InheritChild call, in order.
func (r *scriptedRunner) inheritCalls() []inheritCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.inherits)
}

// run is StartChild then Wait(nil), for tests that drive the double directly
// rather than through the pool.
func (r *scriptedRunner) run(ctx context.Context, req ChildRequest) (ChildResult, error) {
	h, err := r.StartChild(ctx, req)
	if err != nil {
		return ChildResult{}, err
	}
	return h.Wait(nil)
}

// scriptedHandle is one scripted child. A held child (release non-nil) ends
// when its slot's result arrives; abandoning its Wait leaves it in flight and
// a later Wait can still collect the result.
type scriptedHandle struct {
	end     func()
	release <-chan ChildResult
	res     ChildResult
	done    bool
	err     error // Wait's error once the child has ended
	state   ChildState
}

func (h *scriptedHandle) State() ChildState { return h.state }

func (h *scriptedHandle) Wait(abandon <-chan struct{}) (ChildResult, error) {
	if !h.done {
		select {
		case h.res = <-h.release:
			h.done = true
		case <-abandon:
			return ChildResult{}, ErrWaitAbandoned
		}
	}
	if h.end != nil {
		h.end()
		h.end = nil
	}
	if h.err != nil {
		return ChildResult{}, h.err
	}
	return h.res, nil
}

// testClock is the package's one scriptable Clock double: an instant default
// advance, a parked mode that blocks until woken or cancelled, a step mode
// that releases every sleeper over a fresh barrier so concurrent sleeps
// never additively stack, releaseOne, and a sleep-entry signal plus onSleep
// hook for the instant a slot genuinely idles.
type testClock struct {
	mu       sync.Mutex
	now      time.Time
	waitList []time.Duration

	// parked makes Sleep block instead of instantly advancing now;
	// barrierClosed keeps wake and step from double-closing the current
	// barrier generation.
	parked        bool
	barrier       chan struct{}
	barrierClosed bool

	// sleepers queues one release channel per parked Sleep, oldest first, for
	// releaseOne; each Sleep drops its own on return by any route.
	sleepers []chan struct{}

	// sleepSignal, if set, gets a non-blocking best-effort send on every
	// Sleep entry; a dropped send (nobody draining) must never block a
	// slot, so awaitSleep only ever needs to drain what a test actually
	// waits for.
	sleepSignal chan struct{}
	onSleep     func()
}

// park switches Sleep into blocking mode: it will not return on its own
// until wake or step is called, or ctx is done.
func (c *testClock) park() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.parked = true
	if c.barrier == nil {
		c.barrier = make(chan struct{})
		c.barrierClosed = false
	}
}

// wake releases every parked Sleep without changing now or installing a
// fresh barrier: everything is let through from here on, without
// cancelling the pool.
func (c *testClock) wake() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.barrier == nil {
		c.barrier = make(chan struct{})
	}
	if !c.barrierClosed {
		close(c.barrier)
		c.barrierClosed = true
	}
}

// step sets now and releases every Sleep call parked so far at once, then
// installs a fresh barrier for the next round. An additive advance is
// sound for one sleeper at a time but not once several Sleep calls race
// the same shared clock: their durations stack instead of overlapping.
func (c *testClock) step(now time.Time) {
	c.mu.Lock()
	c.now = now
	old := c.barrier
	wasClosed := c.barrierClosed
	c.barrier = make(chan struct{})
	c.barrierClosed = false
	c.mu.Unlock()
	if old != nil && !wasClosed {
		close(old)
	}
}

// releaseOne frees only the longest-parked sleeper still waiting, leaving its
// siblings parked. It does not advance now; callers move the clock separately
// (advanceBy) when the freed sleeper's next Decide needs time to have passed.
func (c *testClock) releaseOne(t *testing.T) {
	t.Helper()
	c.releaseNth(t, 0)
}

// releaseNth is releaseOne for the n'th-longest-parked sleeper.
func (c *testClock) releaseNth(t *testing.T, n int) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if n >= len(c.sleepers) {
		t.Fatalf("testClock: releaseNth(%d) with %d parked sleepers", n, len(c.sleepers))
	}
	close(c.sleepers[n])
	c.sleepers = append(c.sleepers[:n], c.sleepers[n+1:]...)
}

func (c *testClock) setNow(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func (c *testClock) advanceBy(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *testClock) waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, len(c.waitList))
	copy(out, c.waitList)
	return out
}

func (c *testClock) waitCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.waitList)
}

// awaitSleep drains n sleep-entry signals, failing the test with a clear
// message rather than hanging if fewer than n ever arrive. Sends are
// best-effort (see sleepSignal's doc comment), so a test must size its own
// buffered channel to at least the number of signals it intends to drain.
func (c *testClock) awaitSleep(t *testing.T, n int) {
	t.Helper()
	c.mu.Lock()
	sig := c.sleepSignal
	c.mu.Unlock()
	for i := 0; i < n; i++ {
		select {
		case <-sig:
		case <-time.After(5 * time.Second):
			t.Fatalf("testClock: awaitSleep timed out after draining %d/%d signals", i, n)
		}
	}
}

// awaitSleepers waits until exactly n Sleep calls are parked, then discards
// the sleep-entry signals buffered so far so a later awaitSleep counts only
// parks made after this point. Unlike awaitSleep it observes the parked set
// itself, so it is not fooled by a signal from a park that a wake has since
// ended.
func (c *testClock) awaitSleepers(t *testing.T, n int) {
	t.Helper()
	pollUntil(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if len(c.sleepers) != n {
			return false
		}
		for drained := false; !drained; {
			select {
			case <-c.sleepSignal:
			default:
				drained = true
			}
		}
		return true
	}, fmt.Sprintf("exactly %d parked sleepers", n))
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Sleep(ctx context.Context, d time.Duration) {
	c.mu.Lock()
	c.waitList = append(c.waitList, d)
	parked := c.parked
	if !parked {
		c.now = c.now.Add(d)
	}
	barrier := c.barrier
	var release chan struct{}
	if parked {
		release = make(chan struct{})
		c.sleepers = append(c.sleepers, release)
	}
	// Sent under mu so a sleeper listed in c.sleepers has already attempted to signal,
	// which awaitSleepers relies on when it drains the signals.
	if c.sleepSignal != nil {
		select {
		case c.sleepSignal <- struct{}{}:
		default:
		}
	}
	onSleep := c.onSleep
	c.mu.Unlock()

	if onSleep != nil {
		onSleep()
	}

	if !parked {
		return
	}
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, ch := range c.sleepers {
			if ch == release {
				c.sleepers = append(c.sleepers[:i], c.sleepers[i+1:]...)
				return
			}
		}
	}()
	select {
	case <-barrier:
	case <-release:
	case <-ctx.Done():
	}
}

// --- scriptedRunner self-tests ---

func TestScriptedRunnerRevisionScripting(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1", "rev2"}, resolveAt: 3, resolveErr: errors.New("boom")}
	ctx := context.Background()

	tip, err := r.ResolveTip(ctx)
	if err != nil || tip.Revision != "rev1" {
		t.Fatalf("call 1 = (%+v, %v), want (rev1, nil)", tip, err)
	}
	tip, err = r.ResolveTip(ctx)
	if err != nil || tip.Revision != "rev2" {
		t.Fatalf("call 2 = (%+v, %v), want (rev2, nil)", tip, err)
	}
	// call 3 hits resolveAt
	tip, err = r.ResolveTip(ctx)
	if err == nil || tip.Revision != "" {
		t.Fatalf("call 3 = (%+v, %v), want (zero Tip, err) per resolveAt", tip, err)
	}
	if r.resolveCount() != 3 {
		t.Fatalf("resolveCount = %d, want 3", r.resolveCount())
	}
}

func TestScriptedRunnerRevisionRepeatsLastAfterExhaustion(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		tip, err := r.ResolveTip(ctx)
		if err != nil || tip.Revision != "rev1" {
			t.Fatalf("call %d = (%+v, %v), want (rev1, nil)", i+1, tip, err)
		}
	}
}

// TestScriptedRunnerSelfPathScripting scripts revisions alongside selfPaths
// so ResolveTip's resolve half succeeds and its self half is actually
// reached each call.
func TestScriptedRunnerSelfPathScripting(t *testing.T) {
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		selfPaths: []string{"/nix/store/a", "/nix/store/b"}, selfErrAt: 3, selfErr: errors.New("selfboom"),
	}
	ctx := context.Background()

	tip, err := r.ResolveTip(ctx)
	if err != nil || tip.SelfPath != "/nix/store/a" {
		t.Fatalf("call 1 = (%+v, %v), want (/nix/store/a, nil)", tip, err)
	}
	tip, err = r.ResolveTip(ctx)
	if err != nil || tip.SelfPath != "/nix/store/b" {
		t.Fatalf("call 2 = (%+v, %v), want (/nix/store/b, nil)", tip, err)
	}
	tip, err = r.ResolveTip(ctx)
	var se *SelfEvalError
	if !errors.As(err, &se) || tip.SelfPath != "" {
		t.Fatalf("call 3 = (%+v, %v), want (empty SelfPath, *SelfEvalError) per selfErrAt", tip, err)
	}
	if r.selfCount() != 3 {
		t.Fatalf("selfCount = %d, want 3", r.selfCount())
	}
}

// TestScriptedRunnerSelfPathUnscriptedNeverServed asserts ResolveTip never
// touches the self half at all when a test scripts none of selfPaths,
// selfErrAt, or onSelf — selfCalls stays 0 rather than counting a call that
// only served a zero value.
func TestScriptedRunnerSelfPathUnscriptedNeverServed(t *testing.T) {
	r := &scriptedRunner{}
	tip, err := r.ResolveTip(context.Background())
	if err != nil || tip.SelfPath != "" {
		t.Fatalf("ResolveTip with no scripted self path = (%+v, %v), want (empty SelfPath, nil)", tip, err)
	}
	if r.selfCount() != 0 {
		t.Fatalf("selfCount = %d, want 0 (self half never scripted, never served)", r.selfCount())
	}
}

// An all-unset scriptedRunner (no results, byKind, or revisions) must
// return zero values rather than panicking on the empty-list index.
func TestScriptedRunnerAllUnsetYieldsZeroValuesNotPanic(t *testing.T) {
	r := &scriptedRunner{}
	res, err := r.run(context.Background(), ChildRequest{Slot: 0})
	if err != nil || res.Exit != 0 {
		t.Fatalf("run with nothing scripted = (%+v, %v), want (zero ChildResult, nil)", res, err)
	}
	tip, err := r.ResolveTip(context.Background())
	if err != nil || tip.Revision != "" {
		t.Fatalf("ResolveTip with nothing scripted = (%+v, %v), want (zero Tip, nil)", tip, err)
	}
}

func TestScriptedRunnerResultsSharedSequence(t *testing.T) {
	r := &scriptedRunner{results: []ChildResult{{Exit: 0}, {Exit: 3}}}
	ctx := context.Background()

	res, err := r.run(ctx, ChildRequest{Slot: 0, Kind: KindOf(dispatchkind.Work)})
	if err != nil || res.Exit != 0 {
		t.Fatalf("call 1 = (%+v, %v), want Exit 0", res, err)
	}
	res, err = r.run(ctx, ChildRequest{Slot: 1, Kind: KindOf(dispatchkind.Work)})
	if err != nil || res.Exit != 3 {
		t.Fatalf("call 2 = (%+v, %v), want Exit 3", res, err)
	}
	// exhausted: repeats last
	res, err = r.run(ctx, ChildRequest{Slot: 0, Kind: KindOf(dispatchkind.Work)})
	if err != nil || res.Exit != 3 {
		t.Fatalf("call 3 = (%+v, %v), want Exit 3 (repeat)", res, err)
	}
	if r.runCount() != 3 {
		t.Fatalf("runCount = %d, want 3", r.runCount())
	}
	calls := r.calls()
	if len(calls) != 3 || calls[0].Slot != 0 || calls[1].Slot != 1 {
		t.Fatalf("calls = %+v, want 3 recorded runCalls", calls)
	}
}

func TestScriptedRunnerRunErrAt(t *testing.T) {
	r := &scriptedRunner{
		results:  []ChildResult{{Exit: 0}},
		runErrAt: 2,
		runErr:   errors.New("runboom"),
	}
	ctx := context.Background()
	if _, err := r.run(ctx, ChildRequest{Slot: 0}); err != nil {
		t.Fatalf("call 1 err = %v, want nil", err)
	}
	if _, err := r.run(ctx, ChildRequest{Slot: 0}); err == nil {
		t.Fatalf("call 2 err = nil, want runErr")
	}
}

func TestScriptedRunnerByKindScripting(t *testing.T) {
	r := &scriptedRunner{
		byKind: map[Kind][]ChildResult{
			KindOf(dispatchkind.Work):     {{Exit: 0}, {Exit: 1}},
			KindOf(dispatchkind.Research): {{Exit: 9}},
		},
	}
	ctx := context.Background()

	res, _ := r.run(ctx, ChildRequest{Slot: 0, Kind: KindOf(dispatchkind.Work)})
	if res.Exit != 0 {
		t.Fatalf("dispatch call 1 exit = %d, want 0", res.Exit)
	}
	res, _ = r.run(ctx, ChildRequest{Slot: 0, Kind: KindOf(dispatchkind.Research)})
	if res.Exit != 9 {
		t.Fatalf("research call 1 exit = %d, want 9", res.Exit)
	}
	res, _ = r.run(ctx, ChildRequest{Slot: 0, Kind: KindOf(dispatchkind.Work)})
	if res.Exit != 1 {
		t.Fatalf("dispatch call 2 exit = %d, want 1", res.Exit)
	}
	// dispatch queue exhausted: repeats last
	res, _ = r.run(ctx, ChildRequest{Slot: 0, Kind: KindOf(dispatchkind.Work)})
	if res.Exit != 1 {
		t.Fatalf("dispatch call 3 exit = %d, want 1 (repeat)", res.Exit)
	}
	if r.kindCount(KindOf(dispatchkind.Work)) != 3 {
		t.Fatalf("kindCount(dispatch) = %d, want 3", r.kindCount(KindOf(dispatchkind.Work)))
	}
	if r.kindCount(KindOf(dispatchkind.Research)) != 1 {
		t.Fatalf("kindCount(research) = %d, want 1", r.kindCount(KindOf(dispatchkind.Research)))
	}
}

func TestScriptedRunnerByKindWinsOverResults(t *testing.T) {
	r := &scriptedRunner{
		byKind:  map[Kind][]ChildResult{KindOf(dispatchkind.Work): {{Exit: 5}}},
		results: []ChildResult{{Exit: 2}},
	}
	res, _ := r.run(context.Background(), ChildRequest{Slot: 0, Kind: KindOf(dispatchkind.Work)})
	if res.Exit != 5 {
		t.Fatalf("exit = %d, want 5 (byKind wins over results)", res.Exit)
	}
}

func TestScriptedRunnerRunErrAtWinsOverByKind(t *testing.T) {
	r := &scriptedRunner{
		byKind:   map[Kind][]ChildResult{KindOf(dispatchkind.Work): {{Exit: 5}}},
		runErrAt: 1,
		runErr:   errors.New("runboom"),
	}
	res, err := r.run(context.Background(), ChildRequest{Slot: 0, Kind: KindOf(dispatchkind.Work)})
	if err == nil || res.Exit != 0 {
		t.Fatalf("= (%+v, %v), want (zero ChildResult, runErr) (runErrAt wins over byKind)", res, err)
	}
}

// A StartChild call for a slot outside holdSlots' range must error rather
// than block forever receiving from a nil release channel.
func TestScriptedRunnerStartChildOutOfRangeSlotErrors(t *testing.T) {
	r := &scriptedRunner{}
	r.holdSlots(1)
	_, err := r.run(context.Background(), ChildRequest{Slot: 1})
	if err == nil {
		t.Fatalf("StartChild for out-of-range slot 1 err = nil, want an error")
	}
}

func TestScriptedRunnerHoldSlots(t *testing.T) {
	r := &scriptedRunner{}
	r.holdSlots(2)
	ctx := context.Background()
	done := make(chan ChildResult, 2)

	go func() {
		res, _ := r.run(ctx, ChildRequest{Slot: 0})
		done <- res
	}()
	go func() {
		res, _ := r.run(ctx, ChildRequest{Slot: 1})
		done <- res
	}()

	seen := map[int]bool{}
	seen[r.awaitStart(t)] = true
	seen[r.awaitStart(t)] = true
	if !seen[0] || !seen[1] {
		t.Fatalf("started slots = %v, want both 0 and 1", seen)
	}
	if peak := r.peakConcurrency(); peak != 2 {
		t.Fatalf("peakConcurrency = %d, want 2 (both held open at once)", peak)
	}

	r.releaseSlot(t, 0, ChildResult{Exit: 11})
	r.releaseSlot(t, 1, ChildResult{Exit: 22})

	got1 := <-done
	got2 := <-done
	if got1.Exit+got2.Exit != 33 {
		t.Fatalf("released results = %d and %d, want 11 and 22 in some order", got1.Exit, got2.Exit)
	}
}

func TestScriptedRunnerFireOnIssueCallsCapturedHook(t *testing.T) {
	r := &scriptedRunner{results: []ChildResult{{}}}
	var got string
	r.holdSlots(1)
	go func() {
		_, _ = r.run(context.Background(), ChildRequest{Slot: 0, OnRecord: func(rec Record) { got, _ = rec.Key.Fields() }})
	}()
	r.awaitStart(t)
	r.fireOnIssue(t, 0, "issue-42")
	if got != "issue-42" {
		t.Fatalf("OnRecord fired with %q, want issue-42", got)
	}
	r.releaseSlot(t, 0, ChildResult{})
}

func TestScriptedRunnerOnResolveHook(t *testing.T) {
	var seenCall int
	r := &scriptedRunner{
		revisions: []string{"rev1"},
		onResolve: func(ctx context.Context, call int) error {
			seenCall = call
			if call == 2 {
				return errors.New("hookboom")
			}
			return nil
		},
	}
	ctx := context.Background()
	if _, err := r.ResolveTip(ctx); err != nil {
		t.Fatalf("call 1 err = %v, want nil", err)
	}
	if seenCall != 1 {
		t.Fatalf("seenCall = %d, want 1", seenCall)
	}
	if _, err := r.ResolveTip(ctx); err == nil || err.Error() != "hookboom" {
		t.Fatalf("call 2 err = %v, want hookboom (hook short-circuits, scripted value never consulted)", err)
	}
}

func TestScriptedRunnerOnSelfHook(t *testing.T) {
	var seenRevision string
	r := &scriptedRunner{
		revisions: []string{"rev7"},
		selfPaths: []string{"/nix/store/x"},
		onSelf: func(ctx context.Context, call int, revision string) error {
			seenRevision = revision
			return errors.New("selfhookboom")
		},
	}
	_, err := r.ResolveTip(context.Background())
	var se *SelfEvalError
	if !errors.As(err, &se) || se.Error() != "selfhookboom" {
		t.Fatalf("err = %v, want *SelfEvalError wrapping selfhookboom", err)
	}
	if seenRevision != "rev7" {
		t.Fatalf("seenRevision = %q, want rev7", seenRevision)
	}
}

func TestScriptedRunnerOnStartHook(t *testing.T) {
	var seenSlot int
	r := &scriptedRunner{
		results: []ChildResult{{Exit: 0}},
		onStart: func(ctx context.Context, req ChildRequest) error {
			seenSlot = req.Slot
			return errors.New("startboom")
		},
	}
	_, err := r.run(context.Background(), ChildRequest{Slot: 3})
	if err == nil || err.Error() != "startboom" {
		t.Fatalf("err = %v, want startboom", err)
	}
	if seenSlot != 3 {
		t.Fatalf("seenSlot = %d, want 3", seenSlot)
	}
}

// --- testClock self-tests ---

func TestTestClockDefaultSleepAdvancesNow(t *testing.T) {
	c := &testClock{now: time.Unix(0, 0).UTC()}
	c.Sleep(context.Background(), 5*time.Second)
	if got, want := c.Now(), time.Unix(5, 0).UTC(); !got.Equal(want) {
		t.Fatalf("Now = %v, want %v", got, want)
	}
	if c.waitCount() != 1 || c.waits()[0] != 5*time.Second {
		t.Fatalf("waits = %v, want [5s]", c.waits())
	}
}

func TestTestClockParkedBlocksUntilWake(t *testing.T) {
	c := &testClock{}
	c.park()
	done := make(chan struct{})
	go func() {
		c.Sleep(context.Background(), time.Second)
		close(done)
	}()

	select {
	case <-done:
		t.Fatalf("parked Sleep returned before wake()")
	case <-time.After(50 * time.Millisecond):
	}

	c.wake()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("parked Sleep did not return after wake()")
	}
}

func TestTestClockParkedUnblocksOnContextCancel(t *testing.T) {
	c := &testClock{}
	c.park()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.Sleep(ctx, time.Second)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("parked Sleep did not return after ctx cancel")
	}
}

func TestTestClockStepReleasesAllParkedAtOnceWithFreshBarrier(t *testing.T) {
	c := &testClock{}
	c.park()
	// Sized for both rounds below: n sends in the first round, 1 more in
	// the second.
	c.sleepSignal = make(chan struct{}, 4)
	const n = 3
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			c.Sleep(context.Background(), time.Second)
			done <- struct{}{}
		}()
	}
	c.awaitSleep(t, n)

	want := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	c.step(want)

	for i := 0; i < n; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("step did not release all %d parked sleepers", n)
		}
	}
	if got := c.Now(); !got.Equal(want) {
		t.Fatalf("Now = %v, want %v", got, want)
	}

	// fresh barrier: a subsequent park+step round must work again, not
	// reuse an already-closed channel from the first round.
	done2 := make(chan struct{})
	go func() {
		c.Sleep(context.Background(), time.Second)
		close(done2)
	}()
	c.awaitSleep(t, 1)
	c.step(want.Add(time.Hour))
	select {
	case <-done2:
	case <-time.After(5 * time.Second):
		t.Fatalf("second step round did not release its sleeper: barrier was not refreshed")
	}
}

func TestTestClockSleepSignalDrains(t *testing.T) {
	c := &testClock{now: time.Unix(0, 0).UTC()}
	sig := make(chan struct{}, 4)
	c.sleepSignal = sig
	c.Sleep(context.Background(), time.Second)
	c.Sleep(context.Background(), time.Second)
	c.awaitSleep(t, 2)
}

func TestTestClockOnSleepFires(t *testing.T) {
	c := &testClock{now: time.Unix(0, 0).UTC()}
	var fired int
	c.onSleep = func() { fired++ }
	c.Sleep(context.Background(), time.Second)
	c.Sleep(context.Background(), time.Second)
	if fired != 2 {
		t.Fatalf("onSleep fired %d times, want 2", fired)
	}
}

// setDemand scripts kind's Demand answer from now on, clearing any scripted
// error: Loop tests flip it mid-run to model a tracker gaining work.
func (r *scriptedRunner) setDemand(kind Kind, ready int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.outbox[kind]; ok {
		panic("scriptedRunner: setDemand on a kind already scripted with setOutbox")
	}
	if r.demand == nil {
		r.demand = make(map[Kind]Demand)
	}
	r.demand[kind] = Demand{Ready: ready}
	delete(r.demandErr, kind)
}

// setDemandErr makes kind's Demand fail with err (nil clears it).
func (r *scriptedRunner) setDemandErr(kind Kind, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.demandErr == nil {
		r.demandErr = make(map[Kind]error)
	}
	if err == nil {
		delete(r.demandErr, kind)
		return
	}
	r.demandErr[kind] = err
}

// demandCount reports how many times Demand was called for kind.
func (r *scriptedRunner) demandCount(kind Kind) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.demandCalls[kind]
}

// demandFreshCalls reports the fresh flag of each Demand call for kind.
func (r *scriptedRunner) demandFreshCalls(kind Kind) []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.demandFresh[kind])
}

// Demand answers from setDemand/setDemandErr: an unscripted kind has no
// demand, never an error.
func (r *scriptedRunner) Demand(ctx context.Context, kind Kind, fresh bool, inFlight dispatchkey.Set) (Demand, error) {
	r.mu.Lock()
	if r.demandCalls == nil {
		r.demandCalls = make(map[Kind]int)
		r.demandFresh = make(map[Kind][]bool)
	}
	r.demandCalls[kind]++
	r.demandFresh[kind] = append(r.demandFresh[kind], fresh)
	if r.demandInFlight == nil {
		r.demandInFlight = make(map[Kind][]dispatchkey.Set)
	}
	r.demandInFlight[kind] = append(r.demandInFlight[kind], maps.Clone(inFlight))
	hook := r.onDemand
	r.mu.Unlock()
	if hook != nil {
		hook(ctx, kind)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.demandErr[kind]; err != nil {
		return Demand{}, err
	}
	if keys, ok := r.outbox[kind]; ok {
		var ids []string
		for _, k := range keys {
			if !inFlight[k] {
				ids = append(ids, k.String()+"@bundle")
			}
		}
		return Demand{Ready: len(ids), IDs: ids}, nil
	}
	return r.demand[kind], nil
}

// setOutbox makes kind an outbox-backed kind holding bundles for keys: its
// Demand names (and counts) those the probe's inFlight does not hold, as the
// host outbox does.
func (r *scriptedRunner) setOutbox(kind Kind, keys ...dispatchkey.Key) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.demand[kind]; ok {
		panic("scriptedRunner: setOutbox on a kind already scripted with setDemand")
	}
	if r.outbox == nil {
		r.outbox = make(map[Kind][]dispatchkey.Key)
	}
	r.outbox[kind] = keys
}

// demandInFlightCalls reports the inFlight set of each Demand call for kind.
func (r *scriptedRunner) demandInFlightCalls(kind Kind) []dispatchkey.Set {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.demandInFlight[kind])
}

// awaitDemandInFlight waits for a Demand call on kind whose inFlight holds key.
func (r *scriptedRunner) awaitDemandInFlight(t *testing.T, kind Kind, key dispatchkey.Key) {
	t.Helper()
	pollUntil(t, func() bool {
		for _, set := range r.demandInFlightCalls(kind) {
			if set[key] {
				return true
			}
		}
		return false
	}, fmt.Sprintf("a %v Demand call with %v in flight", kind, key))
}

// awaitClaimedSiblingParked waits out the claim of key (of kind) in a
// two-slot pool whose other slot was parked: the claim wakes that slot,
// whose re-probe holds the key before it parks again. Waiting on both leaves
// no stale park signal for a later awaitSleep to mistake for a fresh park.
func (r *scriptedRunner) awaitClaimedSiblingParked(t *testing.T, clk *testClock, kind Kind, key dispatchkey.Key) {
	t.Helper()
	r.awaitDemandInFlight(t, kind, key)
	clk.awaitSleepers(t, 1)
}

// pollUntil polls cond every millisecond until it holds, failing the test
// naming what after waitForLineTimeout.
func pollUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(waitForLineTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", waitForLineTimeout, what)
		}
		time.Sleep(time.Millisecond)
	}
}

// pickKind is the test view of decide: the kind a free slot would start now.
func (p *pool) pickKind() (Kind, bool) {
	st, ok := p.decide(p.clk.Now()).(start)
	return st.Kind, ok
}

// markNoWork folds one no-work child result into the schedule. Like the
// p.mutate closures elsewhere in these tests, it is setup that writes state,
// not a read of pool state to assert a result.
func (p *pool) markNoWork(kind Kind, now time.Time, jammed bool) {
	result := childEmpty
	if jammed {
		result = childJammed
	}
	p.mutate(func(s *state) []Event {
		s.sched, _ = s.sched.Observe(now, childDone{Kind: kind, Result: result, GateGen: s.sched.gateGenOf(kind)})
		return nil
	})
}

// Prebuild counts the call, then answers through onPrebuild if set and with
// success otherwise, so a test that mismatches SelfProgram reaches the halt
// without scripting a self-update.
func (r *scriptedRunner) Prebuild(ctx context.Context, tip Tip) error {
	r.mu.Lock()
	r.prebuildTips = append(r.prebuildTips, tip)
	call := len(r.prebuildTips)
	r.mu.Unlock()
	if r.onPrebuild != nil {
		return r.onPrebuild(ctx, call, tip)
	}
	return nil
}

// SweepLateMerges records the revision, then answers through onSweep.
func (r *scriptedRunner) SweepLateMerges(ctx context.Context, revision string) error {
	r.mu.Lock()
	r.sweepRevs = append(r.sweepRevs, revision)
	r.mu.Unlock()
	if r.onSweep != nil {
		return r.onSweep(ctx, revision)
	}
	return nil
}

// sweeps returns a copy of every revision SweepLateMerges was called with.
func (r *scriptedRunner) sweeps() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sweepRevs)
}

// prebuildCount is how many times Prebuild was called.
func (r *scriptedRunner) prebuildCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.prebuildTips)
}

// prebuilds returns a copy of every Tip Prebuild was called with, in order.
func (r *scriptedRunner) prebuilds() []Tip {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.prebuildTips)
}

// inheritCall is one recorded InheritChild call.
type inheritCall struct {
	Kind     Kind
	Revision string
	Slot     int
	PID      int
	// Stop and Abort are the request's latches, to see what a resumed pool
	// handed the inherited child.
	Stop, Abort <-chan struct{}
	// CtxErr is the ctx error at the call: a re-attach is never to be aborted
	// by a pool halt.
	CtxErr error
}

func TestScriptedRunnerInheritChildExitedByPID(t *testing.T) {
	r := &scriptedRunner{
		results: []ChildResult{{Exit: 2}},
		exited:  map[int]ChildResult{42: {Exit: 9}},
	}
	var got []Record
	req := ChildRequest{Slot: 1, Kind: KindOf(dispatchkind.Work), Revision: "r1", OnRecord: func(rec Record) { got = append(got, rec) }}

	h, err := r.InheritChild(context.Background(), req, ChildState{PID: 42})
	if err != nil {
		t.Fatalf("InheritChild: %v", err)
	}
	if res, err := h.Wait(nil); err != nil || res.Exit != 9 {
		t.Fatalf("Wait = (%+v, %v), want exit 9", res, err)
	}
	want := []inheritCall{{Kind: KindOf(dispatchkind.Work), Revision: "r1", Slot: 1, PID: 42}}
	if calls := r.inheritCalls(); !slices.Equal(calls, want) {
		t.Fatalf("inheritCalls = %+v, want %+v", calls, want)
	}

	r.mu.Lock()
	push := r.onRecord[1]
	r.mu.Unlock()
	if push == nil {
		t.Fatal("OnRecord not registered for the inherited slot")
	}
	push(Record{Event: "box"})
	if len(got) != 1 {
		t.Fatalf("records delivered = %d, want 1", len(got))
	}

	// A pid with no scripted exit takes the next scripted result.
	h, err = r.InheritChild(context.Background(), ChildRequest{Slot: 0, Kind: KindOf(dispatchkind.Work)}, ChildState{PID: 7})
	if err != nil {
		t.Fatalf("InheritChild: %v", err)
	}
	if res, _ := h.Wait(nil); res.Exit != 2 {
		t.Fatalf("exit = %d, want 2 (scripted result)", res.Exit)
	}
}

func TestScriptedHandleStateNamesEachChild(t *testing.T) {
	r := &scriptedRunner{results: []ChildResult{{Exit: 0}}}
	req := ChildRequest{Kind: KindOf(dispatchkind.Work), Revision: "r1"}
	a, err := r.StartChild(context.Background(), req)
	if err != nil {
		t.Fatalf("StartChild: %v", err)
	}
	b, err := r.StartChild(context.Background(), req)
	if err != nil {
		t.Fatalf("StartChild: %v", err)
	}
	sa, sb := a.State(), b.State()
	if sa.PID == 0 || sb.PID == 0 || sa.PID == sb.PID {
		t.Fatalf("pids = %d, %d, want distinct and nonzero", sa.PID, sb.PID)
	}
	if sa.ReportFD <= 0 || sb.ReportFD <= 0 || sa.ReportFD == sb.ReportFD {
		t.Fatalf("report fds = %d, %d, want distinct and positive", sa.ReportFD, sb.ReportFD)
	}

	// An inherited child keeps the pid it was handed, so a handoff round-trips.
	c, err := r.InheritChild(context.Background(), req, ChildState{PID: sa.PID, ReportFD: 9})
	if err != nil {
		t.Fatalf("InheritChild: %v", err)
	}
	if got := c.State(); got.PID != sa.PID || got.ReportFD != 9 {
		t.Fatalf("inherited State = %+v, want pid %d fd 9", got, sa.PID)
	}
}
