package daemon

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// scriptedRunner is the package's one scriptable Runner double, replacing
// the 15 ad-hoc fakes this package used to grow one per test file. Every
// scripting field below is written once, before Loop/the pool starts, and
// never mutated again — mu does not guard them, only the call-recording
// fields and the concurrent reads several slot goroutines make of them
// once a multi-slot test is under way.
type scriptedRunner struct {
	// revisions/resolveAt/resolveErr: one revision per ResolveRevision
	// call, last value repeating once exhausted; resolveAt is a 1-based
	// call index that returns resolveErr instead.
	revisions  []string
	resolveAt  int
	resolveErr error

	// selfPaths/selfErrAt/selfErr: the same convention as revisions, but an
	// empty selfPaths yields ("", nil) rather than panicking, since most
	// tests never script a self-path at all (Config.SelfProgram left
	// empty short-circuits the seam before it's ever called).
	selfPaths []string
	selfErrAt int
	selfErr   error

	// Result scripting, most specific winning: bySlot (keyed by the
	// caller's own per-slot call index — needed wherever several slots
	// call RunChild concurrently, since a shared call counter would hand
	// a result to whichever slot happened to call next) beats byKind
	// (keyed by the caller's own per-kind call index, for two
	// independently-emptying dispatch/research queues) beats the single
	// shared results sequence in total call order. runErrAt
	// is a 1-based *total* call index (across every slot and kind) that
	// wins over all three when it matches.
	bySlot       map[int][]ChildResult
	byKind       map[Kind][]ChildResult
	results      []ChildResult
	runErrAt     int
	runErr       error
	runErrIssues []string

	// Hooks fire before the scripted answer is computed, at each seam
	// crossing, so a test can cancel a context or read a clock at exactly
	// that instant. A non-nil error return short-circuits the seam with
	// that error and no scripted value.
	onResolve func(ctx context.Context, call int) error
	onSelf    func(ctx context.Context, call int, revision string) error
	onStart   func(ctx context.Context, req ChildRequest) error

	mu           sync.Mutex
	resolveCalls int
	selfCalls    int
	runCalls     []runCall
	kindCalls    map[Kind]int
	slotCalls    map[int]int
	inFlight     int
	peak         int
	onIssue      map[int]func(string)

	// release/started: installed by holdSlots for tests that need to hold
	// several children open at once and release them one at a time. A nil
	// release (the default) means RunChild never blocks here at all.
	release map[int]chan ChildResult
	started chan int
}

// holdSlots switches RunChild into blocking mode for slots 0..n-1: each
// call records itself, fires onStart, announces its slot on started, then
// blocks until releaseSlot sends its result. Buffered release channels (1
// each) and a started channel sized to n mean no slot's goroutine ever
// blocks sending on a single wave — only on the intentional release wait —
// so a test that spans several waves must drain each wave before the next.
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

// releaseSlot hands slot's blocked RunChild call its result. It fails the
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
// req.OnIssue, the one shape every RunChild call site that wants a per-slot
// issue announcement needs.
func (r *scriptedRunner) announceEachSlot() {
	r.onStart = func(ctx context.Context, req ChildRequest) error {
		if req.OnIssue != nil {
			req.OnIssue(fmt.Sprintf("issue-%d", req.Slot))
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

// fireOnIssue calls slot's most recently captured RunChild call's
// OnIssue, simulating a live Box announcement while that child is still in
// flight. Failing rather than silently no-op-ing on a nil hook catches a
// test that fires before the slot's RunChild call has actually happened.
func (r *scriptedRunner) fireOnIssue(t *testing.T, slot int, issue string) {
	t.Helper()
	r.mu.Lock()
	fn := r.onIssue[slot]
	r.mu.Unlock()
	if fn == nil {
		t.Fatalf("scriptedRunner: fireOnIssue: no OnIssue captured for slot %d", slot)
	}
	fn(issue)
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

// peakConcurrency is the high-water mark of slots simultaneously inside
// RunChild, tracked across the call's whole body (not just the blocking
// holdSlots wait) so it reflects genuine overlap however the result is
// produced.
func (r *scriptedRunner) peakConcurrency() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peak
}

// calls returns a copy of every recorded RunChild call, in order.
func (r *scriptedRunner) calls() []runCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]runCall, len(r.runCalls))
	copy(out, r.runCalls)
	return out
}

