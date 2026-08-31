// This file's RunContinuous scenarios drive the Queue seam through *Fake
// (issue #2937) -- see queue_engine_test.go's own file-header comment for
// the line between the two files. A scenario typically constructs both a
// forge.Fake (fc, seeded via SetIssue) and a Fake Queue (fake, given a
// DiscoverReturn/DiscoverFunc): the two aren't the same list wearing two
// hats, even when a scenario's fake.DiscoverReturn happens to name the same
// issue numbers fc carries. fc is the tracker RunContinuous claims against,
// checks blocker/priority/DepsOf state on, and settles against; fake is
// only what Discover returns. Keeping them independent, rather than
// deriving one from the other, is what lets a scenario put fake.Claim out
// of step with fc's real label state on purpose (TestRunContinuous_
// StaleDiscoveryNeverDoubleDispatches's stale-search-result batch is the
// clearest example) -- collapsing them back into one hand-maintained list
// would silently reintroduce the live re-listing this migration removed.
package waves

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
	"spindrift.dev/launcher/internal/testutil"
)

// noopPending is a Pending closure for tests that don't exercise the
// stale-drain heldBack path at all -- reporting-only listing, always
// empty. It ignores the claimed set NewHeadlessQueue's Pending() passes in.
func noopPending(map[string]bool) (int, error) { return 0, nil }

// reportFunc adapts a StaleDrainReport callback into a Queue whose other
// three methods are unused no-ops -- reportStaleDrainReleasingMu's own
// mu-release contract test only exercises ReportStaleDrain.
type reportFunc func(StaleDrainReport)

func (r reportFunc) Discover() (Batch, error)                 { return Batch{}, nil }
func (r reportFunc) Claim(string) error                       { return nil }
func (r reportFunc) Pending() (int, error)                    { return 0, nil }
func (r reportFunc) ReportStaleDrain(report StaleDrainReport) { r(report) }

// fakeWavesClock returns a retry.Clock with a fixed Now and a Sleep that
// records durations into calls, mirroring
// dispatch/retry_test.go's fakeClock for the waves package's own Clock seam
// (issue #2866).
func fakeWavesClock(now time.Time, calls *[]time.Duration) retry.Clock {
	return retry.Clock{
		Now:   func() time.Time { return now },
		Sleep: func(d time.Duration) { *calls = append(*calls, d) },
	}
}

// TestRunContinuous_RefillsFreedSlotWhileOthersRunning verifies the core
// slot-refill behavior (#527 AC1): with MaxParallel=2 and three ready
// issues, the third issue launches into the slot #1 frees while #2 is still
// running — a batch-shaped implementation would deadlock here, since #2
// only unblocks after #3 has already started.
func TestRunContinuous_RefillsFreedSlotWhileOthersRunning(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{label}})

	fake := NewFake()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}}

	fr := runner.NewFake()
	started3 := make(chan struct{})
	release2 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		switch box.Issue {
		case "2":
			<-release2
		case "3":
			close(started3)
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, fake, fresh)
	}()

	select {
	case <-started3:
	case <-time.After(2 * time.Second):
		t.Fatal("issue #3 was never dispatched — slot did not refill while #2 was still running")
	}

	close(release2)

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("RunContinuous: got %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return after #2 was released")
	}

	if len(fr.RunCalls) != 3 {
		t.Fatalf("RunCalls: got %d, want 3", len(fr.RunCalls))
	}

	// drainRefill calls refill() serially at bootstrap (continuous.go), so
	// #1 and #2 claim in batch.Issues order before either Box starts
	// running; #3 claims later, once #1's completion frees a slot for the
	// next refill. The order is deterministic -- no sort needed.
	wantClaims := []string{"1", "2", "3"}
	if !slices.Equal(fake.ClaimCalls, wantClaims) {
		t.Fatalf("ClaimCalls: got %v, want %v", fake.ClaimCalls, wantClaims)
	}
}

// TestRunContinuous_RefillPicksUpIssueUnblockedMidRun verifies #527 AC2: a
// blocked issue's blocker resolving mid-run (merged/closed after dispatch
// started) makes it dispatchable on the very next refill, without a fresh
// invocation.
func TestRunContinuous_RefillPicksUpIssueUnblockedMidRun(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"}) // #2's blocker, unmet at start

	fr := runner.NewFake()
	releaseC := make(chan struct{})
	started2 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		switch box.Issue {
		case "1":
			<-releaseC
		case "2":
			close(started2)
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	fake := NewFake()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{"2": {"3"}},
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, fake, fresh)
	}()

	// #2 is blocked at dispatch start (its blocker is open); MaxParallel=1
	// also means it can't launch until #1's slot frees. The blocker
	// resolves here, while #1 is still in flight, before that slot frees —
	// proving the refill re-checks readiness against fresh state rather
	// than a snapshot taken at startup.
	fc.SetIssue(forge.Issue{Number: "3", State: forge.IssueClosed})
	close(releaseC)

	select {
	case <-started2:
	case <-time.After(2 * time.Second):
		t.Fatal("issue #2 was never dispatched after its blocker resolved mid-run")
	}

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("RunContinuous: got %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return")
	}

	if len(fr.RunCalls) != 2 {
		t.Fatalf("RunCalls: got %d, want 2", len(fr.RunCalls))
	}
}

// TestRunContinuous_ResizeUpMidDrainLaunchesNextIssue verifies issue #653:
// raising a live Limiter's cap while a Box is running launches a second,
// already-ready issue immediately — it does not wait for the first Box to
// settle or for any other refill trigger.
func TestRunContinuous_ResizeUpMidDrainLaunchesNextIssue(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	limiter := NewLimiter(1)
	session := &Session{Limiter: limiter}

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fr := runner.NewFake()
	release1 := make(chan struct{})
	started2 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		switch box.Issue {
		case "1":
			<-release1
		case "2":
			close(started2)
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	fake := NewFake()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, session, fc, fc, dir, f, s, fake, fresh)
	}()

	select {
	case <-started2:
		t.Fatal("issue #2 started before the cap was ever raised above 1")
	case <-time.After(100 * time.Millisecond):
	}

	limiter.ResizeDelta(1)

	select {
	case <-started2:
	case <-time.After(2 * time.Second):
		t.Fatal("issue #2 was never dispatched after ResizeDelta(1) — raising the cap must launch immediately")
	}

	close(release1)

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("RunContinuous: got %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return after #1 was released")
	}

	if len(fr.RunCalls) != 2 {
		t.Fatalf("RunCalls: got %d, want 2", len(fr.RunCalls))
	}
}

// TestRunContinuous_RapidResizeLaunchesAllHeldPicks verifies issue #766:
// two Resize calls fired back-to-back (no yield in between, so the
// buffer-1 grow channel coalesces them into a single delivered signal)
// still launch every held pick the raised cap now allows — not just one,
// with the rest stranded until an unrelated Release.
func TestRunContinuous_RapidResizeLaunchesAllHeldPicks(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	limiter := NewLimiter(1)
	session := &Session{Limiter: limiter}

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{label}})

	fr := runner.NewFake()
	release1 := make(chan struct{})
	release2 := make(chan struct{})
	release3 := make(chan struct{})
	started2 := make(chan struct{})
	started3 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		switch box.Issue {
		case "1":
			<-release1
		case "2":
			close(started2)
			<-release2
		case "3":
			close(started3)
			<-release3
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	fake := NewFake()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, session, fc, fc, dir, f, s, fake, fresh)
	}()

	select {
	case <-started2:
		t.Fatal("issue #2 started before the cap was ever raised above 1")
	case <-time.After(100 * time.Millisecond):
	}

	// Simulate two '+' presses landing faster than the resize listener can
	// drain the buffer-1 channel: raise the cap to what back-to-back
	// ResizeDelta(1), ResizeDelta(1) calls would leave it at, but only
	// deliver the single coalesced signal ResizeDelta's own non-blocking
	// send would actually manage to enqueue. Racing two real ResizeDelta
	// calls against this test's already-parked listener goroutine can't
	// force the drop deterministically — Go hands a buffered-channel send
	// directly to a parked receiver instead of filling the buffer, so the
	// first ResizeDelta call here would bypass the buffer entirely and
	// leave nothing for the second call's non-blocking send to collide
	// with. This test reproduces the drop directly via the package-internal
	// fields instead; TestLimiter_ResizeCoalescesGrowSignalUnderRapidRaises
	// (limiter_test.go) covers the same coalescing mechanism through the
	// real ResizeDelta API, with no listener parked to intercept the first
	// send. The listener now selects on Resized, not Grown (#2678 review
	// finding — a lower needs the same checkpoint a raise gets), so the
	// simulated drop writes straight to the resized field.
	limiter.mu.Lock()
	limiter.cap = 3
	limiter.mu.Unlock()
	limiter.cond.Broadcast()
	select {
	case limiter.resized <- struct{}{}:
	default:
	}

	select {
	case <-started2:
	case <-time.After(2 * time.Second):
		t.Fatal("issue #2 was never dispatched after rapid ResizeDelta(1), ResizeDelta(1)")
	}
	select {
	case <-started3:
	case <-time.After(2 * time.Second):
		t.Fatal("issue #3 was never dispatched after rapid ResizeDelta(1), ResizeDelta(1) — a coalesced grow signal must still launch every held pick the new cap allows")
	}

	close(release1)
	close(release2)
	close(release3)

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("RunContinuous: got %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return after all issues were released")
	}

	if len(fr.RunCalls) != 3 {
		t.Fatalf("RunCalls: got %d, want 3", len(fr.RunCalls))
	}
}