func (r *scriptedRunner) ResolveRevision(ctx context.Context) (string, error) {
	r.mu.Lock()
	r.resolveCalls++
	call := r.resolveCalls
	r.mu.Unlock()

	if r.onResolve != nil {
		if err := r.onResolve(ctx, call); err != nil {
			return "", err
		}
	}

	if r.resolveAt != 0 && call == r.resolveAt {
		return "", r.resolveErr
	}
	// An empty revisions yields ("", nil) rather than panicking, same as
	// SelfPath's guard above: most tests never script a revision at all.
	if len(r.revisions) == 0 {
		return "", nil
	}
	idx := call - 1
	if idx >= len(r.revisions) {
		idx = len(r.revisions) - 1
	}
	return r.revisions[idx], nil
}

func (r *scriptedRunner) SelfPath(ctx context.Context, revision string) (string, error) {
	r.mu.Lock()
	r.selfCalls++
	call := r.selfCalls
	r.mu.Unlock()

	if r.onSelf != nil {
		if err := r.onSelf(ctx, call, revision); err != nil {
			return "", err
		}
	}

	if r.selfErrAt != 0 && call == r.selfErrAt {
		return "", r.selfErr
	}
	if len(r.selfPaths) == 0 {
		return "", nil
	}
	idx := call - 1
	if idx >= len(r.selfPaths) {
		idx = len(r.selfPaths) - 1
	}
	return r.selfPaths[idx], nil
}

// pickScripted clamps idx to the last index of list, mirroring every
// scripting field's "last value repeats once exhausted" rule in one place.
// An empty list yields the zero ChildResult rather than panicking, same as
// ResolveRevision/SelfPath's empty guards: an all-unset scriptedRunner is a
// valid, if boring, script.
func pickScripted(list []ChildResult, idx int) ChildResult {
	if len(list) == 0 {
		return ChildResult{}
	}
	if idx >= len(list) {
		idx = len(list) - 1
	}
	return list[idx]
}

func (r *scriptedRunner) RunChild(ctx context.Context, req ChildRequest) (ChildResult, error) {
	r.mu.Lock()
	r.runCalls = append(r.runCalls, runCall{Kind: req.Kind, Revision: req.Revision, Slot: req.Slot})
	total := len(r.runCalls)
	if r.kindCalls == nil {
		r.kindCalls = make(map[Kind]int)
	}
	kindIdx := r.kindCalls[req.Kind]
	r.kindCalls[req.Kind]++
	if r.slotCalls == nil {
		r.slotCalls = make(map[int]int)
	}
	slotIdx := r.slotCalls[req.Slot]
	r.slotCalls[req.Slot]++
	if r.onIssue == nil {
		r.onIssue = make(map[int]func(string))
	}
	r.onIssue[req.Slot] = req.OnIssue
	r.inFlight++
	if r.inFlight > r.peak {
		r.peak = r.inFlight
	}
	release := r.release
	started := r.started
	r.mu.Unlock()

	// The mutex must not be held across a hook call or the blocking
	// holdSlots wait below: several slots call RunChild at once in the
	// start-gate and dual-kind tests, and holding mu here would serialize
	// what those tests need to run genuinely concurrently.
	defer func() {
		r.mu.Lock()
		r.inFlight--
		r.mu.Unlock()
	}()

	if r.onStart != nil {
		if err := r.onStart(ctx, req); err != nil {
			return ChildResult{}, err
		}
	}

	if release != nil {
		ch := release[req.Slot]
		if ch == nil {
			// A nil receive here blocks forever, unlike awaitStart/
			// awaitSleep's explicit five-second bound; fail the call
			// instead of hanging the test on a slot outside holdSlots' range.
			return ChildResult{}, fmt.Errorf("scriptedRunner: RunChild: slot %d out of holdSlots range", req.Slot)
		}
		if started != nil {
			started <- req.Slot
		}
		return <-ch, nil
	}

	if r.runErrAt != 0 && total == r.runErrAt {
		return ChildResult{Issues: r.runErrIssues}, r.runErr
	}
	if list, ok := r.bySlot[req.Slot]; ok {
		return pickScripted(list, slotIdx), nil
	}
	if list, ok := r.byKind[req.Kind]; ok {
		return pickScripted(list, kindIdx), nil
	}
	return pickScripted(r.results, total-1), nil
}

// testClock is the package's one scriptable Clock double, covering every
// sleep behaviour the 5 ad-hoc fake clocks it replaces needed: an instant
// default advance, a parked mode that blocks until woken or cancelled, a
// step mode that releases every parked sleeper at once over a fresh
// barrier so N concurrent sleeps never additively stack, and a
// sleep-entry signal plus onSleep hook for tests that need to know the
// instant a slot genuinely idles.
type testClock struct {
	mu       sync.Mutex
	now      time.Time
	waitList []time.Duration

	// parked, once true (via park), makes Sleep block instead of
	// instantly advancing now; barrierClosed tracks whether the current
	// barrier generation has already been closed, so wake (which may be
	// called when no one has parked yet) and step (which always installs
	// a fresh generation) never double-close the same channel.
	parked        bool
	barrier       chan struct{}
	barrierClosed bool

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

// wake releases every currently parked Sleep call without changing now and
// without installing a fresh barrier — the one-shot "let everything through
// from here on" gesture, for unparking every slot asleep in a shut window
// without cancelling the pool.
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
	sig := c.sleepSignal
	onSleep := c.onSleep
	c.mu.Unlock()

	if sig != nil {
		select {
		case sig <- struct{}{}:
		default:
		}
	}
	if onSleep != nil {
		onSleep()
	}

	if !parked {
		return
	}
	select {
	case <-barrier:
	case <-ctx.Done():
	}
}

// --- scriptedRunner self-tests ---

func TestScriptedRunnerRevisionScripting(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1", "rev2"}, resolveAt: 3, resolveErr: errors.New("boom")}
	ctx := context.Background()

	got, err := r.ResolveRevision(ctx)
	if err != nil || got != "rev1" {
		t.Fatalf("call 1 = (%q, %v), want (rev1, nil)", got, err)
	}
	got, err = r.ResolveRevision(ctx)
	if err != nil || got != "rev2" {
		t.Fatalf("call 2 = (%q, %v), want (rev2, nil)", got, err)
	}
	// exhausted: repeats the last value
	got, err = r.ResolveRevision(ctx)
	if err == nil || got != "" {
		t.Fatalf("call 3 = (%q, %v), want (\"\", err) per resolveAt", got, err)
	}
	if r.resolveCount() != 3 {
		t.Fatalf("resolveCount = %d, want 3", r.resolveCount())
	}
}

func TestScriptedRunnerRevisionRepeatsLastAfterExhaustion(t *testing.T) {
	r := &scriptedRunner{revisions: []string{"rev1"}}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		got, err := r.ResolveRevision(ctx)
		if err != nil || got != "rev1" {
			t.Fatalf("call %d = (%q, %v), want (rev1, nil)", i+1, got, err)
		}
	}
}

func TestScriptedRunnerSelfPathScripting(t *testing.T) {
	r := &scriptedRunner{selfPaths: []string{"/nix/store/a", "/nix/store/b"}, selfErrAt: 3, selfErr: errors.New("selfboom")}
	ctx := context.Background()

	got, err := r.SelfPath(ctx, "rev1")
	if err != nil || got != "/nix/store/a" {
		t.Fatalf("call 1 = (%q, %v), want (/nix/store/a, nil)", got, err)
	}
	got, err = r.SelfPath(ctx, "rev1")
	if err != nil || got != "/nix/store/b" {
		t.Fatalf("call 2 = (%q, %v), want (/nix/store/b, nil)", got, err)
	}
	got, err = r.SelfPath(ctx, "rev1")
	if err == nil || got != "" {
		t.Fatalf("call 3 = (%q, %v), want (\"\", err) per selfErrAt", got, err)
	}
	if r.selfCount() != 3 {
		t.Fatalf("selfCount = %d, want 3", r.selfCount())
	}
}

func TestScriptedRunnerSelfPathEmptyYieldsNoError(t *testing.T) {
	r := &scriptedRunner{}
	got, err := r.SelfPath(context.Background(), "rev1")
	if err != nil || got != "" {
		t.Fatalf("SelfPath with no scripted paths = (%q, %v), want (\"\", nil)", got, err)
	}
}