// TestRunContinuous_ResizeDownNeverTerminatesGatesNewLaunches verifies issue
// #653: lowering a live Limiter's cap below the current live count kills
// nothing already running, and a third ready issue is held back — not
// launched over the lowered cap — until enough in-flight Boxes settle to
// bring live back under it.
func TestRunContinuous_ResizeDownNeverTerminatesGatesNewLaunches(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	limiter := NewLimiter(2)
	session := &Session{Limiter: limiter}

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{label}})

	fr := runner.NewFake()
	started1 := make(chan struct{})
	started2 := make(chan struct{})
	release1 := make(chan struct{})
	release2 := make(chan struct{})
	started3 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		switch box.Issue {
		case "1":
			close(started1)
			<-release1
		case "2":
			close(started2)
			<-release2
		case "3":
			close(started3)
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	fake := NewFake()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, session, fc, fc, dir, f, s, fake, fresh)
	}()

	for _, ch := range []chan struct{}{started1, started2} {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("both #1 and #2 should have started with cap=2")
		}
	}

	limiter.ResizeDelta(-1)

	select {
	case <-started3:
		t.Fatal("#3 launched over the lowered cap while #1 and #2 were both still running")
	case <-time.After(100 * time.Millisecond):
	}

	close(release1)

	select {
	case <-started3:
		t.Fatal("#3 launched with live==lowered cap (only #1 freed, #2 still running)")
	case <-time.After(100 * time.Millisecond):
	}

	close(release2)

	select {
	case <-started3:
	case <-time.After(2 * time.Second):
		t.Fatal("#3 never launched once live sank under the lowered cap")
	}

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("RunContinuous: got %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return")
	}

	if len(fr.RunCalls) != 3 {
		t.Fatalf("RunCalls: got %d, want 3 (lowering must never terminate #1 or #2 — all three run to completion)", len(fr.RunCalls))
	}
}

// TestRunContinuous_StaleProbeStopsRefillLetsInFlightFinish verifies #527
// AC3: once the freshness checker reports rebuild-needed, no further Box
// launches, the Box already in flight still runs to completion, and
// RunContinuous returns ErrImageStale (the new documented exit code) once
// it does.
func TestRunContinuous_StaleProbeStopsRefillLetsInFlightFinish(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fr := runner.NewFake()
	release1 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		if box.Issue == "1" {
			<-release1
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	fake := NewFake()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}

	// Fresh for the first refill (fills #1's slot), stale for every
	// refill after — including the second initial slot and #1's eventual
	// completion refill.
	var freshCalls int
	var mu sync.Mutex
	fresh := func() (bool, bool, string) {
		mu.Lock()
		defer mu.Unlock()
		freshCalls++
		if freshCalls == 1 {
			return true, true, "fresh"
		}
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, fake, fresh)
	}()

	close(release1)

	select {
	case err := <-resultCh:
		if !errors.Is(err, ErrImageStale) {
			t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return")
	}

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("RunCalls: got %v, want exactly issue 1 (no new Box after the probe went stale)", fr.RunCalls)
	}
}

// TestRunContinuous_StaleDrainWithInFlightBoxReportsHeldBack verifies
// #2678's non-zero-outstanding case: when the stale verdict fires while a
// Box is still in flight, the drain report is emitted once that Box
// finishes (from the completion goroutine, not the stale-transition
// branch), and reflects the issue left unclaimed by the stale verdict.
func TestRunContinuous_StaleDrainWithInFlightBoxReportsHeldBack(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	// Deterministic clock (issue #2678 mutation-testing gap: replacing the
	// freeSlotSecs accumulation at continuous.go with a literal 0 left the
	// whole waves suite green, since the two assertions below only checked
	// >=0). The mu.Lock()/idle.Wait() pairing in RunContinuous's bootstrap
	// section guarantees this scenario reads the clock exactly twice, in
	// order: once to set staleDrainStart when the stale verdict fires (still
	// holding mu inside drainRefill), once more to checkpoint when box #1
	// -- the only Box ever in flight -- completes and its handler acquires
	// mu (which it cannot do until the bootstrap's idle.Wait() releases
	// it). That fixed two-call sequence makes the resulting freeSlotSecs
	// value exactly computable by hand instead of only >=0-checkable
	// against real wall-clock time.
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	const tick = 5 * time.Second
	var clockMu sync.Mutex
	clockCalls := 0
	c.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		got := base.Add(time.Duration(clockCalls) * tick)
		clockCalls++
		return got
	}

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fr := runner.NewFake()
	release1 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		if box.Issue == "1" {
			<-release1
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	fake := NewFake()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{},
	}
	fake.PendingFunc = fakePending(fc, c, nil, nil)

	// Fresh for the first refill (fills #1's slot), stale for every
	// refill after -- including the second initial slot -- so #2 stays
	// held back while #1 keeps running.
	var freshCalls int
	var mu sync.Mutex
	fresh := func() (bool, bool, string) {
		mu.Lock()
		defer mu.Unlock()
		freshCalls++
		if freshCalls == 1 {
			return true, true, "fresh"
		}
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, fake, fresh)
	}()
	close(release1)
	var err error
	select {
	case err = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return")
	}

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("RunCalls: got %v, want exactly issue 1 (no new Box after the probe went stale)", fr.RunCalls)
	}

	// Fake.ReportStaleDrain's own doc comment (queue_fake.go) explains why
	// the report is read directly off the recorded call.
	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]
	if report.HeldBack != 1 || report.HeldBackUnknown {
		t.Fatalf("report: got HeldBack=%d HeldBackUnknown=%v, want HeldBack=1 HeldBackUnknown=false", report.HeldBack, report.HeldBackUnknown)
	}

	// The clock advances by exactly one tick between the two reads
	// (staleDrainStart, then the completion checkpoint that becomes
	// staleDrainEnd), so Duration() == tick exactly.
	wantDur := tick.Seconds()
	if dur := report.Duration().Seconds(); dur != wantDur {
		t.Fatalf("report.Duration(): got %v, want exactly %v (base+%v clock, two reads)", dur, wantDur, tick)
	}

	// freeSlotSecs accumulates (limiter.Cap()-outstanding)*elapsed across
	// the single interval between the two clock reads: Cap()=2,
	// outstanding=1 (box #1 still counted before its own decrement) over
	// the one tick between staleDrainStart and the completion checkpoint, so
	// the exact expected value is 1*tick, not merely >=0 -- reverting the
	// real accumulation to a literal 0 (or any other wrong formula) must
	// fail this assertion.
	wantFree := float64(2-1) * tick.Seconds()
	if report.FreeSlotSecs != wantFree {
		t.Fatalf("report.FreeSlotSecs: got %v, want exactly %v ((cap-outstanding)*tick = (2-1)*%v)", report.FreeSlotSecs, wantFree, tick)
	}

	if clockCalls != 2 {
		t.Fatalf("clock reads: got %d, want exactly 2 (staleDrainStart + completion checkpoint) -- test assumptions about the deterministic sequence no longer hold", clockCalls)
	}
}

// TestRunContinuous_StaleDrainDiscoverErrorReportsHeldBackUnknown verifies a
// review finding on #2678: when queue.Pending() errors (a transient tracker
// hiccup), the emitted StaleDrainReport must say the held-back count is
// unknown, not silently fabricate a confirmed-looking zero.
func TestRunContinuous_StaleDrainDiscoverErrorReportsHeldBackUnknown(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fr := runner.NewFake()
	release1 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		if box.Issue == "1" {
			<-release1
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	fake := NewFake()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{},
	}
	errDiscover := errors.New("tracker rate limited")
	fake.PendingErr = errDiscover

	// Fresh for the first refill (fills #1's slot), stale for every refill
	// after -- including the second initial slot -- so the stale-transition
	// branch's Pending() call is the one that errors.
	var freshCalls int
	var freshMu sync.Mutex
	fresh := func() (bool, bool, string) {
		freshMu.Lock()
		defer freshMu.Unlock()
		freshCalls++
		if freshCalls == 1 {
			return true, true, "fresh"
		}
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	resultCh := make(chan error, 1)
	var err error
	stderr := testutil.CaptureStderr(t, func() {
		go func() {
			resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, fake, fresh)
		}()
		close(release1)
		select {
		case err = <-resultCh:
		case <-time.After(2 * time.Second):
			t.Fatal("RunContinuous did not return")
		}
	})

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("RunCalls: got %v, want exactly issue 1 (no new Box after the probe went stale)", fr.RunCalls)
	}
	if !strings.Contains(stderr, "continuous: query pending for stale-drain report:") {
		t.Fatalf("stderr: got %q, want a line reporting the discover error that caused held-back=unknown", stderr)
	}

	// Fake.ReportStaleDrain's own doc comment (queue_fake.go) explains why
	// the report is read directly off the recorded call.
	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]
	if !report.HeldBackUnknown {
		t.Fatal("report.HeldBackUnknown: got false, want true after a Pending error (must not fabricate a confirmed-looking count)")
	}
	if report.HeldBack != 0 {
		t.Fatalf("report.HeldBack: got %d, want 0 (unset -- HeldBackUnknown is what callers must check)", report.HeldBack)
	}
}

// TestRunContinuous_StaleDrainHeldBackExcludesBlockedIssues verifies a review
// finding on #2678: the stale-transition branch's heldBack count must apply
// nextReady's own blocked/touch-overlap/failed-check filtering, not just
// drop issues already claimed this run. The discovered-but-unclaimed batch
// here is a mix -- #1 is genuinely ready (no blockers), #2 is blocked by an
// unresolved edge to #9 -- so the correct heldBack is 1 (only #1), not 2
// (every unclaimed issue). Before the fix, heldBack was computed as
// len(dropClaimed(issues, claimed)), which counts #2 too even though it was
// never going to dispatch; that inflated count would make this assertion
// fail. Ported forward onto the Queue.Pending seam (issue #2939): pending
// now recomputes its own Batch (via fakePending, mirroring main.go's
// production pending closure) instead of reusing RunContinuous's discover.
func TestRunContinuous_StaleDrainHeldBackExcludesBlockedIssues(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "9", State: "OPEN"}) // #2's blocker, unmet

	edges := map[string][]string{"2": {"9"}}

	dir := tempLogDir(t)

	fake := NewFake()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  edges,
	}
	fake.PendingFunc = fakePending(fc, c, edges, nil)
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	// stale fires before any launch (fresh is always stale here), so no Box
	// ever dispatches -- nil, nil for the *dispatch.Factory and
	// settle.Settler parameters, mirroring
	// TestRunContinuous_ThroughQueueFake_AllBlockedNeedsNoFactory
	// (queue_engine_test.go).
	err := RunContinuous(c, nil, fc, fc, dir, nil, nil, fake, fresh)

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}

	// Fake.ReportStaleDrain's own doc comment (queue_fake.go) explains why
	// the report is read directly off the recorded call.
	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]
	if report.HeldBack != 1 || report.HeldBackUnknown {
		t.Fatalf("report: got HeldBack=%d HeldBackUnknown=%v, want HeldBack=1 HeldBackUnknown=false (only #1 is ready; #2 is blocked by #9, so it must not inflate the count)", report.HeldBack, report.HeldBackUnknown)
	}
}

// TestRunContinuous_StaleDrainHeldBackExcludesTouchOverlapDeferredIssues
// pins the second of countReady's three documented exclusions (#2778):
// heldBack must also skip an issue deferred by the touch-overlap gate, not
// just one blocked by an unresolved edge. #1 is genuinely ready; #2 declares
// the same touch path as in-progress #9, so the overlap gate defers it --
// the correct heldBack is 1 (only #1), never 2. Ported forward onto the
// Queue.Pending seam (issue #2939).
func TestRunContinuous_StaleDrainHeldBackExcludesTouchOverlapDeferredIssues(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Body: "## Touches\n- lib/foo.nix", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "9", Body: "## Touches\n- lib/foo.nix", Labels: []string{testInProgressLabel}, State: "OPEN"}) // #2's overlap collider

	dir := tempLogDir(t)

	fake := NewFake()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}
	fake.PendingFunc = fakePending(fc, c, nil, nil)
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	// stale fires before any launch (fresh is always stale here), so no Box
	// ever dispatches -- nil, nil for the *dispatch.Factory and
	// settle.Settler parameters, mirroring
	// TestRunContinuous_ThroughQueueFake_AllBlockedNeedsNoFactory
	// (queue_engine_test.go).
	err := RunContinuous(c, nil, fc, fc, dir, nil, nil, fake, fresh)

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}

	// Fake.ReportStaleDrain's own doc comment (queue_fake.go) explains why
	// the report is read directly off the recorded call.
	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]
	if report.HeldBack != 1 || report.HeldBackUnknown {
		t.Fatalf("report: got HeldBack=%d HeldBackUnknown=%v, want HeldBack=1 HeldBackUnknown=false (only #1 is ready; #2 is deferred by the touch-overlap gate, so it must not inflate the count)", report.HeldBack, report.HeldBackUnknown)
	}
}

// TestRunContinuous_StaleDrainHeldBackExcludesDepsOfFailedIssues pins the
// third of countReady's three documented exclusions (#2778): heldBack must
// also skip an issue whose own DepsOf check failed, not just a blocked or
// touch-overlap-deferred one. #1 is genuinely ready; #2's own DepsOf check
// is marked failed via the discover closure's failed map -- the correct
// heldBack is 1 (only #1), never 2. Ported forward onto the Queue.Pending
// seam (issue #2939).
func TestRunContinuous_StaleDrainHeldBackExcludesDepsOfFailedIssues(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}}) // its own DepsOf check fails

	failed := map[string]bool{"2": true}

	dir := tempLogDir(t)

	fake := NewFake()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Failed: failed,
	}
	fake.PendingFunc = fakePending(fc, c, nil, failed)
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	// stale fires before any launch (fresh is always stale here), so no Box
	// ever dispatches -- nil, nil for the *dispatch.Factory and
	// settle.Settler parameters, mirroring
	// TestRunContinuous_ThroughQueueFake_AllBlockedNeedsNoFactory
	// (queue_engine_test.go).
	err := RunContinuous(c, nil, fc, fc, dir, nil, nil, fake, fresh)

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}

	// Fake.ReportStaleDrain's own doc comment (queue_fake.go) explains why
	// the report is read directly off the recorded call.
	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]
	if report.HeldBack != 1 || report.HeldBackUnknown {
		t.Fatalf("report: got HeldBack=%d HeldBackUnknown=%v, want HeldBack=1 HeldBackUnknown=false (only #1 is ready; #2's own DepsOf check failed, so it must not inflate the count)", report.HeldBack, report.HeldBackUnknown)
	}
}

// TestRunContinuous_StaleDrainHeldBackCountsAllExclusionsWhenIgnoreBlockers
// pins the counterexample to the two sibling exclusion tests above (#2778
// review finding): under cfg.IgnoreBlockers (research-kind continuous
// dispatch, set from main.go's dispatchKindResearch wiring), issueReadiness
// (continuous.go) skips both the DepsOf-failed switch case's own guard and
// the unresolved-blocker-edge computation itself, so neither exclusion
// applies -- only the touch-overlap exclusion, unexercised here, still can.
// #1 is genuinely ready; #2's own DepsOf check is marked failed via the
// discover closure's failed map, same setup as
// TestRunContinuous_StaleDrainHeldBackExcludesDepsOfFailedIssues; #3 has an
// edge to unresolved #9 in the edges map, same setup as
// TestRunContinuous_StaleDrainHeldBackExcludesBlockedIssues, but under
// IgnoreBlockers that edge is never even consulted. With
// IgnoreBlockers=true the correct heldBack is 3 (#1, #2, and #3): the
// DepsOf-failed exclusion is skipped so #2 is counted ready, and the
// blocker-edge check is skipped entirely so #3 is counted ready too. Ported
// forward onto the Queue.Pending seam (issue #2939).
func TestRunContinuous_StaleDrainHeldBackCountsAllExclusionsWhenIgnoreBlockers(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.IgnoreBlockers = true

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}}) // its own DepsOf check fails
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{label}}) // blocked by unresolved edge to #9
	fc.SetIssue(forge.Issue{Number: "9", State: "OPEN"})           // #3's blocker, unmet

	edges := map[string][]string{"3": {"9"}}
	failed := map[string]bool{"2": true}

	dir := tempLogDir(t)

	fake := NewFake()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}},
		Edges:  edges,
		Failed: failed,
	}
	fake.PendingFunc = fakePending(fc, c, edges, failed)
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	// stale fires before any launch (fresh is always stale here), so no Box
	// ever dispatches -- nil, nil for the *dispatch.Factory and
	// settle.Settler parameters, mirroring
	// TestRunContinuous_ThroughQueueFake_AllBlockedNeedsNoFactory
	// (queue_engine_test.go).
	err := RunContinuous(c, nil, fc, fc, dir, nil, nil, fake, fresh)

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}

	// Fake.ReportStaleDrain's own doc comment (queue_fake.go) explains why
	// the report is read directly off the recorded call.
	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]
	if report.HeldBack != 3 || report.HeldBackUnknown {
		t.Fatalf("report: got HeldBack=%d HeldBackUnknown=%v, want HeldBack=3 HeldBackUnknown=false (IgnoreBlockers skips both the DepsOf-failed exclusion and the blocker-edge check, so #1, #2, and #3 are all counted ready)", report.HeldBack, report.HeldBackUnknown)
	}
}