// An all-unset scriptedRunner (no results, byKind, bySlot, or revisions)
// must return zero values rather than panicking on the empty-list index.
func TestScriptedRunnerAllUnsetYieldsZeroValuesNotPanic(t *testing.T) {
	r := &scriptedRunner{}
	res, err := r.RunChild(context.Background(), ChildRequest{Slot: 0})
	if err != nil || res.Exit != 0 || res.Issues != nil {
		t.Fatalf("RunChild with nothing scripted = (%+v, %v), want (zero ChildResult, nil)", res, err)
	}
	rev, err := r.ResolveRevision(context.Background())
	if err != nil || rev != "" {
		t.Fatalf("ResolveRevision with nothing scripted = (%q, %v), want (\"\", nil)", rev, err)
	}
}

func TestScriptedRunnerResultsSharedSequence(t *testing.T) {
	r := &scriptedRunner{results: []ChildResult{{Exit: 0}, {Exit: 3}}}
	ctx := context.Background()

	res, err := r.RunChild(ctx, ChildRequest{Slot: 0, Kind: KindDispatch})
	if err != nil || res.Exit != 0 {
		t.Fatalf("call 1 = (%+v, %v), want Exit 0", res, err)
	}
	res, err = r.RunChild(ctx, ChildRequest{Slot: 1, Kind: KindDispatch})
	if err != nil || res.Exit != 3 {
		t.Fatalf("call 2 = (%+v, %v), want Exit 3", res, err)
	}
	// exhausted: repeats last
	res, err = r.RunChild(ctx, ChildRequest{Slot: 0, Kind: KindDispatch})
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
		results:      []ChildResult{{Exit: 0}},
		runErrAt:     2,
		runErr:       errors.New("runboom"),
		runErrIssues: []string{"issue-9"},
	}
	ctx := context.Background()
	if _, err := r.RunChild(ctx, ChildRequest{Slot: 0}); err != nil {
		t.Fatalf("call 1 err = %v, want nil", err)
	}
	res, err := r.RunChild(ctx, ChildRequest{Slot: 0})
	if err == nil {
		t.Fatalf("call 2 err = nil, want runErr")
	}
	if len(res.Issues) != 1 || res.Issues[0] != "issue-9" {
		t.Fatalf("call 2 issues = %v, want [issue-9]", res.Issues)
	}
}

func TestScriptedRunnerByKindScripting(t *testing.T) {
	r := &scriptedRunner{
		byKind: map[Kind][]ChildResult{
			KindDispatch: {{Exit: 0}, {Exit: 1}},
			KindResearch: {{Exit: 9}},
		},
	}
	ctx := context.Background()

	res, _ := r.RunChild(ctx, ChildRequest{Slot: 0, Kind: KindDispatch})
	if res.Exit != 0 {
		t.Fatalf("dispatch call 1 exit = %d, want 0", res.Exit)
	}
	res, _ = r.RunChild(ctx, ChildRequest{Slot: 0, Kind: KindResearch})
	if res.Exit != 9 {
		t.Fatalf("research call 1 exit = %d, want 9", res.Exit)
	}
	res, _ = r.RunChild(ctx, ChildRequest{Slot: 0, Kind: KindDispatch})
	if res.Exit != 1 {
		t.Fatalf("dispatch call 2 exit = %d, want 1", res.Exit)
	}
	// dispatch queue exhausted: repeats last
	res, _ = r.RunChild(ctx, ChildRequest{Slot: 0, Kind: KindDispatch})
	if res.Exit != 1 {
		t.Fatalf("dispatch call 3 exit = %d, want 1 (repeat)", res.Exit)
	}
	if r.kindCount(KindDispatch) != 3 {
		t.Fatalf("kindCount(dispatch) = %d, want 3", r.kindCount(KindDispatch))
	}
	if r.kindCount(KindResearch) != 1 {
		t.Fatalf("kindCount(research) = %d, want 1", r.kindCount(KindResearch))
	}
}