// TestRunContinuous_StaleDrainResizeBelowOutstandingClampsFreeSlotSecs
// verifies a review finding on #2678: the completion goroutine's
// freeSlotSecs accumulation multiplies the elapsed interval by
// staleDrainCap-outstanding, with no floor. ResizeDelta (limiter.go) never
// revokes a slot already claimed/outstanding, so an operator lowering the
// live cap mid-drain below the outstanding count makes that term negative,
// corrupting the running total with a negative contribution instead of
// crediting zero free slots for an interval that had none.
//
// A second review finding on #2678 fixed staleDrainCap itself: it now tracks the
// cap actually in effect since the last checkpoint (frozen at staleDrainStart,
// refreshed only after each checkpoint closes out the interval that just
// ended) rather than reading limiter.Cap() live at checkpoint time -- see
// TestRunContinuous_StaleDrainResizeUpCheckpointsBeforeCapChange for that
// half of the fix. A THIRD review finding fixed the resize listener itself:
// it now wakes on Limiter.Resized() (fires on either direction), not just
// Grown() (raise-only) -- ResizeDelta's lower here checkpoints immediately,
// exactly like a raise does, instead of leaving staleDrainCap frozen until
// whichever Box happens to complete next. This scenario now proves all
// three fixes compose: with 3 Boxes launched against an initial cap of 4,
// then resized straight down to the Limiter's floor of 1 while all 3 are
// still outstanding, the resize listener's own checkpoint is the FIRST of
// the four checkpoints this drain sees (staleDrainStart, then one checkpoint
// each for the resize and the three completions -- five now() calls total,
// not four) -- and one of the three completion checkpoints after it must
// still be clamped, because staleDrainCap can fall below outstanding even after
// the refresh.
//
// The initial cap is 4, not 3, because the staleness probe that trips a
// drain can only succeed while a slot is still free (Limiter.TryAcquire
// requires cap > live) -- tripping it with all 3 Boxes already outstanding
// forces the frozen staleDrainCap at staleDrainStart to be at least outstanding+1,
// never outstanding itself. That's an inherent floor of the "extra
// TryAcquire probe" mechanism RunContinuous uses to detect staleness, not a
// choice made for this test.
//
// The deterministic clock's `now` closure is the synchronization point,
// exactly as in
// TestRunContinuous_StaleDrainResizeDownAboveOutstandingCheckpointsBeforeCapChange
// below: it signals sawResizeCheckpoint the moment its SECOND call happens,
// which by construction can only be the resize listener's own
// checkpointStaleDrain (nothing else calls now() between staleDrainStart and the
// test's own ResizeDelta) -- and because that call happens while the
// resize listener still holds the shared mutex the completion goroutines
// also need, waiting for the signal before releasing any of the three
// Boxes guarantees the resize listener's checkpoint (and its staleDrainCap
// refresh) has already run to completion before any completion goroutine's
// own checkpoint can start. Without that barrier, the resize listener's
// checkpoint is only taken at all if inProgress() is still true when
// it acquires mu (continuous.go's `case <-limiter.Resized():` branch) --
// if all three completions raced ahead and drained outstanding to 0
// first, the checkpoint would be skipped outright, so the barrier is load
// bearing, not merely a nicety.
//
// With that ordering pinned, the math comes out as follows: only the FIRST
// of the four checkpoints -- now guaranteed to be the resize listener's,
// not a completion's -- ever sees the still-frozen staleDrainCap=4 and
// outstanding=3 (the count before any completion has decremented it) --
// (4-3)*tick = one tick, correctly positive, no clamp needed -- because
// that first checkpoint is also what refreshes staleDrainCap to the
// already-applied live cap of 1, so every checkpoint after it credits
// (1-outstanding)*tick for whatever outstanding remains, and outstanding
// never drops below 1 until the very last of the three completions, so
// every one of those three intervals clamps to zero. Total: one tick (5s)
// -- not the 0s a naive unclamped formula would produce by summing one
// tick against negative contributions. That's a materially different (and
// wrong) answer, not merely a negative one: the unclamped formula silently
// erases interval 1's genuine free-slot second against interval 2's
// physically-impossible negative one, which the exact-equality assertion
// below catches.
func TestRunContinuous_StaleDrainResizeBelowOutstandingClampsFreeSlotSecs(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 4
	limiter := NewLimiter(4)
	session := &Session{Limiter: limiter}

	// Deterministic clock (same pattern as
	// TestRunContinuous_StaleDrainWithInFlightBoxReportsHeldBack): this
	// scenario reads the clock exactly five times -- once to set staleDrainStart
	// when the stale verdict fires (bootstrap, still holding mu), once for
	// the resize listener's own checkpoint (triggered by the ResizeDelta
	// below), then once per completion checkpoint as box #1, #2, and #3
	// each settle. sawResizeCheckpoint closes the moment the SECOND now()
	// call happens. Between staleDrainStart (the first call) and the test's own
	// limiter.ResizeDelta below, nothing else calls now() -- so the second
	// call can only be the resize listener's checkpointStaleDrain, and it fires
	// while that listener still holds mu, guaranteeing (see the function
	// doc comment) that closing any of the three releases right after
	// receiving this signal can never race ahead of the resize listener's
	// checkpoint. That pins the resize listener's checkpoint as always the
	// FIRST of the four checkpoints, so the count and the order are both
	// exactly what this test can pin down and assert on.
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	const tick = 5 * time.Second
	var clockMu sync.Mutex
	clockCalls := 0
	sawResizeCheckpoint := make(chan struct{})
	c.now = func() time.Time {
		clockMu.Lock()
		n := clockCalls
		clockCalls++
		clockMu.Unlock()
		if n == 1 {
			close(sawResizeCheckpoint)
		}
		return base.Add(time.Duration(n) * tick)
	}

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{label}})

	fr := runner.NewFake()
	started1 := make(chan struct{})
	started2 := make(chan struct{})
	started3 := make(chan struct{})
	release1 := make(chan struct{})
	release2 := make(chan struct{})
	release3 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		switch box.Issue {
		case "1":
			close(started1)
			<-release1
		case "2":
			close(started2)
			<-release2
		case "3":
			close(started3)
			<-release3
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	fake := NewFake()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}},
		Edges:  map[string][]string{},
	}

	// Fresh for the first three refills (fills #1, #2, and #3's slots
	// against cap=4), stale for the fourth -- the bootstrap's own attempt
	// at a fourth slot, which finds no more ready work but still trips the
	// freshness check while all three Boxes are outstanding. That's the
	// "all 3 Boxes outstanding" starting point the resize-below-outstanding
	// scenario needs.
	var freshCalls int
	var freshMu sync.Mutex
	fresh := func() (bool, bool, string) {
		freshMu.Lock()
		defer freshMu.Unlock()
		freshCalls++
		if freshCalls <= 3 {
			return true, true, "fresh"
		}
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	resultCh := make(chan error, 1)
	var err error
	go func() {
		resultCh <- RunContinuous(c, session, fc, fc, dir, f, s, fake, fresh)
	}()

	for _, ch := range []chan struct{}{started1, started2, started3} {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("#1, #2, and #3 should all have started with cap=4")
		}
	}

	// All three Boxes are outstanding and the drain is already
	// underway (the bootstrap's fourth refill attempt tripped
	// staleness while #1, #2, and #3 were still running). Drop the
	// live cap straight to the Limiter's floor -- exactly the
	// operator action the review finding calls out, just further
	// below outstanding than the original 2-Box scenario exercised.
	limiter.ResizeDelta(-3) // cap 4 -> 1, outstanding == 3

	select {
	case <-sawResizeCheckpoint:
	case <-time.After(2 * time.Second):
		t.Fatal("resize listener should have checkpointed the drain before any Box completes")
	}

	close(release1)
	close(release2)
	close(release3)

	select {
	case err = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return")
	}

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}

	// Fake.ReportStaleDrain's own doc comment (queue_fake.go) explains why
	// the report is read directly off the recorded call.
	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]

	free := report.FreeSlotSecs
	// See the function doc comment above for the full derivation: the
	// resize listener's checkpoint, guaranteed first of the four by the
	// sawResizeCheckpoint barrier above, contributes (4-3)*tick == one
	// tick, and the remaining three completion checkpoints all clamp to
	// zero -- one tick total.
	wantFree := tick.Seconds()
	if free != wantFree {
		t.Fatalf("freeSlotSeconds: got %v, want exactly %v (first checkpoint at the frozen pre-resize cap, every checkpoint after it clamped to 0)", free, wantFree)
	}
	if free < 0 {
		t.Fatalf("freeSlotSeconds: got %v, must never be negative", free)
	}

	if clockCalls != 5 {
		t.Fatalf("clock reads: got %d, want exactly 5 (staleDrainStart + the resize listener's own checkpoint + three completion checkpoints) -- test assumptions about the deterministic sequence no longer hold", clockCalls)
	}
}

// TestRunContinuous_StaleDrainResizeUpCheckpointsBeforeCapChange verifies
// the other half of the #2678 review finding fixed above: freeSlotSecs must
// never read limiter.Cap() live at checkpoint time and apply it
// retroactively to the whole interval that just ended. A Console operator
// can raise the live cap mid-drain via ResizeDelta (ADR 0023) at any
// moment; RunContinuous's grow listener (the `case <-limiter.Grown():`
// branch) must checkpoint the interval that just ended -- at the OLD cap --
// before it ever lets staleDrainCap see the raised value, so the raise is only
// ever credited to the interval that starts after it, never retroactively
// to the interval before it.
//
// One Box is held outstanding at cap 2 (the minimum that can trip the
// staleness probe with a single Box outstanding, for the same
// Limiter.TryAcquire-requires-cap>live reason documented on the clamp test
// above), then a Console-style ResizeDelta(+8) raises the cap to 10 while
// that Box is still running. The deterministic clock's `now` closure
// itself is the synchronization point: it signals sawGrowCheckpoint the
// moment its SECOND call happens, which by construction can only be the
// grow listener's checkpointStaleDrain (nothing else calls now() between
// staleDrainStart and the resize) -- and because that call happens while the
// grow listener still holds the shared mutex the completion goroutine also
// needs, waiting for the signal before releasing the Box guarantees the
// grow listener's checkpoint (and its staleDrainCap refresh) has already run to
// completion before the Box's own completion checkpoint can start, with no
// sleep or poll required.
//
// Interval 1 (staleDrainStart -> the grow listener's checkpoint, triggered by
// ResizeDelta itself, not a Box completion): staleDrainCap is still the OLD cap,
// 2 (frozen since staleDrainStart), outstanding=1 -- (2-1)*tick = one tick.
// Interval 2 (that checkpoint -> the Box's own completion): staleDrainCap has
// refreshed to the NEW cap, 10, outstanding=1 -- (10-1)*tick = nine ticks.
// Total: ten ticks (50s) -- neither the ~90s crediting the whole two-tick
// drain at the raised cap 10 (the pre-fix bug this pins: reading
// limiter.Cap() live at the single completion checkpoint would apply 10 to
// the entire interval since staleDrainStart) nor the ~10s crediting it all at
// the original cap 2.
func TestRunContinuous_StaleDrainResizeUpCheckpointsBeforeCapChange(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	limiter := NewLimiter(2)
	session := &Session{Limiter: limiter}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	const tick = 5 * time.Second
	var clockMu sync.Mutex
	clockCalls := 0
	// sawGrowCheckpoint closes the moment the SECOND now() call happens.
	// Between staleDrainStart (the first call) and the test's own
	// limiter.ResizeDelta below, nothing else calls now() -- so the second
	// call can only be the grow listener's checkpointStaleDrain, and it fires
	// while that listener still holds mu, guaranteeing (see the function
	// doc comment) that closing release1 right after receiving this signal
	// can never race ahead of the grow listener's checkpoint.
	sawGrowCheckpoint := make(chan struct{})
	c.now = func() time.Time {
		clockMu.Lock()
		n := clockCalls
		clockCalls++
		clockMu.Unlock()
		if n == 1 {
			close(sawGrowCheckpoint)
		}
		return base.Add(time.Duration(n) * tick)
	}

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fr := runner.NewFake()
	started1 := make(chan struct{})
	release1 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		if box.Issue == "1" {
			close(started1)
			<-release1
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	fake := NewFake()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}},
		Edges:  map[string][]string{},
	}

	// Fresh for the first refill (fills #1's slot against cap=2), stale for
	// the second -- the bootstrap's own probe attempt, which trips
	// staleness while #1 is still the sole outstanding Box.
	var freshCalls int
	var freshMu sync.Mutex
	fresh := func() (bool, bool, string) {
		freshMu.Lock()
		defer freshMu.Unlock()
		freshCalls++
		if freshCalls <= 1 {
			return true, true, "fresh"
		}
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	resultCh := make(chan error, 1)
	var err error
	go func() {
		resultCh <- RunContinuous(c, session, fc, fc, dir, f, s, fake, fresh)
	}()

	select {
	case <-started1:
	case <-time.After(2 * time.Second):
		t.Fatal("#1 should have started with cap=2")
	}

	// #1 is outstanding and the drain is already underway (the
	// bootstrap's second refill attempt tripped staleness while #1
	// was still running). Raise the live cap -- a Console "+"
	// (ADR 0023) -- while #1 is still in flight.
	limiter.ResizeDelta(8) // cap 2 -> 10

	select {
	case <-sawGrowCheckpoint:
	case <-time.After(2 * time.Second):
		t.Fatal("grow listener should have checkpointed the drain before the Box completes")
	}

	close(release1)

	select {
	case err = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return")
	}

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}

	// Fake.ReportStaleDrain's own doc comment (queue_fake.go) explains why
	// the report is read directly off the recorded call.
	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]

	dur := report.Duration().Seconds()
	wantDur := 2 * tick.Seconds()
	if dur != wantDur {
		t.Fatalf("Duration(): got %v, want exactly %v (staleDrainStart + two checkpoints, one tick apart each)", dur, wantDur)
	}

	free := report.FreeSlotSecs
	// See the function doc comment above for the interval-by-interval
	// derivation: (2-1)*tick + (10-1)*tick == one tick + nine ticks == ten
	// ticks (50s).
	wantFree := (1 + 9) * tick.Seconds()
	if free != wantFree {
		t.Fatalf("FreeSlotSecs: got %v, want exactly %v (old cap credited before the raise, new cap only after it)", free, wantFree)
	}

	if clockCalls != 3 {
		t.Fatalf("clock reads: got %d, want exactly 3 (staleDrainStart + grow checkpoint + completion checkpoint) -- test assumptions about the deterministic sequence no longer hold", clockCalls)
	}
}

// TestRunContinuous_StaleDrainResizeDownAboveOutstandingCheckpointsBeforeCapChange
// verifies a review finding on #2678: the fix that made the resize listener
// checkpoint on a raise
// (TestRunContinuous_StaleDrainResizeUpCheckpointsBeforeCapChange above) left
// the mirror-image case unfixed. RunContinuous's resize listener
// used to wake only on Limiter.Grown(), which never signals for a lower
// (limiter.go's signalGrow returns early when the resize didn't grow the
// cap) -- so a mid-drain lower sat unnoticed until whichever Box happened to
// complete next, and that completion's checkpoint credited the ENTIRE
// interval since the last checkpoint at the stale, pre-lower staleDrainCap,
// over-crediting every second between the lower and that completion as if
// the higher cap had still been in effect.
// TestRunContinuous_StaleDrainResizeBelowOutstandingClampsFreeSlotSecs
// above never caught this because it lowers the cap to the Limiter's floor
// of 1, below the 3 outstanding Boxes -- the clamp erases the over-credit
// along with everything else. This scenario instead lowers a cap of 6 to 3
// with only 2 Boxes outstanding (the review finding's own repro numbers),
// so the lowered cap stays ABOVE outstanding, the clamp never engages, and
// an over-credit would show up directly in the asserted total.
//
// Both Boxes are held outstanding at cap 6 (the review finding's own
// numbers), then a Console-style ResizeDelta(-3) lowers the cap to 3 while
// both are still running -- 3 stays above the 2 outstanding Boxes, so
// nothing here is clamped. The deterministic clock's `now` closure is the
// synchronization point, exactly as in the Up-checkpoints test above: it
// signals sawResizeCheckpoint the moment its SECOND call happens, which by
// construction can only be the resize listener's checkpointStaleDrain (nothing
// else calls now() between staleDrainStart and the test's own ResizeDelta) --
// and because that call happens while the resize listener still holds the
// shared mutex the completion goroutines also need, waiting for the signal
// before releasing either Box guarantees the resize listener's checkpoint
// (and its staleDrainCap refresh) has already run to completion before either
// Box's own completion checkpoint can start.
//
// Interval 1 (staleDrainStart -> the resize listener's checkpoint, triggered by
// ResizeDelta itself, not a Box completion): staleDrainCap is still the OLD cap,
// 6 (frozen since staleDrainStart), outstanding=2 (both Boxes still running) --
// (6-2)*tick = four ticks. Interval 2 (that checkpoint -> the first Box to
// complete): staleDrainCap has refreshed to the NEW cap, 3, outstanding=2
// (pre-decrement, neither Box has completed yet) -- (3-2)*tick = one tick.
// Interval 3 (-> the second Box's completion): staleDrainCap=3, outstanding=1
// (pre-decrement, the one remaining Box) -- (3-1)*tick = two ticks. Total:
// seven ticks (35s) -- not the ~45s (nine ticks: (6-2)+(6-1)... crediting
// the whole pre-completion span at the stale cap 6) the pre-fix bug this
// pins would produce by leaving staleDrainCap frozen at 6 until a Box completion
// happened to refresh it.
//
// Which of the two Boxes completes first is never pinned down -- both are
// symmetric, so the math is identical either way: the first completion
// checkpoint always sees outstanding=2 (pre-decrement, neither has
// completed yet) and the second always sees outstanding=1, regardless of
// Box identity.
func TestRunContinuous_StaleDrainResizeDownAboveOutstandingCheckpointsBeforeCapChange(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 6
	limiter := NewLimiter(6)
	session := &Session{Limiter: limiter}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	const tick = 5 * time.Second
	var clockMu sync.Mutex
	clockCalls := 0
	// sawResizeCheckpoint closes the moment the SECOND now() call happens.
	// Between staleDrainStart (the first call) and the test's own
	// limiter.ResizeDelta below, nothing else calls now() -- so the second
	// call can only be the resize listener's checkpointStaleDrain, and it fires
	// while that listener still holds mu, guaranteeing (see the function
	// doc comment) that closing either release right after receiving this
	// signal can never race ahead of the resize listener's checkpoint.
	sawResizeCheckpoint := make(chan struct{})
	c.now = func() time.Time {
		clockMu.Lock()
		n := clockCalls
		clockCalls++
		clockMu.Unlock()
		if n == 1 {
			close(sawResizeCheckpoint)
		}
		return base.Add(time.Duration(n) * tick)
	}

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fr := runner.NewFake()
	started1 := make(chan struct{})
	started2 := make(chan struct{})
	release1 := make(chan struct{})
	release2 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		switch box.Issue {
		case "1":
			close(started1)
			<-release1
		case "2":
			close(started2)
			<-release2
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	fake := NewFake()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{},
	}

	// Fresh for the first two refills (fills #1's and #2's slots against
	// cap=6), stale for the third -- the bootstrap's own probe attempt,
	// which trips staleness while #1 and #2 are still the only outstanding
	// Boxes.
	var freshCalls int
	var freshMu sync.Mutex
	fresh := func() (bool, bool, string) {
		freshMu.Lock()
		defer freshMu.Unlock()
		freshCalls++
		if freshCalls <= 2 {
			return true, true, "fresh"
		}
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	resultCh := make(chan error, 1)
	var err error
	go func() {
		resultCh <- RunContinuous(c, session, fc, fc, dir, f, s, fake, fresh)
	}()

	for _, ch := range []chan struct{}{started1, started2} {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("#1 and #2 should both have started with cap=6")
		}
	}

	// #1 and #2 are outstanding and the drain is already underway (the
	// bootstrap's third refill attempt tripped staleness while both
	// were still running). Lower the live cap -- a Console "-"
	// (ADR 0023) -- while both are still in flight, staying above the
	// outstanding count so the clamp never engages.
	limiter.ResizeDelta(-3) // cap 6 -> 3

	select {
	case <-sawResizeCheckpoint:
	case <-time.After(2 * time.Second):
		t.Fatal("resize listener should have checkpointed the drain before either Box completes")
	}

	close(release1)
	close(release2)

	select {
	case err = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return")
	}

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}

	// Fake.ReportStaleDrain's own doc comment (queue_fake.go) explains why
	// the report is read directly off the recorded call.
	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]

	dur := report.Duration().Seconds()
	wantDur := 3 * tick.Seconds()
	if dur != wantDur {
		t.Fatalf("Duration(): got %v, want exactly %v (staleDrainStart + three checkpoints, one tick apart each)", dur, wantDur)
	}

	free := report.FreeSlotSecs
	// See the function doc comment above for the interval-by-interval
	// derivation: (6-2)*tick + (3-2)*tick + (3-1)*tick == four ticks + one
	// tick + two ticks == seven ticks (35s).
	wantFree := (4 + 1 + 2) * tick.Seconds()
	if free != wantFree {
		t.Fatalf("FreeSlotSecs: got %v, want exactly %v (old cap credited before the lower, new cap only after it -- an over-credited total here would mean the resize listener never checkpointed the lower)", free, wantFree)
	}

	if clockCalls != 4 {
		t.Fatalf("clock reads: got %d, want exactly 4 (staleDrainStart + resize checkpoint + two completion checkpoints) -- test assumptions about the deterministic sequence no longer hold", clockCalls)
	}
}