func TestScriptedRunnerBySlotScripting(t *testing.T) {
	r := &scriptedRunner{
		bySlot: map[int][]ChildResult{
			0: {{Exit: 0}, {Exit: 2}},
			1: {{Exit: 7}},
		},
	}
	ctx := context.Background()

	res, _ := r.RunChild(ctx, ChildRequest{Slot: 0})
	if res.Exit != 0 {
		t.Fatalf("slot0 call1 exit = %d, want 0", res.Exit)
	}
	res, _ = r.RunChild(ctx, ChildRequest{Slot: 1})
	if res.Exit != 7 {
		t.Fatalf("slot1 call1 exit = %d, want 7", res.Exit)
	}
	res, _ = r.RunChild(ctx, ChildRequest{Slot: 0})
	if res.Exit != 2 {
		t.Fatalf("slot0 call2 exit = %d, want 2", res.Exit)
	}
}

// bySlot must win over byKind and results when more than one is set, per
// the acceptance criteria's "most specific winning" ordering.
func TestScriptedRunnerBySlotWinsOverByKindAndResults(t *testing.T) {
	r := &scriptedRunner{
		bySlot:  map[int][]ChildResult{0: {{Exit: 42}}},
		byKind:  map[Kind][]ChildResult{KindDispatch: {{Exit: 1}}},
		results: []ChildResult{{Exit: 2}},
	}
	res, _ := r.RunChild(context.Background(), ChildRequest{Slot: 0, Kind: KindDispatch})
	if res.Exit != 42 {
		t.Fatalf("exit = %d, want 42 (bySlot wins)", res.Exit)
	}
}

func TestScriptedRunnerByKindWinsOverResults(t *testing.T) {
	r := &scriptedRunner{
		byKind:  map[Kind][]ChildResult{KindDispatch: {{Exit: 5}}},
		results: []ChildResult{{Exit: 2}},
	}
	res, _ := r.RunChild(context.Background(), ChildRequest{Slot: 0, Kind: KindDispatch})
	if res.Exit != 5 {
		t.Fatalf("exit = %d, want 5 (byKind wins over results)", res.Exit)
	}
}

// A RunChild call for a slot outside holdSlots' range must error rather
// than block forever receiving from a nil release channel.
func TestScriptedRunnerRunChildOutOfRangeSlotErrors(t *testing.T) {
	r := &scriptedRunner{}
	r.holdSlots(1)
	_, err := r.RunChild(context.Background(), ChildRequest{Slot: 1})
	if err == nil {
		t.Fatalf("RunChild for out-of-range slot 1 err = nil, want an error")
	}
}

func TestScriptedRunnerHoldSlots(t *testing.T) {
	r := &scriptedRunner{}
	r.holdSlots(2)
	ctx := context.Background()
	done := make(chan ChildResult, 2)

	go func() {
		res, _ := r.RunChild(ctx, ChildRequest{Slot: 0})
		done <- res
	}()
	go func() {
		res, _ := r.RunChild(ctx, ChildRequest{Slot: 1})
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
		_, _ = r.RunChild(context.Background(), ChildRequest{Slot: 0, OnIssue: func(issue string) { got = issue }})
	}()
	r.awaitStart(t)
	r.fireOnIssue(t, 0, "issue-42")
	if got != "issue-42" {
		t.Fatalf("OnIssue fired with %q, want issue-42", got)
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
	if _, err := r.ResolveRevision(ctx); err != nil {
		t.Fatalf("call 1 err = %v, want nil", err)
	}
	if seenCall != 1 {
		t.Fatalf("seenCall = %d, want 1", seenCall)
	}
	if _, err := r.ResolveRevision(ctx); err == nil || err.Error() != "hookboom" {
		t.Fatalf("call 2 err = %v, want hookboom (hook short-circuits, scripted value never consulted)", err)
	}
}

func TestScriptedRunnerOnSelfHook(t *testing.T) {
	var seenRevision string
	r := &scriptedRunner{
		selfPaths: []string{"/nix/store/x"},
		onSelf: func(ctx context.Context, call int, revision string) error {
			seenRevision = revision
			return errors.New("selfhookboom")
		},
	}
	_, err := r.SelfPath(context.Background(), "rev7")
	if err == nil || err.Error() != "selfhookboom" {
		t.Fatalf("err = %v, want selfhookboom", err)
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
	_, err := r.RunChild(context.Background(), ChildRequest{Slot: 3})
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