// TestRunContinuous_AllBlockedReturnsErrOpenNoneDispatchable verifies that
// exit-3 semantics are unchanged in continuous mode (#527 AC): when nothing
// in the initial batch is ever dispatchable, RunContinuous returns
// ErrOpenNoneDispatchable exactly as drainMaxJobs does for a batch wave,
// rather than hanging waiting for a refill event that can never come (no
// slot was ever filled).
func TestRunContinuous_AllBlockedReturnsErrOpenNoneDispatchable(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", State: "OPEN"}) // blocker, not complete

	dir := tempLogDir(t)

	edges := map[string][]string{"1": {"2"}}
	fake := NewFake()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}},
		Edges:  edges,
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	// nil, nil: nothing ever dispatches, so a nil *dispatch.Factory and
	// settle.Settler is a stronger guarantee than a fr.RunCalls==0
	// assertion -- with no Factory, dispatch is not merely unobserved, it
	// is impossible.
	err := RunContinuous(c, nil, fc, fc, dir, nil, nil, fake, fresh)
	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("RunContinuous: got %v, want ErrOpenNoneDispatchable", err)
	}
}

// TestRunContinuous_RateLimitedRediscoverRetriesWithBackoffThenSucceeds
// verifies issue #2866: a re-discover that fails with forge.ErrRateLimit
// retries with backoff instead of ending the run. discover fails on its
// first 2 calls, then succeeds on the 3rd; RunContinuous must retry through
// the failures, dispatch the issue once it discovers it, and return nil --
// sleeping through the injected fake Clock for exactly the 2 rate-limited
// attempts, with durations matching LinearBackoff{Unit: 1s}.Duration(1) and
// .Duration(2) (1s, 2s).
func TestRunContinuous_RateLimitedRediscoverRetriesWithBackoffThenSucceeds(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.TransientRetryMax = 3
	c.TransientBackoffSecs = 1

	var sleeps []time.Duration
	c.Clock = fakeWavesClock(time.Time{}, &sleeps)

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	fake := NewFake()
	fake.DiscoverFunc = func(callN int) (Batch, error) {
		if callN <= 2 {
			return Batch{}, fmt.Errorf("%w: rate limited", forge.ErrRateLimit)
		}
		return Batch{Issues: []Issue{{Number: "1"}}}, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	err := RunContinuous(c, nil, fc, fc, dir, f, s, fake, fresh)
	if err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}

	lb := retry.LinearBackoff{Unit: time.Duration(c.TransientBackoffSecs) * time.Second, Clock: c.Clock}
	wantSleeps := []time.Duration{lb.Duration(1), lb.Duration(2)}
	if len(sleeps) != len(wantSleeps) || sleeps[0] != wantSleeps[0] || sleeps[1] != wantSleeps[1] {
		t.Fatalf("sleeps: got %v, want %v", sleeps, wantSleeps)
	}
}

// TestRunContinuous_RateLimitedRediscoverExhaustsRetries verifies issue
// #2866's exhaustion path: a re-discover that keeps failing with
// forge.ErrRateLimit for TransientRetryMax+1 total attempts must give up the
// same way a non-rate-limit error does today -- refill returns false, no
// panic, no infinite loop -- but the stderr message it prints must name rate
// limiting as the cause. With nothing ever dispatched, RunContinuous falls
// out to ErrOpenNoneDispatchable exactly as an ordinary all-blocked run
// does; no special-casing of the rate-limit-exhausted path is needed for
// that.
func TestRunContinuous_RateLimitedRediscoverExhaustsRetries(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.TransientRetryMax = 2
	c.TransientBackoffSecs = 1

	var sleeps []time.Duration
	c.Clock = fakeWavesClock(time.Time{}, &sleeps)

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	dir := tempLogDir(t)

	fake := NewFake()
	fake.DiscoverErr = fmt.Errorf("%w: rate limited", forge.ErrRateLimit)
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	var err error
	out := testutil.CaptureStderr(t, func() {
		err = RunContinuous(c, nil, fc, fc, dir, nil, nil, fake, fresh)
	})

	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("RunContinuous: got %v, want ErrOpenNoneDispatchable", err)
	}
	if fake.DiscoverCalls != 1+c.TransientRetryMax {
		t.Fatalf("DiscoverCalls: got %d, want %d (1 initial + TransientRetryMax retries)", fake.DiscoverCalls, 1+c.TransientRetryMax)
	}
	if len(sleeps) != c.TransientRetryMax {
		t.Fatalf("sleeps: got %d, want %d (one backoff sleep before each retry, none after the final failed attempt)", len(sleeps), c.TransientRetryMax)
	}
	if !strings.Contains(out, "rate limit") {
		t.Fatalf("stderr must name rate limiting as the exhaustion cause, got:\n%s", out)
	}
}

// TestRunContinuous_NonRateLimitRediscoverErrorFailsFastUnchanged verifies
// issue #2866 left the pre-existing non-rate-limit re-discover failure path
// untouched: a discover() error that is not forge.ErrRateLimit ends the
// refill on the very first call, with the exact original stderr message and
// no backoff sleep.
func TestRunContinuous_NonRateLimitRediscoverErrorFailsFastUnchanged(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.TransientRetryMax = 2
	c.TransientBackoffSecs = 1

	var sleeps []time.Duration
	c.Clock = fakeWavesClock(time.Time{}, &sleeps)

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	dir := tempLogDir(t)

	wantErr := errors.New("boom")
	fake := NewFake()
	fake.DiscoverErr = wantErr
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	var err error
	out := testutil.CaptureStderr(t, func() {
		err = RunContinuous(c, nil, fc, fc, dir, nil, nil, fake, fresh)
	})

	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("RunContinuous: got %v, want ErrOpenNoneDispatchable", err)
	}
	if fake.DiscoverCalls != 1 {
		t.Fatalf("DiscoverCalls: got %d, want 1 (non-rate-limit error must fail fast on the very first attempt)", fake.DiscoverCalls)
	}
	if len(sleeps) != 0 {
		t.Fatalf("sleeps: got %d, want 0 (non-rate-limit error must never back off)", len(sleeps))
	}
	wantMsg := fmt.Sprintf("continuous: re-discover: %v\n", wantErr)
	if out != wantMsg {
		t.Fatalf("stderr: got %q, want exactly %q (unchanged message)", out, wantMsg)
	}
}

// TestRunContinuous_DiscoverSourcesReachRefill verifies issue #662: the
// discover closure's Sources return value (NewReadiness's native/body
// provenance for each blocker) survives the trip through RunContinuous's
// refill loop instead of being silently discarded. #2's declared blocker is
// body-parsed, populating Sources; RunContinuous must complete without
// error, dispatching only the unblocked #1 and leaving #2 held.
func TestRunContinuous_DiscoverSourcesReachRefill(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Body: "blocked by #3", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"}) // #2's blocker, unmet

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	raw, err := fc.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	out := make([]Issue, len(raw))
	for i, fi := range raw {
		out[i] = Issue{Number: fi.Number, Title: fi.Title}
	}
	result, err := NewReadiness(fc, out)
	if err != nil {
		t.Fatalf("NewReadiness: %v", err)
	}
	gotSources := result.Sources

	fake := NewFake()
	fake.DiscoverReturn = Batch{Issues: out, Edges: result.Edges, Sources: result.Sources, Failed: result.Failed}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, nil, fc, fc, dir, f, s, fake, fresh); err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}

	if gotSources["2"]["3"] != forge.DepSourceBody {
		t.Errorf("sources[2][3]: got %v, want DepSourceBody (#2's blocker on #3 is body-parsed)", gotSources["2"]["3"])
	}
	if len(gotSources) != 1 || len(gotSources["2"]) != 1 {
		t.Errorf("sources: got %v, want exactly {2: {3: DepSourceBody}}", gotSources)
	}

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("RunCalls: got %v, want exactly issue 1 (#2 stays held on its unmet body-parsed blocker)", fr.RunCalls)
	}

	iss2, err := fc.Issue("2")
	if err != nil {
		t.Fatalf("Issue(2): %v", err)
	}
	if !containsLabel(iss2.Labels, label) {
		t.Errorf("issue 2 must remain %q (held, not cascaded) — sources threading must not change selection; labels=%v", label, iss2.Labels)
	}
}

// TestRunContinuous_RefillCycleGuardSkipsAndReports verifies #571: a refill
// whose re-discovery returns an edge set with a cycle among in-batch issues
// must not launch a Box for any of them, must surface the offending issue
// number, and must return through RunContinuous's normal completion path
// rather than hanging.
func TestRunContinuous_RefillCycleGuardSkipsAndReports(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{label}})

	dir := tempLogDir(t)

	// Cyclic dependency among all three in-batch issues: 1 -> 2 -> 3 -> 1.
	edges := map[string][]string{
		"1": {"2"},
		"2": {"3"},
		"3": {"1"},
	}
	fake := NewFake()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}},
		Edges:  edges,
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	var err error
	resultCh := make(chan error, 1)
	errOut := testutil.CaptureStderr(t, func() {
		resultCh <- RunContinuous(c, nil, fc, fc, dir, nil, nil, fake, fresh)
	})

	select {
	case err = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return — cycle guard may have hung a refill")
	}

	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("RunContinuous: got %v, want ErrOpenNoneDispatchable (no issue in the cycle is ever dispatchable)", err)
	}
	// No Box may launch for a cyclic batch; passing a nil *dispatch.Factory
	// makes that structural (a launch attempt would nil-panic), so there is
	// no separate RunCalls count to assert here.
	if !strings.Contains(errOut, "cycle") || !strings.Contains(errOut, "#1") {
		t.Fatalf("stderr missing cycle report naming issue #1, got:\n%s", errOut)
	}
}

// TestRunContinuous_StaleDiscoveryNeverDoubleDispatches verifies #560: a
// Discoverer that keeps listing an already-claimed issue as dispatchable —
// modeling GitHub's eventually-consistent search index right after the
// label swap — must not launch a second Box for it, and the suppressed
// re-discovery must not re-attempt the dispatch-state transition (the live
// run's agent-in-progress claim is left untouched).
func TestRunContinuous_StaleDiscoveryNeverDoubleDispatches(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error { return nil }

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	// Real settle, not settle.NewFake(): the TransitionStateCalls==1
	// assertion below only holds because a real Settle actually performs
	// the InProgress -> Failed demotion on fc for the outcome-less,
	// PR-less run (issue #1605) -- settle.NewFake() only records calls on
	// itself and never touches fc, so that assertion would see 0 instead.
	s := newSettle(fc, fc)

	// Always reports #1 as dispatchable, regardless of the claim already
	// made against it -- a stale search result, not a live forge query. The
	// SAME batch on every call is exactly what a static DiscoverReturn
	// models, by design.
	fake := NewFake()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1", Title: "stale"}}, Edges: map[string][]string{}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	var err error
	out := testutil.CaptureStdout(t, func() {
		err = RunContinuous(c, nil, fc, fc, dir, f, s, fake, fresh)
	})
	if err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1 (stale re-discovery of #1 must not double-dispatch)", len(fr.RunCalls))
	}
	// The claim now flows through the Fake Queue's own Claim, which never
	// touches fc -- so the only entry left in fc.TransitionStateCalls is
	// real settle's own demotion of the box's outcome-less, PR-less run
	// (InProgress -> Failed, issue #1605). A second entry would mean the
	// suppressed stale re-discovery re-attempted the Failed transition.
	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("TransitionStateCalls: got %d, want 1 (suppressed stale entry must not re-attempt settle's transition)", len(fc.TransitionStateCalls))
	}
	// fake.ClaimCalls is what now proves the claim itself happened exactly
	// once, replacing the coverage the old TransitionStateCalls==2 count
	// implicitly gave the claim half before Claim moved onto the Fake.
	if len(fake.ClaimCalls) != 1 || fake.ClaimCalls[0] != "1" {
		t.Fatalf("ClaimCalls: got %v, want [\"1\"] (stale re-discovery of #1 must not double-claim)", fake.ClaimCalls)
	}
	if strings.Contains(out, "already claimed this run") {
		t.Fatalf("output must not log the stale re-discovery skip line, got:\n%s", out)
	}
}

// TestRunContinuous_TerminatedIssueSkipsFailedTransitionAndSettle verifies
// that when a Box's issue is marked on cfg.Terminated (Terminate landed
// while it was running, ADR 0024, issue #649), a non-zero exit is neither
// transitioned to Failed nor handed to Settle — Terminate already
// transitioned the issue to Dispatchable itself, and a subsequent Failed
// transition here would corrupt that.
func TestRunContinuous_TerminatedIssueSkipsFailedTransitionAndSettle(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	reg := terminate.NewRegistry()
	reg.Mark("1")
	session := &Session{Terminated: reg}

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fr := runner.NewFake()
	fr.RunErr = boxErr

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	fakeSettle := settle.NewFake()

	fake := NewFake()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}, Edges: map[string][]string{}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, session, fc, fc, dir, f, fakeSettle, fake, fresh); err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}

	for _, call := range fc.TransitionStateCalls {
		if call.To == forge.Failed {
			t.Errorf("must not transition to Failed after termination; got %+v", fc.TransitionStateCalls)
		}
	}
	if len(fakeSettle.SettleCalls) != 0 {
		t.Errorf("Settle must not be called after termination; got %+v", fakeSettle.SettleCalls)
	}
}

// TestRunContinuous_FailedBoxCallsSettlerFail verifies that when a Box exits
// non-zero (result.Success == false, no termination in play), RunContinuous
// transitions the tracker issue to Failed *and* calls the Settler's Fail
// hook — the seam a wrapper like the Console's queueSettler uses to move its
// queue row to a terminal state instead of stranding it at "running" (issue
// #705).
func TestRunContinuous_FailedBoxCallsSettlerFail(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fr := runner.NewFake()
	fr.RunErr = boxErr

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	fakeSettle := settle.NewFake()

	fake := NewFake()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}, Edges: map[string][]string{}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, nil, fc, fc, dir, f, fakeSettle, fake, fresh); err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}

	found := false
	for _, call := range fc.TransitionStateCalls {
		if call.To == forge.Failed {
			found = true
		}
	}
	if !found {
		t.Errorf("must transition to Failed; got %+v", fc.TransitionStateCalls)
	}
	if len(fakeSettle.FailCalls) != 1 || fakeSettle.FailCalls[0].Num != "1" {
		t.Errorf("fakeSettle.FailCalls = %+v, want one call for #1", fakeSettle.FailCalls)
	}
	if len(fakeSettle.SettleCalls) != 0 {
		t.Errorf("Settle must not be called on a Box failure; got %+v", fakeSettle.SettleCalls)
	}
}

// TestRunContinuous_RefillHoldsDepsOfFailedIssue verifies that a refill's
// Discoverer naming an issue in its failed set (#1103, the Discoverer's own
// NewReadiness/DepsOf call errored) holds it rather than dispatching it — the
// continuous-mode counterpart of TestDrainMaxJobs_HoldsDepsOfCheckFailedIssue.
func TestRunContinuous_RefillHoldsDepsOfFailedIssue(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	failed := map[string]bool{"1": true}
	fake := NewFake()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{},
		Failed: failed,
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, nil, fc, fc, dir, f, s, fake, fresh); err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "2" {
		t.Fatalf("RunCalls: got %v, want exactly issue 2", fr.RunCalls)
	}

	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if containsLabel(iss1.Labels, c.FailedLabel) {
		t.Errorf("issue 1 must NOT be cascade-failed on a DepsOf check failure; labels=%v", iss1.Labels)
	}
}

// TestRunContinuous_CompletionDrainsAllFreedSlots verifies #1587: the
// completing-Box refill trigger must drain every currently-free slot with
// ready work, not launch at most one replacement. #1 and #2 complete while
// #4/#5 are still invisible to discover, stranding two free slots; #3's
// later completion reveals both -- a single refill() call there launches
// only one, so this fails pre-fix and passes once the handler drains.
func TestRunContinuous_CompletionDrainsAllFreedSlots(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 3

	fc := forge.NewFake(dispatchLabels(c, label))
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		fc.SetIssue(forge.Issue{Number: n, Labels: []string{label}})
	}

	var visMu sync.Mutex
	visible := []string{"1", "2", "3"}
	calls := make(chan struct{}, 100)
	fake := NewFake()
	fake.DiscoverFunc = func(callN int) (Batch, error) {
		visMu.Lock()
		nums := append([]string(nil), visible...)
		visMu.Unlock()
		out := make([]Issue, len(nums))
		for i, n := range nums {
			out[i] = Issue{Number: n, Title: "issue " + n}
		}
		calls <- struct{}{}
		return Batch{Issues: out, Edges: map[string][]string{}}, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	fr := runner.NewFake()
	release3 := make(chan struct{})
	release4 := make(chan struct{})
	release5 := make(chan struct{})
	started4 := make(chan struct{})
	started5 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		switch box.Issue {
		case "3":
			<-release3
		case "4":
			close(started4)
			<-release4
		case "5":
			close(started5)
			<-release5
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, fake, fresh)
	}()

	drain := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			select {
			case <-calls:
			case <-time.After(3 * time.Second):
				t.Fatalf("timed out waiting for discover call %d/%d", i+1, n)
			}
		}
	}

	// Bootstrap drains discover three times, one per launch of #1, #2, #3.
	// Its own terminating refill() attempt never reaches discover: with all
	// three slots claimed, TryAcquire fails first, so that check is silent.
	drain(3)

	// #1 and #2 complete immediately (no RunFunc case blocks them). Their
	// mu-serialized completion handlers each fire exactly one refill
	// attempt (two more discover calls) while #4/#5 are still invisible,
	// so both find only the already-claimed #1-#3 and strand their freed
	// slot.
	drain(2)

	// The backlog becomes visible now that both stranded slots already
	// exist. #3 is still holding the only running slot; releasing it is
	// the sole remaining trigger that can ever revisit those two free
	// slots.
	visMu.Lock()
	visible = []string{"1", "2", "3", "4", "5"}
	visMu.Unlock()
	close(release3)

	select {
	case <-started4:
	case <-time.After(2 * time.Second):
		t.Fatal("issue #4 was never dispatched after the backlog became visible")
	}
	select {
	case <-started5:
	case <-time.After(2 * time.Second):
		t.Fatal("issue #5 was never dispatched -- completion refill filled only one freed slot instead of draining all of them, so the pool never climbed back to MAX_PARALLEL")
	}

	close(release4)
	close(release5)

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("RunContinuous: got %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return")
	}

	if len(fr.RunCalls) != 5 {
		t.Fatalf("RunCalls: got %d, want 5", len(fr.RunCalls))
	}
}

// TestRunContinuous_PollRefillsSlotLeftIdleByTransientMiss verifies #1637: a
// slot a refill attempt couldn't fill -- because the ready issue wasn't yet
// visible in discover's result -- gets picked up by a later background poll
// tick, with no Box ever completing to trigger it. MaxParallel=2 lets #1
// launch and strands the second slot once bootstrap's terminating refill
// attempt still finds nothing else ready; #2 then becomes visible while #1
// is still running, so only the poll ticker -- never a completion event --
// can be what launches it.
func TestRunContinuous_PollRefillsSlotLeftIdleByTransientMiss(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	c.pollInterval = 10 * time.Millisecond

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	var visMu sync.Mutex
	visible := []string{"1"}
	calls := make(chan struct{}, 100)
	fake := NewFake()
	fake.DiscoverFunc = func(callN int) (Batch, error) {
		visMu.Lock()
		nums := append([]string(nil), visible...)
		visMu.Unlock()
		out := make([]Issue, len(nums))
		for i, n := range nums {
			out[i] = Issue{Number: n, Title: "issue " + n}
		}
		calls <- struct{}{}
		return Batch{Issues: out, Edges: map[string][]string{}}, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	fr := runner.NewFake()
	release1 := make(chan struct{})
	started2 := make(chan struct{})
	fr.RunFunc = func(box runner.Box) error {
		switch box.Issue {
		case "1":
			<-release1
		case "2":
			close(started2)
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, fake, fresh)
	}()

	drain := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			select {
			case <-calls:
			case <-time.After(3 * time.Second):
				t.Fatalf("timed out waiting for discover call %d/%d", i+1, n)
			}
		}
	}

	// Bootstrap drains discover twice: once to launch #1, once more for the
	// terminating refill() attempt that finds the second slot's only
	// candidate (#1) already claimed and gives up, stranding the slot. Both
	// calls happen inside RunContinuous's single initial drainRefill(),
	// which holds mu for its whole loop -- the poll ticker can't acquire mu
	// and contribute a call of its own until that loop exits and the main
	// goroutine reaches idle.Wait(), so these first two calls are
	// deterministically bootstrap's, never a poll tick's.
	drain(2)

	// #2 becomes visible now that the slot is already stranded. #1 is still
	// running (no completion), so only a poll tick can revisit this slot.
	visMu.Lock()
	visible = []string{"1", "2"}
	visMu.Unlock()

	select {
	case <-started2:
	case <-time.After(2 * time.Second):
		t.Fatal("issue #2 was never dispatched by a background poll tick -- refill only fires on completion or a Console cap-raise")
	}

	close(release1)

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("RunContinuous: got %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return after #1 was released")
	}

	if len(fr.RunCalls) != 2 {
		t.Fatalf("RunCalls: got %d, want 2", len(fr.RunCalls))
	}
}

// TestRunContinuous_RefillDispatchesInPriorityOrder verifies the #2281
// review finding: continuous.go's refill closure sorts the discovered pool
// by Priority (forge.SortByPriority) before picking the next launch, so the
// refill loop actually dispatches in priority order end to end — not just
// in the isolated forge.SortByPriority/NewPlan unit coverage in plan_test.go.
// MaxParallel=1 forces strictly one-at-a-time dispatch, so fr.RunCalls'
// order is the observed launch order; five issues spanning every tier are
// seeded out of priority order (deliberately, so a passing result can only
// come from the sort, never from discovery/insertion order) and must launch
// Critical > High > Normal > Low, oldest-number-first within a tier.
func TestRunContinuous_RefillDispatchesInPriorityOrder(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})                            // Normal (unlabeled)
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label, "agent-priority-low"}})      // Low
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{label, "agent-priority-critical"}}) // Critical
	fc.SetIssue(forge.Issue{Number: "4", Labels: []string{label, "agent-priority-high"}})     // High
	fc.SetIssue(forge.Issue{Number: "5", Labels: []string{label, "agent-priority-critical"}}) // Critical, tiebreaks after #3

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	raw, err := fc.ListIssues(forge.Dispatchable)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	out := make([]Issue, len(raw))
	for i, fi := range raw {
		out[i] = Issue{Number: fi.Number, Title: fi.Title, Priority: fi.Priority}
	}
	fake := NewFake()
	fake.DiscoverReturn = Batch{Issues: out, Edges: map[string][]string{}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, nil, fc, fc, dir, f, s, fake, fresh); err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}

	got := make([]string, len(fr.RunCalls))
	for i, box := range fr.RunCalls {
		got[i] = box.Issue
	}
	want := []string{"3", "5", "4", "1", "2"}
	if !slices.Equal(got, want) {
		t.Fatalf("dispatch order: got %v, want %v (Critical > High > Normal > Low, oldest-number-first within a tier)", got, want)
	}
}

// TestRunContinuous_StaleWithNothingInFlightReportsZeroLengthDrain verifies
// #2678's zero-in-flight case: when the stale verdict fires on the very
// first refill -- before anything has ever launched -- the drain is already
// over, so RunContinuous reports it immediately with a zero-length
// duration, rather than reporting nothing (since there is no in-flight Box
// completion to trigger a later report).
func TestRunContinuous_StaleWithNothingInFlightReportsZeroLengthDrain(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	dir := tempLogDir(t)

	fake := NewFake()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}},
		Edges:  map[string][]string{},
	}
	fake.PendingFunc = fakePending(fc, c, nil, nil)
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	// Freshness is stale from the very first refill, so no Box ever
	// launches; passing a nil *dispatch.Factory/settle.Settler makes that
	// structural (a launch attempt would nil-panic), the same pattern
	// TestRunContinuous_RefillCycleGuardSkipsAndReports uses.
	err := RunContinuous(c, nil, fc, fc, dir, nil, nil, fake, fresh)

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}

	// Fake.ReportStaleDrain's own doc comment (queue_fake.go) explains why
	// the report is read directly off the recorded call.
	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]
	if report.HeldBack != 1 || report.HeldBackUnknown {
		t.Fatalf("report: got HeldBack=%d HeldBackUnknown=%v, want HeldBack=1 HeldBackUnknown=false", report.HeldBack, report.HeldBackUnknown)
	}
	if report.Duration() != 0 {
		t.Fatalf("report.Duration(): got %v, want exactly 0 (stale fired before any launch, so the drain is already over)", report.Duration())
	}
	if report.FreeSlotSecs != 0 {
		t.Fatalf("report.FreeSlotSecs: got %v, want exactly 0", report.FreeSlotSecs)
	}
}

// TestReportStaleDrainReleasingMu_ReleasesMuAroundIO verifies the contract
// reportStaleDrainReleasingMu's call sites (#2775, both stale-drain
// emission sites in continuous.go's refill/completion paths) rely on: it
// releases the caller-held mu before doing queue.ReportStaleDrain's
// blocking I/O, then re-acquires mu before returning -- held on entry, held
// on exit, released only in between.
func TestReportStaleDrainReleasingMu_ReleasesMuAroundIO(t *testing.T) {
	var mu sync.Mutex
	mu.Lock()

	releasedDuring := false
	queue := reportFunc(func(StaleDrainReport) {
		if mu.TryLock() {
			releasedDuring = true
			mu.Unlock()
		}
	})

	reportStaleDrainReleasingMu(&mu, queue, StaleDrainReport{})

	if !releasedDuring {
		t.Error("ReportStaleDrain callback: got mu held, want mu released during reportStaleDrainReleasingMu's I/O")
	}
	if mu.TryLock() {
		mu.Unlock()
		t.Error("mu after reportStaleDrainReleasingMu returns: got released, want held (re-acquired before return)")
	}
}

// TestResolvePollInterval covers resolvePollInterval's fallback (issue
// #2874); see defaultPollInterval's doc comment for the cadence rationale.
func TestResolvePollInterval(t *testing.T) {
	if got := resolvePollInterval(0); got != defaultPollInterval {
		t.Errorf("resolvePollInterval(0): got %s, want %s", got, defaultPollInterval)
	}

	const override = 10 * time.Millisecond
	if got := resolvePollInterval(override); got != override {
		t.Errorf("resolvePollInterval(%s): got %s, want %s", override, got, override)
	}
}
