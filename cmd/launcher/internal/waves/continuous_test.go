// This file's RunContinuous scenarios drive the Queue seam through *FakeQueue
// (issue #2937); queue_engine_test.go's header covers the line between the two
// files. A scenario's forge.Fake (fc, the tracker it claims and settles
// against) and its FakeQueue (only what Discover returns) stay independent on
// purpose, so a scenario can put fake.Claim out of step with fc's label state.
package waves

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/retry"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
	"spindrift.dev/launcher/internal/testutil"
)

// noopPending is a Pending closure for tests that never exercise the
// stale-drain heldBack path. It ignores the claimed set its caller passes in.
func noopPending(map[string]bool) (int, error) { return 0, nil }

// reportFunc adapts a StaleDrainReport callback into a Queue whose other four
// methods are unused no-ops, since reportStaleDrainReleasingMu's mu-release
// contract test only exercises ReportStaleDrain.
type reportFunc func(StaleDrainReport)

func (r reportFunc) Discover() (Batch, error)                 { return Batch{}, nil }
func (r reportFunc) Claim(string) error                       { return nil }
func (r reportFunc) Pending(map[string]bool) (int, error)     { return 0, nil }
func (r reportFunc) ReportStaleDrain(report StaleDrainReport) { r(report) }
func (r reportFunc) EnsureLogDirExists() error                { return nil }

// fakeWavesClock returns a retry.Clock with a fixed Now and a Sleep that
// records durations into calls, mirroring dispatch/retry_test.go's fakeClock
// for the waves package's own Clock seam (issue #2866).
func fakeWavesClock(now time.Time, calls *[]time.Duration) retry.Clock {
	return retry.Clock{
		Now:   func() time.Time { return now },
		Sleep: func(d time.Duration) { *calls = append(*calls, d) },
	}
}

// TestRunContinuous_RefillsFreedSlotWhileOthersRunning verifies the core
// slot-refill behavior (#527 AC1): with MaxParallel=2 and three ready issues,
// the third launches into the slot #1 frees while #2 is still running. A
// batch-shaped implementation would deadlock here, since #2 only unblocks
// after #3 has already started.
func TestRunContinuous_RefillsFreedSlotWhileOthersRunning(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{label}})

	fake := NewFakeQueue()
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
		resultCh <- RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
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

	// drainRefill calls refill() serially at bootstrap (continuous.go), so #1
	// and #2 claim in batch.Issues order before either Box starts running; #3
	// claims later, once #1's completion frees a slot. The order is
	// deterministic, so no sort is needed.
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{"2": {"3"}},
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
	}()

	// #2 is blocked at dispatch start (its blocker is open); MaxParallel=1
	// also means it can't launch until #1's slot frees. The blocker resolves
	// here, while #1 is still in flight, proving the refill re-checks
	// readiness against fresh state rather than a snapshot taken at startup.
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
// already-ready issue immediately, without waiting for the first Box to settle
// or for any other refill trigger.
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, session, fc, fc, f, s, fake, fresh)
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

// TestRunContinuous_RapidResizeLaunchesAllHeldPicks verifies issue #766: two
// Resize calls fired back-to-back (no yield in between, so the buffer-1 grow
// channel coalesces them into a single delivered signal) still launch every
// held pick the raised cap now allows, not just one with the rest stranded
// until an unrelated Release.
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, session, fc, fc, f, s, fake, fresh)
	}()

	select {
	case <-started2:
		t.Fatal("issue #2 started before the cap was ever raised above 1")
	case <-time.After(100 * time.Millisecond):
	}

	// Racing two real ResizeDelta calls against the parked listener cannot
	// force the dropped signal deterministically: Go hands a buffered send
	// straight to a parked receiver, leaving nothing for the second send to
	// collide with. Poke the internals instead, writing to resized because the
	// listener selects on Resized, not Grown (#2678 review finding).
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
// nothing already running, and a third ready issue is held back rather than
// launched over the lowered cap, until enough in-flight Boxes settle to bring
// live back under it.
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, session, fc, fc, f, s, fake, fresh)
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}

	// Fresh for the first refill (fills #1's slot), stale for every refill
	// after, including the second initial slot and #1's completion refill.
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
		resultCh <- RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
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
	// freeSlotSecs accumulation with a literal 0 left the whole waves suite
	// green, since the assertions below only checked >=0). The
	// mu.Lock()/idle.Wait() pairing in the bootstrap makes this scenario read
	// the clock exactly twice, so freeSlotSecs is computable by hand.
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{},
	}
	fake.PendingFunc = fakePending(fc, c, nil, nil)

	// Fresh for the first refill (fills #1's slot), stale for every refill
	// after, so #2 stays held back while #1 keeps running.
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
		resultCh <- RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
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

	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]
	if report.HeldBack != 1 || report.HeldBackUnknown {
		t.Fatalf("report: got HeldBack=%d HeldBackUnknown=%v, want HeldBack=1 HeldBackUnknown=false", report.HeldBack, report.HeldBackUnknown)
	}

	// The clock advances by exactly one tick between the two reads
	// (staleDrain.start, then the completion checkpoint), so Duration() is
	// exactly tick.
	wantDur := tick.Seconds()
	if dur := report.Duration().Seconds(); dur != wantDur {
		t.Fatalf("report.Duration(): got %v, want exactly %v (base+%v clock, two reads)", dur, wantDur, tick)
	}

	// freeSlotSecs accumulates (limiter.Cap()-outstanding)*elapsed across the
	// single interval between the two clock reads: Cap()=2, outstanding=1 (box
	// #1 still counted before its own decrement), so the exact value is
	// 1*tick, not merely >=0. Reverting the accumulation to a literal 0, or
	// any other wrong formula, must fail this assertion.
	wantFree := float64(2-1) * tick.Seconds()
	if report.FreeSlotSecs != wantFree {
		t.Fatalf("report.FreeSlotSecs: got %v, want exactly %v ((cap-outstanding)*tick = (2-1)*%v)", report.FreeSlotSecs, wantFree, tick)
	}

	if clockCalls != 2 {
		t.Fatalf("clock reads: got %d, want exactly 2 (staleDrain.start + completion checkpoint) -- test assumptions about the deterministic sequence no longer hold", clockCalls)
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{},
	}
	errDiscover := errors.New("tracker rate limited")
	fake.PendingErr = errDiscover

	// Fresh for the first refill (fills #1's slot), stale for every refill
	// after, so the stale-transition branch's Pending() call is the one that
	// errors.
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
			resultCh <- RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
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
// finding on #2678: heldBack must apply nextReady's blocked, touch-overlap
// and failed-check filtering, not just drop issues already claimed this run.
// #1 is ready and #2 is blocked by #9, so heldBack is 1; the old
// len(dropClaimed(issues, claimed)) counted both. Ported onto Pending (#2939).
func TestRunContinuous_StaleDrainHeldBackExcludesBlockedIssues(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "9", State: "OPEN"}) // #2's blocker, unmet

	edges := map[string][]string{"2": {"9"}}

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  edges,
	}
	fake.PendingFunc = fakePending(fc, c, edges, nil)
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	// Stale fires before any launch, so no Box ever dispatches: nil, nil for
	// the *dispatch.Factory and settle.Settler, mirroring
	// TestRunContinuous_ThroughFakeQueue_AllBlockedNeedsNoFactory
	// (queue_engine_test.go).
	err := RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}

	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]
	if report.HeldBack != 1 || report.HeldBackUnknown {
		t.Fatalf("report: got HeldBack=%d HeldBackUnknown=%v, want HeldBack=1 HeldBackUnknown=false (only #1 is ready; #2 is blocked by #9, so it must not inflate the count)", report.HeldBack, report.HeldBackUnknown)
	}
}

// TestRunContinuous_StaleDrainHeldBackExcludesTouchOverlapDeferredIssues pins
// the second of countReady's three documented exclusions (#2778): heldBack
// must also skip an issue deferred by the touch-overlap gate. #1 is ready; #2
// declares the same touch path as in-progress #9, so heldBack is 1, never 2.
// Ported forward onto the Queue.Pending seam (issue #2939).
func TestRunContinuous_StaleDrainHeldBackExcludesTouchOverlapDeferredIssues(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Body: "## Touches\n- lib/foo.nix", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "9", Body: "## Touches\n- lib/foo.nix", Labels: []string{testInProgressLabel}, State: "OPEN"}) // #2's overlap collider

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}
	fake.PendingFunc = fakePending(fc, c, nil, nil)
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	// Stale fires before any launch, so no Box ever dispatches: nil, nil for
	// the *dispatch.Factory and settle.Settler, mirroring
	// TestRunContinuous_ThroughFakeQueue_AllBlockedNeedsNoFactory
	// (queue_engine_test.go).
	err := RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}

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
// also skip an issue whose own DepsOf check failed. #1 is ready; #2 is marked
// failed via the discover closure's failed map, so heldBack is 1, never 2.
// Ported forward onto the Queue.Pending seam (issue #2939).
func TestRunContinuous_StaleDrainHeldBackExcludesDepsOfFailedIssues(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}}) // its own DepsOf check fails

	failed := map[string]bool{"2": true}

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Failed: failed,
	}
	fake.PendingFunc = fakePending(fc, c, nil, failed)
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	// Stale fires before any launch, so no Box ever dispatches: nil, nil for
	// the *dispatch.Factory and settle.Settler, mirroring
	// TestRunContinuous_ThroughFakeQueue_AllBlockedNeedsNoFactory
	// (queue_engine_test.go).
	err := RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}

	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]
	if report.HeldBack != 1 || report.HeldBackUnknown {
		t.Fatalf("report: got HeldBack=%d HeldBackUnknown=%v, want HeldBack=1 HeldBackUnknown=false (only #1 is ready; #2's own DepsOf check failed, so it must not inflate the count)", report.HeldBack, report.HeldBackUnknown)
	}
}

// TestRunContinuous_StaleDrainHeldBackCountsAllExclusionsWhenIgnoreBlockers is
// the counterexample to the two sibling exclusion tests above (#2778 review
// finding): under cfg.IgnoreBlockers (research-kind dispatch), issueReadiness
// skips both the DepsOf-failed guard and the blocker-edge computation, so #1,
// #2, and #3 all count ready and heldBack is 3. Ported onto Pending (#2939).
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}},
		Edges:  edges,
		Failed: failed,
	}
	fake.PendingFunc = fakePending(fc, c, edges, failed)
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	// Stale fires before any launch, so no Box ever dispatches: nil, nil for
	// the *dispatch.Factory and settle.Settler, mirroring
	// TestRunContinuous_ThroughFakeQueue_AllBlockedNeedsNoFactory
	// (queue_engine_test.go).
	err := RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}

	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]
	if report.HeldBack != 3 || report.HeldBackUnknown {
		t.Fatalf("report: got HeldBack=%d HeldBackUnknown=%v, want HeldBack=3 HeldBackUnknown=false (IgnoreBlockers skips both the DepsOf-failed exclusion and the blocker-edge check, so #1, #2, and #3 are all counted ready)", report.HeldBack, report.HeldBackUnknown)
	}
}

// TestRunContinuous_StaleDrainResizeBelowOutstandingClampsFreeSlotSecs proves
// three #2678 review findings compose: freeSlotSecs clamps at zero when a
// mid-drain lower puts staleDrain.cap under outstanding, staleDrain.cap stays
// frozen per interval instead of reading limiter.Cap() live, and the resize
// listener wakes on Resized() rather than raise-only Grown().
func TestRunContinuous_StaleDrainResizeBelowOutstandingClampsFreeSlotSecs(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 4
	// Cap 4, not 3: the staleness probe can only trip while a slot is still
	// free (Limiter.TryAcquire requires cap > live), so tripping it with all
	// three Boxes outstanding forces the frozen staleDrain.cap to be at least
	// outstanding+1. That is an inherent floor of the probe, not a choice.
	limiter := NewLimiter(4)
	session := &Session{Limiter: limiter}

	// Deterministic clock: this scenario reads it exactly five times (the
	// drain's start, the resize listener's checkpoint, then one per
	// completion). sawResizeCheckpoint closes on the SECOND call, which can
	// only be the resize listener's checkpoint and fires while that listener
	// still holds mu, so it pins that checkpoint as the first of the four.
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}},
		Edges:  map[string][]string{},
	}

	// drainBegun orders the test's own ResizeDelta after the drain has frozen
	// staleDrain.cap. The startedN signals only prove the Boxes launched; the
	// freeze happens later, in refill's stale branch, so a ResizeDelta racing
	// ahead of it gets baked into staleDrain.cap itself. Pending() runs once,
	// right after begin() under the same mu, so it is the earliest barrier.
	drainBegun := make(chan struct{})
	fake.PendingFunc = func(map[string]bool) (int, error) {
		close(drainBegun)
		return 0, nil
	}

	// Fresh for the first three refills (fills #1, #2, and #3's slots against
	// cap=4), stale for the fourth: the bootstrap's own attempt at a fourth
	// slot, which trips the freshness check while all three Boxes are
	// outstanding. That is the starting point this scenario needs.
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
		resultCh <- RunContinuous(c, session, fc, fc, f, s, fake, fresh)
	}()

	for _, ch := range []chan struct{}{started1, started2, started3} {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("#1, #2, and #3 should all have started with cap=4")
		}
	}

	select {
	case <-drainBegun:
	case <-time.After(2 * time.Second):
		t.Fatal("the bootstrap's fourth refill attempt should have tripped staleness")
	}

	// All three Boxes are outstanding and the drain is already underway. Drop
	// the live cap straight to the Limiter's floor, the operator action the
	// review finding calls out, further below outstanding than the original
	// 2-Box scenario exercised.
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

	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]

	free := report.FreeSlotSecs
	// The resize listener's checkpoint, guaranteed first of the four by the
	// sawResizeCheckpoint barrier above, contributes (4-3)*tick; it also
	// refreshes staleDrain.cap to the lowered cap of 1, so the three
	// completion checkpoints after it all clamp to zero. One tick total, which
	// an unclamped formula would instead cancel out to zero.
	wantFree := tick.Seconds()
	if free != wantFree {
		t.Fatalf("freeSlotSeconds: got %v, want exactly %v (first checkpoint at the frozen pre-resize cap, every checkpoint after it clamped to 0)", free, wantFree)
	}
	if free < 0 {
		t.Fatalf("freeSlotSeconds: got %v, must never be negative", free)
	}

	if clockCalls != 5 {
		t.Fatalf("clock reads: got %d, want exactly 5 (staleDrain.start + the resize listener's own checkpoint + three completion checkpoints) -- test assumptions about the deterministic sequence no longer hold", clockCalls)
	}
}

// TestRunContinuous_StaleDrainResizeUpCheckpointsBeforeCapChange verifies the
// other half of the #2678 review finding fixed above: freeSlotSecs must never
// read limiter.Cap() live at checkpoint time and apply it retroactively to the
// interval that just ended. A Console operator can raise the cap mid-drain
// (ADR 0023), and the grow listener must checkpoint at the OLD cap first.
func TestRunContinuous_StaleDrainResizeUpCheckpointsBeforeCapChange(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	// Cap 2 is the minimum that can trip the staleness probe with a single Box
	// outstanding, for the same TryAcquire-requires-cap>live reason the clamp
	// test above documents.
	limiter := NewLimiter(2)
	session := &Session{Limiter: limiter}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	const tick = 5 * time.Second
	var clockMu sync.Mutex
	clockCalls := 0
	// sawGrowCheckpoint closes on the SECOND now() call, which can only be the
	// grow listener's checkpoint and fires while that listener still holds mu,
	// so closing release1 after this signal can never race ahead of it.
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}},
		Edges:  map[string][]string{},
	}

	// drainBegun orders the ResizeDelta below after staleDrain.cap is frozen;
	// see the same barrier in
	// TestRunContinuous_StaleDrainResizeBelowOutstandingClampsFreeSlotSecs
	// above for why started1 alone is not that ordering.
	drainBegun := make(chan struct{})
	fake.PendingFunc = func(map[string]bool) (int, error) {
		close(drainBegun)
		return 0, nil
	}

	// Fresh for the first refill (fills #1's slot against cap=2), stale for
	// the second: the bootstrap's own probe attempt, which trips staleness
	// while #1 is still the sole outstanding Box.
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
		resultCh <- RunContinuous(c, session, fc, fc, f, s, fake, fresh)
	}()

	select {
	case <-started1:
	case <-time.After(2 * time.Second):
		t.Fatal("#1 should have started with cap=2")
	}

	select {
	case <-drainBegun:
	case <-time.After(2 * time.Second):
		t.Fatal("the bootstrap's second refill attempt should have tripped staleness")
	}

	// #1 is outstanding and the drain is already underway. Raise the live cap,
	// a Console "+" (ADR 0023), while #1 is still in flight.
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

	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]

	dur := report.Duration().Seconds()
	wantDur := 2 * tick.Seconds()
	if dur != wantDur {
		t.Fatalf("Duration(): got %v, want exactly %v (staleDrain.start + two checkpoints, one tick apart each)", dur, wantDur)
	}

	free := report.FreeSlotSecs
	// Interval 1 runs at the frozen old cap 2 with outstanding=1, interval 2
	// at the raised cap 10: (2-1)*tick + (10-1)*tick == ten ticks (50s).
	// Reading Cap() live at the completion checkpoint would credit the whole
	// drain at 10 instead.
	wantFree := (1 + 9) * tick.Seconds()
	if free != wantFree {
		t.Fatalf("FreeSlotSecs: got %v, want exactly %v (old cap credited before the raise, new cap only after it)", free, wantFree)
	}

	if clockCalls != 3 {
		t.Fatalf("clock reads: got %d, want exactly 3 (staleDrain.start + grow checkpoint + completion checkpoint) -- test assumptions about the deterministic sequence no longer hold", clockCalls)
	}
}

// TestRunContinuous_StaleDrainResizeDownAboveOutstandingCheckpointsBeforeCapChange
// verifies a review finding on #2678: the resize listener used to wake only on
// Limiter.Grown(), which never signals for a lower, so a mid-drain lower sat
// unnoticed until the next Box completed and that completion credited the
// whole interval at the stale, pre-lower staleDrain.cap.
func TestRunContinuous_StaleDrainResizeDownAboveOutstandingCheckpointsBeforeCapChange(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 6
	// Cap 6 lowered to 3 with only 2 Boxes outstanding (the review finding's
	// own numbers): the lowered cap stays above outstanding, so the clamp
	// never engages and an over-credit lands directly in the asserted total.
	// The sibling clamp test lowers to the floor of 1, where the clamp hides
	// exactly this bug.
	limiter := NewLimiter(6)
	session := &Session{Limiter: limiter}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	const tick = 5 * time.Second
	var clockMu sync.Mutex
	clockCalls := 0
	// sawResizeCheckpoint closes on the SECOND now() call, which can only be
	// the resize listener's checkpoint and fires while that listener still
	// holds mu, so closing either release after this signal can never race
	// ahead of it.
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{},
	}

	// drainBegun orders the ResizeDelta below after staleDrain.cap is frozen;
	// see the same barrier in
	// TestRunContinuous_StaleDrainResizeBelowOutstandingClampsFreeSlotSecs
	// above for why started1/started2 alone are not that ordering.
	drainBegun := make(chan struct{})
	fake.PendingFunc = func(map[string]bool) (int, error) {
		close(drainBegun)
		return 0, nil
	}

	// Fresh for the first two refills (fills #1's and #2's slots against
	// cap=6), stale for the third: the bootstrap's own probe attempt, which
	// trips staleness while #1 and #2 are still the only outstanding Boxes.
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
		resultCh <- RunContinuous(c, session, fc, fc, f, s, fake, fresh)
	}()

	for _, ch := range []chan struct{}{started1, started2} {
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("#1 and #2 should both have started with cap=6")
		}
	}

	select {
	case <-drainBegun:
	case <-time.After(2 * time.Second):
		t.Fatal("the bootstrap's third refill attempt should have tripped staleness")
	}

	// #1 and #2 are outstanding and the drain is already underway. Lower the
	// live cap, a Console "-" (ADR 0023), while both are still in flight,
	// staying above the outstanding count so the clamp never engages.
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

	if len(fake.ReportStaleDrainCalls) != 1 {
		t.Fatalf("ReportStaleDrainCalls: got %d, want exactly 1", len(fake.ReportStaleDrainCalls))
	}
	report := fake.ReportStaleDrainCalls[0]

	dur := report.Duration().Seconds()
	wantDur := 3 * tick.Seconds()
	if dur != wantDur {
		t.Fatalf("Duration(): got %v, want exactly %v (staleDrain.start + three checkpoints, one tick apart each)", dur, wantDur)
	}

	free := report.FreeSlotSecs
	// (6-2)*tick at the frozen old cap, then (3-2)*tick and (3-1)*tick after
	// the lower: seven ticks (35s), where leaving staleDrain.cap frozen at 6
	// gives nine. Which Box completes first does not matter, since the first
	// completion checkpoint always sees outstanding=2 and the second 1.
	wantFree := (4 + 1 + 2) * tick.Seconds()
	if free != wantFree {
		t.Fatalf("FreeSlotSecs: got %v, want exactly %v (old cap credited before the lower, new cap only after it -- an over-credited total here would mean the resize listener never checkpointed the lower)", free, wantFree)
	}

	if clockCalls != 4 {
		t.Fatalf("clock reads: got %d, want exactly 4 (staleDrain.start + resize checkpoint + two completion checkpoints) -- test assumptions about the deterministic sequence no longer hold", clockCalls)
	}
}

// TestRunContinuous_AllBlockedReturnsErrOpenNoneDispatchable verifies that
// exit-3 semantics are unchanged in continuous mode (#527 AC): when nothing in
// the initial batch is ever dispatchable, RunContinuous returns
// ErrOpenNoneDispatchable exactly as drainMaxJobs does for a batch wave,
// rather than hanging on a refill event that can never come.
func TestRunContinuous_AllBlockedReturnsErrOpenNoneDispatchable(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", State: "OPEN"}) // blocker, not complete

	edges := map[string][]string{"1": {"2"}}
	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}},
		Edges:  edges,
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	// nil, nil: nothing ever dispatches, so a nil *dispatch.Factory and
	// settle.Settler is a stronger guarantee than an fr.RunCalls==0
	// assertion. With no Factory, dispatch is impossible, not merely
	// unobserved.
	err := RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)
	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("RunContinuous: got %v, want ErrOpenNoneDispatchable", err)
	}
}

// TestRunContinuous_RateLimitedRediscoverRetriesWithBackoffThenSucceeds
// verifies issue #2866: a re-discover that fails with forge.ErrRateLimit
// retries with backoff instead of ending the run. Discover fails twice then
// succeeds, so the run must dispatch and return nil, sleeping through the fake
// Clock for exactly the 2 rate-limited attempts (1s, 2s).
func TestRunContinuous_RateLimitedRediscoverRetriesWithBackoffThenSucceeds(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.Policy.Max = 3
	c.Policy.Unit = time.Second

	var sleeps []time.Duration
	c.Clock = fakeWavesClock(time.Time{}, &sleeps)

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	fake := NewFakeQueue()
	fake.DiscoverFunc = func(callN int) (Batch, error) {
		if callN <= 2 {
			return Batch{}, fmt.Errorf("%w: rate limited", forge.ErrRateLimit)
		}
		return Batch{Issues: []Issue{{Number: "1"}}}, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	err := RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
	if err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}

	lb := retry.LinearBackoff{Unit: c.Policy.Unit, Clock: c.Clock}
	wantSleeps := []time.Duration{lb.Duration(1), lb.Duration(2)}
	if len(sleeps) != len(wantSleeps) || sleeps[0] != wantSleeps[0] || sleeps[1] != wantSleeps[1] {
		t.Fatalf("sleeps: got %v, want %v", sleeps, wantSleeps)
	}
}

// TestRunContinuous_RateLimitedRediscoverExhaustsRetries verifies issue
// #2866's exhaustion path: a re-discover that keeps failing with
// forge.ErrRateLimit for Policy.Max+1 attempts must give up the way a
// non-rate-limit error does (refill returns false, no panic, no infinite
// loop), but its stderr message must name rate limiting as the cause.
func TestRunContinuous_RateLimitedRediscoverExhaustsRetries(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.Policy.Max = 2
	c.Policy.Unit = time.Second

	var sleeps []time.Duration
	c.Clock = fakeWavesClock(time.Time{}, &sleeps)

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fake := NewFakeQueue()
	fake.DiscoverErr = fmt.Errorf("%w: rate limited", forge.ErrRateLimit)
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	var err error
	out := testutil.CaptureStderr(t, func() {
		err = RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)
	})

	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("RunContinuous: got %v, want ErrOpenNoneDispatchable", err)
	}
	if fake.DiscoverCalls != 1+c.Policy.Max {
		t.Fatalf("DiscoverCalls: got %d, want %d (1 initial + Policy.Max retries)", fake.DiscoverCalls, 1+c.Policy.Max)
	}
	if len(sleeps) != c.Policy.Max {
		t.Fatalf("sleeps: got %d, want %d (one backoff sleep before each retry, none after the final failed attempt)", len(sleeps), c.Policy.Max)
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
	c.Policy.Max = 2
	c.Policy.Unit = time.Second

	var sleeps []time.Duration
	c.Clock = fakeWavesClock(time.Time{}, &sleeps)

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	wantErr := errors.New("boom")
	fake := NewFakeQueue()
	fake.DiscoverErr = wantErr
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	var err error
	out := testutil.CaptureStderr(t, func() {
		err = RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)
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
// provenance for each blocker) survives the trip through the refill loop
// instead of being silently discarded. #2's declared blocker is body-parsed,
// so the run must dispatch only the unblocked #1 and leave #2 held.
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: out, Edges: result.Edges, Sources: result.Sources, Failed: result.Failed}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, nil, fc, fc, f, s, fake, fresh); err != nil {
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

	// Cyclic dependency among all three in-batch issues: 1 -> 2 -> 3 -> 1.
	edges := map[string][]string{
		"1": {"2"},
		"2": {"3"},
		"3": {"1"},
	}
	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}, {Number: "3"}},
		Edges:  edges,
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	var err error
	resultCh := make(chan error, 1)
	errOut := testutil.CaptureStderr(t, func() {
		resultCh <- RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)
	})

	select {
	case err = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return — cycle guard may have hung a refill")
	}

	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("RunContinuous: got %v, want ErrOpenNoneDispatchable (no issue in the cycle is ever dispatchable)", err)
	}
	// No Box may launch for a cyclic batch; a nil *dispatch.Factory makes that
	// structural (a launch attempt would nil-panic), so there is no separate
	// RunCalls count to assert here.
	if !strings.Contains(errOut, "cycle") || !strings.Contains(errOut, "#1") {
		t.Fatalf("stderr missing cycle report naming issue #1, got:\n%s", errOut)
	}
}

// TestRunContinuous_StaleDiscoveryNeverDoubleDispatches verifies #560: a
// Discoverer that keeps listing an already-claimed issue as dispatchable,
// modeling GitHub's eventually-consistent search index right after the label
// swap, must not launch a second Box for it, and the suppressed re-discovery
// must not re-attempt the dispatch-state transition.
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
	// Real settle, not settle.NewFake(): the TransitionStateCalls==1 assertion
	// below only holds because a real Settle performs the InProgress -> Failed
	// demotion on fc for the outcome-less, PR-less run (issue #1605).
	// settle.NewFake() records calls on itself and never touches fc, so that
	// assertion would see 0 instead.
	s := newSettle(fc, fc)

	// Always reports #1 as dispatchable regardless of the claim already made
	// against it: a stale search result, not a live forge query. The same
	// batch on every call is what a static DiscoverReturn models, by design.
	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1", Title: "stale"}}, Edges: map[string][]string{}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	var err error
	out := testutil.CaptureStdout(t, func() {
		err = RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
	})
	if err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1 (stale re-discovery of #1 must not double-dispatch)", len(fr.RunCalls))
	}
	// The claim flows through FakeQueue.Claim, which never touches fc, so the
	// only entry left in fc.TransitionStateCalls is real settle's demotion of
	// the box's outcome-less, PR-less run (InProgress -> Failed, issue #1605).
	// A second entry would mean the suppressed stale re-discovery re-attempted
	// the Failed transition.
	if len(fc.TransitionStateCalls) != 1 {
		t.Fatalf("TransitionStateCalls: got %d, want 1 (suppressed stale entry must not re-attempt settle's transition)", len(fc.TransitionStateCalls))
	}
	// fake.ClaimCalls now proves the claim happened exactly once, replacing
	// the coverage the old TransitionStateCalls==2 count gave the claim half
	// before Claim moved onto the FakeQueue.
	if len(fake.ClaimCalls) != 1 || fake.ClaimCalls[0] != "1" {
		t.Fatalf("ClaimCalls: got %v, want [\"1\"] (stale re-discovery of #1 must not double-claim)", fake.ClaimCalls)
	}
	if strings.Contains(out, "already claimed this run") {
		t.Fatalf("output must not log the stale re-discovery skip line, got:\n%s", out)
	}
}

// TestRunContinuous_TerminatedIssueSkipsFailedTransitionAndSettle verifies
// that when a Box's issue is marked on cfg.Terminated (Terminate landed while
// it was running, ADR 0024, issue #649), a non-zero exit is neither
// transitioned to Failed nor handed to Settle: Terminate already transitioned
// the issue to Dispatchable, and a Failed transition here would corrupt that.
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}, Edges: map[string][]string{}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, session, fc, fc, f, fakeSettle, fake, fresh); err != nil {
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
// non-zero with no termination in play, RunContinuous transitions the tracker
// issue to Failed and calls the Settler's Fail hook, the seam a wrapper like
// the Console's queueSettler uses to move its queue row to a terminal state
// instead of stranding it at "running" (issue #705).
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}, Edges: map[string][]string{}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, nil, fc, fc, f, fakeSettle, fake, fresh); err != nil {
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

// TestRunContinuous_FailedBoxWithEmptyLogPrintsErrToStderr is
// TestRunContinuous_FailedBoxCallsSettlerFail's stderr-output counterpart:
// a box that never launched (Result.Err populated per dispatch/retry.go)
// must have its reason surfaced next to the FAILED line (issue #3119).
func TestRunContinuous_FailedBoxWithEmptyLogPrintsErrToStderr(t *testing.T) {
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}, Edges: map[string][]string{}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	errOut := testutil.CaptureStderr(t, func() {
		if err := RunContinuous(c, nil, fc, fc, f, fakeSettle, fake, fresh); err != nil {
			t.Fatalf("RunContinuous: got %v, want nil", err)
		}
	})

	if !strings.Contains(errOut, "?? #1: ") || !strings.Contains(errOut, boxErr.Error()) {
		t.Errorf("want a '?? #1: <err>' diagnostic line on stderr; got stderr=%q", errOut)
	}
}

// TestRunContinuous_FailedBoxWithLogOutputPrintsNoExtraStderr verifies that
// a box that ran and genuinely failed (left content in its log) leaves
// Result.Err nil, so RunContinuous's completion handler prints no extra
// "??" diagnostic beyond the terse FAILED line (issue #3119).
func TestRunContinuous_FailedBoxWithLogOutputPrintsNoExtraStderr(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fr := runner.NewFake()
	fr.RunErr = boxErr
	fr.WriteToOutput = []byte("some box output before it failed\n")

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	fakeSettle := settle.NewFake()

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}, Edges: map[string][]string{}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	errOut := testutil.CaptureStderr(t, func() {
		if err := RunContinuous(c, nil, fc, fc, f, fakeSettle, fake, fresh); err != nil {
			t.Fatalf("RunContinuous: got %v, want nil", err)
		}
	})

	if strings.Contains(errOut, "?? #1") {
		t.Errorf("want no '?? #1' diagnostic when the box ran and produced log output; got stderr=%q", errOut)
	}
}

// TestRunContinuous_RefillHoldsDepsOfFailedIssue verifies that a refill's
// Discoverer naming an issue in its failed set (#1103, the Discoverer's own
// NewReadiness/DepsOf call errored) holds it rather than dispatching it, the
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
	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{},
		Failed: failed,
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, nil, fc, fc, f, s, fake, fresh); err != nil {
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
// #4/#5 are still invisible to discover, stranding two free slots; #3's later
// completion reveals both, and a single refill() call there launches only one.
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
	fake := NewFakeQueue()
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
		resultCh <- RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
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

	// Bootstrap drains discover three times, one per launch of #1, #2, #3. Its
	// own terminating refill() attempt never reaches discover: with all three
	// slots claimed, TryAcquire fails first, so that check is silent.
	drain(3)

	// #1 and #2 complete immediately (no RunFunc case blocks them). Their
	// mu-serialized completion handlers each fire exactly one refill attempt
	// while #4/#5 are still invisible, so both find only the already-claimed
	// #1-#3 and strand their freed slot.
	drain(2)

	// The backlog becomes visible now that both stranded slots already exist.
	// #3 holds the only running slot, so releasing it is the sole remaining
	// trigger that can ever revisit those two free slots.
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
// slot a refill attempt couldn't fill, because the ready issue wasn't yet
// visible in discover's result, gets picked up by a later background poll
// tick, with no Box ever completing to trigger it. #2 becomes visible while #1
// is still running, so only the poll ticker can be what launches it.
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
	fake := NewFakeQueue()
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
		resultCh <- RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
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
	// terminating refill() attempt that finds the second slot's only candidate
	// (#1) already claimed and strands the slot. Both calls happen inside the
	// single initial drainRefill(), which holds mu for its whole loop, so the
	// poll ticker cannot contribute a call until it reaches idle.Wait().
	drain(2)

	// #2 becomes visible now that the slot is already stranded. #1 is still
	// running, so only a poll tick can revisit this slot.
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

// TestRunContinuous_RefillDispatchesInPriorityOrder verifies the #2281 review
// finding: refill sorts the discovered pool by Priority (forge.SortByPriority)
// before picking, so dispatch is priority-ordered end to end, not just in
// plan_test.go's isolated unit coverage. MaxParallel=1 makes fr.RunCalls the
// launch order, and the five issues are seeded out of priority order on purpose.
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
	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: out, Edges: map[string][]string{}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, nil, fc, fc, f, s, fake, fresh); err != nil {
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
// #2678's zero-in-flight case: when the stale verdict fires on the very first
// refill, before anything has launched, the drain is already over, so
// RunContinuous reports it immediately with a zero-length duration rather than
// reporting nothing for want of a completion to trigger a later report.
func TestRunContinuous_StaleWithNothingInFlightReportsZeroLengthDrain(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}},
		Edges:  map[string][]string{},
	}
	fake.PendingFunc = fakePending(fc, c, nil, nil)
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	// Freshness is stale from the very first refill, so no Box ever launches;
	// a nil *dispatch.Factory/settle.Settler makes that structural (a launch
	// attempt would nil-panic), the same pattern
	// TestRunContinuous_RefillCycleGuardSkipsAndReports uses.
	err := RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}

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

// TestRunContinuous_ClaimedSetReachesPendingVerbatim pins the call-site half
// of issue #3035: the map RunContinuous threads into Queue.Pending(claimed) is
// its own live claimed set, not an empty or stale one. A regression that
// passed the wrong map would desync FakeQueue.Claimed from what PendingFunc
// observes, while every other stale-drain assertion stayed green.
func TestRunContinuous_ClaimedSetReachesPendingVerbatim(t *testing.T) {
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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{},
	}
	realPending := fakePending(fc, c, nil, nil)

	// Copy claimed rather than keeping the reference: Queue.Pending's contract
	// is that the map stays the caller's, valid only for the duration of the
	// call, so an assertion made after RunContinuous returns has to run
	// against a snapshot taken inside it.
	var observedMu sync.Mutex
	var observed map[string]bool
	fake.PendingFunc = func(claimed map[string]bool) (int, error) {
		observedMu.Lock()
		observed = make(map[string]bool, len(claimed))
		for num := range claimed {
			observed[num] = true
		}
		observedMu.Unlock()
		return realPending(claimed)
	}

	// Fresh for the first refill (claims #1), stale for every refill after, so
	// #2 stays held back and the stale transition's single
	// queue.Pending(claimed) call fires with #1 already claimed.
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
	go func() {
		resultCh <- RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
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

	observedMu.Lock()
	defer observedMu.Unlock()
	if len(observed) == 0 {
		t.Fatal("PendingFunc's claimed set is empty; scenario failed to exercise a claim before the stale transition")
	}
	if !reflect.DeepEqual(observed, fake.Claimed) {
		t.Fatalf("claimed set seen by Pending = %v, want exactly FakeQueue.Claimed (what Queue.Claim recorded) = %v", observed, fake.Claimed)
	}
}

// TestReportStaleDrainReleasingMu_ReleasesMuAroundIO verifies the contract
// reportStaleDrainReleasingMu's call sites (#2775, both stale-drain emission
// sites in continuous.go's refill/completion paths) rely on: mu is held on
// entry, released across queue.ReportStaleDrain's blocking I/O, and
// re-acquired before returning.
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

// TestRunContinuous_StopClosedBeforeCall_NonEmptyQueue_ReturnsErrSignalledStop
// covers #3520's pre-launch case: a Stop already closed when RunContinuous is
// called must win the very first refill guard, so nothing ever claims or
// launches despite a dispatchable issue sitting in the queue.
func TestRunContinuous_StopClosedBeforeCall_NonEmptyQueue_ReturnsErrSignalledStop(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	stop := make(chan struct{})
	close(stop)
	c.Stop = stop

	// nil, nil: a stopped-before-call run must never reach dispatch.Factory.New
	// at all, so a nil Factory/Settler is a stronger guarantee than an
	// fr.RunCalls==0 assertion would be.
	var err error
	out := captureStdout(t, func() {
		err = RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)
	})
	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
	}
	if len(fake.ClaimCalls) != 0 {
		t.Fatalf("ClaimCalls: got %v, want none (stop closed before any refill)", fake.ClaimCalls)
	}
	// Nothing was ever outstanding, so the line must not claim a drain is
	// under way.
	if !strings.Contains(out, "nothing in flight") {
		t.Fatalf("stdout: got %q, want the nothing-in-flight line", out)
	}
	if strings.Contains(out, "draining outstanding work") {
		t.Fatalf("stdout: got %q, want no draining line (queue was empty)", out)
	}
}

// TestRunContinuous_StopClosedBeforeCall_EmptyQueue_ReturnsErrSignalledStopNotOpenNoneDispatchable
// covers #3520's exit-code precedence: with nothing dispatchable, the
// terminal switch could plausibly return ErrOpenNoneDispatchable instead, but
// a signalled stop must win that race too — it is a distinct exit code (7),
// never 3.
func TestRunContinuous_StopClosedBeforeCall_EmptyQueue_ReturnsErrSignalledStopNotOpenNoneDispatchable(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", State: "OPEN"}) // never dispatchable: unlabeled

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	stop := make(chan struct{})
	close(stop)
	c.Stop = stop

	var err error
	out := captureStdout(t, func() {
		err = RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)
	})
	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
	}
	if errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("RunContinuous: got ErrOpenNoneDispatchable wrapped in, want ErrSignalledStop only")
	}
	if !strings.Contains(out, "nothing in flight") {
		t.Fatalf("stdout: got %q, want the nothing-in-flight line", out)
	}
	if strings.Contains(out, "draining outstanding work") {
		t.Fatalf("stdout: got %q, want no draining line (queue was empty)", out)
	}
}

// TestRunContinuous_StopClosedWhileBoxInFlight_DrainsWithoutFurtherLaunchAndPrintsOnce
// covers #3520's drain: a Stop closed mid-run lets the in-flight Box finish
// and settle, blocks the poll ticker's own refill attempts (pollInterval is
// overridden short enough for several ticks to fire before release), and
// prints the drain line exactly once despite every one of those attempts
// re-checking the latch.
func TestRunContinuous_StopClosedWhileBoxInFlight_DrainsWithoutFurtherLaunchAndPrintsOnce(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.pollInterval = 5 * time.Millisecond

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

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

	stop := make(chan struct{})
	c.Stop = stop

	var err error
	out := captureStdout(t, func() {
		resultCh := make(chan error, 1)
		go func() {
			resultCh <- RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
		}()

		select {
		case <-started1:
		case <-time.After(2 * time.Second):
			t.Fatal("issue #1 was never dispatched")
		}

		close(stop)
		// Long enough for several 5ms poll ticks to fire while #1 is still
		// running, proving the guard holds across repeated refill attempts,
		// not just the first one.
		time.Sleep(50 * time.Millisecond)
		close(release1)

		select {
		case err = <-resultCh:
		case <-time.After(2 * time.Second):
			t.Fatal("RunContinuous did not return after #1 was released")
		}
	})

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("RunCalls: got %v, want exactly issue 1 (no launch after stop, including by the poll ticker)", fr.RunCalls)
	}
	if got := strings.Count(out, "stop requested"); got != 1 {
		t.Fatalf("drain line printed %d time(s) in stdout, want exactly 1:\n%s", got, out)
	}
}

// TestRunContinuous_StopClosedAsLastBoxCompletes_ReturnsErrSignalledStop
// pins the latest a stop can still be observed (#3520): closed from inside the
// only Box's Run, it lands after that Box was launched and just as the run
// starts winding down, and must still be the verdict rather than the nil one
// an otherwise-complete drain would return.
func TestRunContinuous_StopClosedAsLastBoxCompletes_ReturnsErrSignalledStop(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	stop := make(chan struct{})
	c.Stop = stop

	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error {
		close(stop)
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	err := RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
	}
}

// TestRunContinuous_StaleThenStop_ReturnsErrSignalledStopNotErrImageStale
// covers #3520's precedence when the image goes stale first: a stop that
// arrives afterward, while the stale drain is still awaiting the in-flight
// Box, must still win the terminal switch.
func TestRunContinuous_StaleThenStop_ReturnsErrSignalledStopNotErrImageStale(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

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

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}

	// Fresh for the first refill (fills #1's slot), stale for every refill
	// after, same shape as TestRunContinuous_StaleProbeStopsRefillLetsInFlightFinish.
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

	stop := make(chan struct{})
	c.Stop = stop

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
	}()

	select {
	case <-started1:
	case <-time.After(2 * time.Second):
		t.Fatal("issue #1 was never dispatched")
	}

	// #1's own slot filled fresh; the second initial refill attempt (slot 2)
	// goes stale, so by now the run is already draining on staleness alone.
	// Signal the stop on top of that in-progress stale drain.
	close(stop)
	close(release1)

	select {
	case err := <-resultCh:
		if !errors.Is(err, ErrSignalledStop) {
			t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
		}
		if errors.Is(err, ErrImageStale) {
			t.Fatalf("RunContinuous: got ErrImageStale wrapped in, want ErrSignalledStop only (stop must win over stale)")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return")
	}
}

// TestRunContinuous_StopThenStale_ReturnsErrSignalledStopNotErrImageStale
// covers #3520's precedence in the other detection order: a stop closed
// before RunContinuous is even called must win even though the freshness
// checker would otherwise report stale on the very first refill.
func TestRunContinuous_StopThenStale_ReturnsErrSignalledStopNotErrImageStale(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}}
	// Would report stale on the very first call — never reached, since the
	// stop guard short-circuits refill before fresh() is ever consulted.
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	stop := make(chan struct{})
	close(stop)
	c.Stop = stop

	err := RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)
	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
	}
	if errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got ErrImageStale wrapped in, want ErrSignalledStop only (stop must win over stale)")
	}
}

// TestRunContinuous_NonNilStopNeverClosed_OrdinaryResultUnaffected covers
// #3520's negative case: a non-nil Stop that the operator never closes must
// not change RunContinuous's ordinary outcome, and closing it after the call
// has already returned must be harmless — no panic, no further launch, no
// goroutine left writing to state this call already returned.
func TestRunContinuous_NonNilStopNeverClosed_OrdinaryResultUnaffected(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	stop := make(chan struct{})
	c.Stop = stop

	err := RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
	if err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}

	// Closing after return must not panic or race with anything still
	// running: the stop watcher goroutine, if any, is already confirmed
	// exited before RunContinuous returns.
	close(stop)
}

// TestRunContinuous_StopClosedDuringRateLimitBackoffSleep_LaunchesNothing
// reproduces the #3520 review finding at continuous.go:351: a stop closed
// while refill is sleeping out a forge.ErrRateLimit re-discover backoff must
// abort that refill on wake, not fall through to a re-discover that
// succeeds and claims/launches one more Box. Discover fails once (forcing
// the backoff sleep) then would succeed on the very next call if reached —
// the fix must never let refill reach that second call once Sleep's own
// stop-close has landed.
func TestRunContinuous_StopClosedDuringRateLimitBackoffSleep_LaunchesNothing(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.Policy.Max = 3
	c.Policy.Unit = time.Second

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	stop := make(chan struct{})
	c.Stop = stop

	// The Sleep injection is the reviewer's own repro: close cfg.Stop from
	// inside the backoff sleep, then see whether refill still launches.
	c.Clock = retry.Clock{
		Now: func() time.Time { return time.Time{} },
		Sleep: func(time.Duration) {
			close(stop)
		},
	}

	fake := NewFakeQueue()
	fake.DiscoverFunc = func(callN int) (Batch, error) {
		if callN == 1 {
			return Batch{}, fmt.Errorf("%w: rate limited", forge.ErrRateLimit)
		}
		// Only reached if the post-sleep stop check is missing; must never
		// happen once the fix lands.
		return Batch{Issues: []Issue{{Number: "1"}}}, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	var err error
	out := captureStdout(t, func() {
		err = RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
	})

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %v, want none (stop closed during the backoff sleep must abort before any launch)", fr.RunCalls)
	}
	if fake.DiscoverCalls != 1 {
		t.Fatalf("DiscoverCalls: got %d, want 1 (the post-sleep stop check must return before a second re-discover)", fake.DiscoverCalls)
	}
	if !strings.Contains(out, "stop requested") {
		t.Fatalf("stdout: got %q, want a stop-requested line", out)
	}
}

// TestRunContinuous_StopClosedDuringRateLimitBackoffSleep_PrintsPromptlyNotAfterFullBackoff
// reproduces the #3520 review finding at continuous.go:333: refill used to
// hold mu across the backoff clock.Sleep, so the dedicated stop-watcher
// goroutine (which needs mu to print) was blocked out until the sleep
// returned. This drives a Sleep that blocks on a channel the test alone
// controls, closes cfg.Stop while that sleep is still blocked, and asserts
// the drain line appears on stdout before the sleep is ever released. A
// regression to a mu-held sleep reproduces the finding exactly: the watcher
// cannot take mu until Sleep returns, so the line never appears within the
// bounded wait below and the test times out instead of passing.
func TestRunContinuous_StopClosedDuringRateLimitBackoffSleep_PrintsPromptlyNotAfterFullBackoff(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.Policy.Max = 3
	c.Policy.Unit = time.Second

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	sleeping := make(chan struct{})
	release := make(chan struct{})
	c.Clock = retry.Clock{
		Now: func() time.Time { return time.Time{} },
		Sleep: func(time.Duration) {
			close(sleeping)
			<-release
		},
	}

	fake := NewFakeQueue()
	fake.DiscoverErr = fmt.Errorf("%w: rate limited", forge.ErrRateLimit)
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	stop := make(chan struct{})
	c.Stop = stop

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = orig })

	// captureStdout can't serve this test: it reads the pipe only after fn
	// returns, and the assertion below must see a line while RunContinuous is
	// still mid-run. Hence the hand-swap, plus a scanner goroutine that never
	// blocks on a full channel and always ends when closeStdout runs.
	lines := make(chan string, 16)
	scannerDone := make(chan struct{})
	go func() {
		defer close(scannerDone)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			default:
			}
		}
	}()
	var closeOnce sync.Once
	closeStdout := func() { closeOnce.Do(func() { _ = w.Close() }) }
	t.Cleanup(func() {
		closeStdout()
		<-scannerDone
	})

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)
	}()

	select {
	case <-sleeping:
	case <-time.After(2 * time.Second):
		t.Fatal("refill never reached the backoff sleep")
	}

	close(stop)

	// The sleep is held open by release, not by a duration, so seeing the
	// line here proves the watcher printed it without the sleep returning —
	// exactly the promptness the finding says a mu-held sleep breaks.
	found := false
	deadline := time.After(1 * time.Second)
	for !found {
		select {
		case line := <-lines:
			if strings.Contains(line, "stop requested") {
				found = true
			}
		case <-deadline:
			t.Fatal("stop-requested line not observed while the backoff sleep was still held open")
		}
	}

	close(release)

	select {
	case err = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return after the backoff sleep was released")
	}
	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
	}
}

// killHook wraps runner.Fake so an abort test can wait for terminate.Reclaim's
// Kill to actually land before letting a blocked RunFunc return (#3521): in
// production, Kill terminating the real sandbox is what makes a Box's Run()
// return, an ordering the plain Fake cannot reproduce on its own since its
// Kill only records the call.
type killHook struct {
	*runner.Fake
	killed chan string
}

func newKillHook(fr *runner.Fake) *killHook {
	return &killHook{Fake: fr, killed: make(chan string, 8)}
}

func (k *killHook) Kill(name string) error {
	err := k.Fake.Kill(name)
	k.killed <- name
	return err
}

// settleSignal wraps settle.Fake so a test can wait for a Settle call to have
// actually landed before moving on, since Fake.Settle records the call with
// no way for a caller to block on it otherwise.
type settleSignal struct {
	*settle.Fake
	settled chan string
}

func (s *settleSignal) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) {
	s.Fake.Settle(d, num, gen, result)
	s.settled <- num
}

// waitOn fails the test if ch does not receive within 2s, the same timeout
// this file's other RunContinuous scenarios use throughout. Generic so it
// covers both a started-signal (chan struct{}) and a killed/settled-name
// channel (chan string).
func waitOn[T any](t *testing.T, ch <-chan T, msg string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal(msg)
		var zero T
		return zero
	}
}

// assertReclaimedToDispatchable fails t unless fc.TransitionStateCalls
// contains a num InProgress->Dispatchable transition, the reclaim check
// three of the abort tests below share.
func assertReclaimedToDispatchable(t *testing.T, fc *forge.Fake, num string) {
	t.Helper()
	for _, call := range fc.TransitionStateCalls {
		if call.Num == num && call.From == forge.InProgress && call.To == forge.Dispatchable {
			return
		}
	}
	t.Fatalf("TransitionStateCalls: got %+v, want #%s InProgress->Dispatchable", fc.TransitionStateCalls, num)
}

// TestRunContinuous_AbortClosedWhileBoxInFlight_ReclaimsAndAbandons covers
// #3521 items 1 and 2: an Abort closed while one Box is in flight reclaims it
// (kill + tracker back to Dispatchable) rather than waiting for it to finish
// on its own, and the Box's own completion goroutine abandons — Reclaim's
// Registry mark means it neither Fails nor Settles the issue Reclaim already
// released. Uses a Session-supplied Registry, covering that registry source.
func TestRunContinuous_AbortClosedWhileBoxInFlight_ReclaimsAndAbandons(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	started1 := make(chan struct{})
	release1 := make(chan struct{})
	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error {
		if box.Issue == "1" {
			close(started1)
			<-release1
		}
		return nil
	}
	kr := newKillHook(fr)

	dir := tempLogDir(t)
	f := testFactory(t, dir, kr)
	s := settle.NewFake()

	abort := make(chan struct{})
	c.Abort = abort
	session := &Session{Terminated: terminate.NewRegistry()}

	var err error
	out := captureStdout(t, func() {
		resultCh := make(chan error, 1)
		go func() {
			resultCh <- RunContinuous(c, session, fc, fc, f, s, fake, fresh)
		}()

		waitOn(t, started1, "issue #1 was never dispatched")
		close(abort)
		if got := waitOn(t, kr.killed, "Kill was never called after abort"); got != "agent-issue-1" {
			t.Fatalf("Kill: got %q, want agent-issue-1", got)
		}
		close(release1)

		select {
		case err = <-resultCh:
		case <-time.After(2 * time.Second):
			t.Fatal("RunContinuous did not return")
		}
	})

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("RunCalls: got %v, want exactly issue 1 (no launch after abort)", fr.RunCalls)
	}
	if len(kr.Fake.KillCalls) != 1 || kr.Fake.KillCalls[0] != "agent-issue-1" {
		t.Fatalf("KillCalls: got %v, want exactly [agent-issue-1]", kr.Fake.KillCalls)
	}
	var toDispatchable int
	for _, call := range fc.TransitionStateCalls {
		if call.Num == "1" && call.To == forge.Dispatchable {
			toDispatchable++
		}
	}
	if toDispatchable != 2 {
		t.Fatalf("TransitionStateCalls: got %+v, want two transitions of #1 to Dispatchable", fc.TransitionStateCalls)
	}
	if len(s.FailCalls) != 0 {
		t.Fatalf("FailCalls: got %+v, want none (abort abandons, never fails)", s.FailCalls)
	}
	if len(s.SettleCalls) != 0 {
		t.Fatalf("SettleCalls: got %+v, want none (abort abandons, never settles)", s.SettleCalls)
	}
	if !strings.Contains(out, "==> abort requested; terminating 1 outstanding Box(es)") {
		t.Fatalf("stdout: got %q, want the abort-terminating line", out)
	}
	if !strings.Contains(out, "terminated by operator; abandoning") {
		t.Fatalf("stdout: got %q, want the abandon line from the reclaimed Box's own goroutine", out)
	}
}

// TestRunContinuous_AbortClosedWithMultipleBoxesInFlight_ReclaimsEachOne
// covers #3521 item 3: every in-flight Box is reaped, not just the first one
// found, when more than one is outstanding.
func TestRunContinuous_AbortClosedWithMultipleBoxesInFlight_ReclaimsEachOne(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	started1 := make(chan struct{})
	started2 := make(chan struct{})
	release1 := make(chan struct{})
	release2 := make(chan struct{})
	fr := runner.NewFake()
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
	kr := newKillHook(fr)

	dir := tempLogDir(t)
	f := testFactory(t, dir, kr)
	s := settle.NewFake()

	abort := make(chan struct{})
	c.Abort = abort

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
	}()

	waitOn(t, started1, "issue #1 was never dispatched")
	waitOn(t, started2, "issue #2 was never dispatched")
	close(abort)

	killedNums := []string{waitOn(t, kr.killed, "first Kill never observed"), waitOn(t, kr.killed, "second Kill never observed")}
	slices.Sort(killedNums)
	if want := []string{"agent-issue-1", "agent-issue-2"}; !slices.Equal(killedNums, want) {
		t.Fatalf("killed: got %v, want %v", killedNums, want)
	}

	close(release1)
	close(release2)

	select {
	case err := <-resultCh:
		if !errors.Is(err, ErrSignalledStop) {
			t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return")
	}

	if len(s.FailCalls) != 0 || len(s.SettleCalls) != 0 {
		t.Fatalf("Fail/Settle calls: got %+v / %+v, want none", s.FailCalls, s.SettleCalls)
	}
}

// TestRunContinuous_AbortAfterOneSettles_OnlyInFlightIssueReclaimed covers
// #3521 item 4: an issue that already finished and settled before the abort
// fired must never be Reclaimed, only the one still genuinely in flight.
func TestRunContinuous_AbortAfterOneSettles_OnlyInFlightIssueReclaimed(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	started1 := make(chan struct{})
	release1 := make(chan struct{})
	started2 := make(chan struct{})
	release2 := make(chan struct{})
	fr := runner.NewFake()
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
	kr := newKillHook(fr)

	dir := tempLogDir(t)
	f := testFactory(t, dir, kr)
	ss := &settleSignal{Fake: settle.NewFake(), settled: make(chan string, 4)}

	abort := make(chan struct{})
	c.Abort = abort
	// Close Abort from c.now instead of from the test goroutine after a timing
	// margin: once armed, the only site left that reaches now() is #1's own
	// completion goroutine (staleDrain.checkpointIfNeeded), which runs under mu
	// immediately above its outstanding--/delete(inflight), so the first
	// observeAbort after it is that goroutine's own drainRefill — with #1
	// provably already out of inflight (#3521).
	armed := make(chan struct{})
	var armOnce sync.Once
	c.now = func() time.Time {
		select {
		case <-armed:
			armOnce.Do(func() { close(abort) })
		default:
		}
		return time.Now()
	}

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, nil, fc, fc, f, ss, fake, fresh)
	}()

	waitOn(t, started1, "issue #1 was never dispatched")
	waitOn(t, started2, "issue #2 was never dispatched")
	close(armed)
	close(release1)
	if got := waitOn(t, ss.settled, "issue #1 never settled"); got != "1" {
		t.Fatalf("settled: got %q, want 1", got)
	}

	if got := waitOn(t, kr.killed, "Kill was never called after abort"); got != "agent-issue-2" {
		t.Fatalf("Kill: got %q, want agent-issue-2", got)
	}
	close(release2)

	select {
	case err := <-resultCh:
		if !errors.Is(err, ErrSignalledStop) {
			t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return")
	}

	if len(kr.Fake.KillCalls) != 1 || kr.Fake.KillCalls[0] != "agent-issue-2" {
		t.Fatalf("KillCalls: got %v, want exactly [agent-issue-2] (issue #1 already settled, never reclaimed)", kr.Fake.KillCalls)
	}
	for _, call := range fc.TransitionStateCalls {
		if call.Num == "1" {
			t.Fatalf("TransitionStateCalls: got a transition of already-settled #1: %+v", call)
		}
	}
	if len(ss.SettleCalls) != 1 || ss.SettleCalls[0].Num != "1" {
		t.Fatalf("SettleCalls: got %+v, want exactly one Settle of #1", ss.SettleCalls)
	}
}

// TestRunContinuous_AbortClosedWithNothingInFlight_ReturnsErrSignalledStop
// covers #3521 item 5: an Abort closed before RunContinuous ever launches
// anything still returns ErrSignalledStop, prints the nothing-in-flight
// line, and claims nothing — mirroring
// TestRunContinuous_StopClosedBeforeCall_EmptyQueue_ReturnsErrSignalledStopNotOpenNoneDispatchable's
// shape for Stop.
func TestRunContinuous_AbortClosedWithNothingInFlight_ReturnsErrSignalledStop(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", State: "OPEN"}) // unlabeled, never dispatchable

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	abort := make(chan struct{})
	close(abort)
	c.Abort = abort

	var err error
	out := captureStdout(t, func() {
		err = RunContinuous(c, nil, fc, fc, nil, nil, fake, fresh)
	})
	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
	}
	if len(fake.ClaimCalls) != 0 {
		t.Fatalf("ClaimCalls: got %v, want none", fake.ClaimCalls)
	}
	if !strings.Contains(out, "==> abort requested; nothing in flight") {
		t.Fatalf("stdout: got %q, want the abort nothing-in-flight line", out)
	}
	if strings.Contains(out, "terminating") {
		t.Fatalf("stdout: got %q, want no terminating line (nothing was in flight)", out)
	}
}

// TestRunContinuous_NonNilAbortNeverClosed_OrdinaryResultUnaffected covers
// #3521's negative case: a non-nil Abort the operator never closes must not
// change RunContinuous's ordinary outcome, and closing it after the call has
// already returned must be harmless.
func TestRunContinuous_NonNilAbortNeverClosed_OrdinaryResultUnaffected(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	abort := make(chan struct{})
	c.Abort = abort

	err := RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
	if err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}
	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1", len(fr.RunCalls))
	}

	close(abort)
}

// TestRunContinuous_StopAndAbortBothClosed_ReturnsErrSignalledStopAndReclaims
// covers #3521 item 7, the real production shape (a second SIGTERM escalates
// a drain already under Stop into an Abort): both channels closed still
// returns ErrSignalledStop and still reclaims the in-flight issue.
func TestRunContinuous_StopAndAbortBothClosed_ReturnsErrSignalledStopAndReclaims(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	started1 := make(chan struct{})
	release1 := make(chan struct{})
	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error {
		close(started1)
		<-release1
		return nil
	}
	kr := newKillHook(fr)

	dir := tempLogDir(t)
	f := testFactory(t, dir, kr)
	s := settle.NewFake()

	stop := make(chan struct{})
	abort := make(chan struct{})
	c.Stop = stop
	c.Abort = abort

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
	}()

	waitOn(t, started1, "issue #1 was never dispatched")
	close(stop)
	close(abort)
	waitOn(t, kr.killed, "Kill was never called after abort")
	close(release1)

	select {
	case err := <-resultCh:
		if !errors.Is(err, ErrSignalledStop) {
			t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return")
	}

	if len(kr.Fake.KillCalls) != 1 || kr.Fake.KillCalls[0] != "agent-issue-1" {
		t.Fatalf("KillCalls: got %v, want exactly [agent-issue-1]", kr.Fake.KillCalls)
	}
}

// transitionGate wraps *forge.Fake so an abort test can hold the first
// InProgress->Dispatchable transition open — the exact window inside
// terminate.Reclaim between the Kill and the issue actually landing back on
// Dispatchable (#3521).
type transitionGate struct {
	*forge.Fake
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *transitionGate) TransitionState(num string, from, to forge.DispatchState) error {
	if from == forge.InProgress && to == forge.Dispatchable {
		g.once.Do(func() {
			close(g.entered)
			<-g.release
		})
	}
	return g.Fake.TransitionState(num, from, to)
}

// TestRunContinuous_AbortReclaimInFlight_HoldsReturnUntilTransitioned pins the
// abort contract's own invariant (#3521): every aborted issue is transitioned
// off InProgress back to Dispatchable, so RunContinuous must not return — and
// let the process exit — while a Reclaim is still between its Kill and that
// transition.
//
// The sequencing makes a *completing Box's* goroutine, not the abort watcher,
// the site that first observes cfg.Abort: c.now is only ever called with mu
// held, so closing Abort from there means the watcher cannot have latched
// first, and the very next mu-held observeAbort is this goroutine's own via
// drainRefill. The watcher is the easy case — its reclaim runs inside the
// goroutine RunContinuous joins on before returning.
func TestRunContinuous_AbortReclaimInFlight_HoldsReturnUntilTransitioned(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	gate := &transitionGate{Fake: fc, entered: make(chan struct{}), release: make(chan struct{})}

	abort := make(chan struct{})
	c.Abort = abort
	armed := make(chan struct{})
	var armOnce sync.Once
	c.now = func() time.Time {
		select {
		case <-armed:
			armOnce.Do(func() { close(abort) })
		default:
		}
		return time.Now()
	}

	started1 := make(chan struct{})
	started2 := make(chan struct{})
	release2 := make(chan struct{})
	fr := runner.NewFake()
	kr := newKillHook(fr)
	fr.RunFunc = func(box runner.Box) error {
		switch box.Issue {
		case "1":
			close(started1)
			// Reclaim's Kill is what ends this Box in production; returning on
			// it is what drops outstanding to 0 while the gated transition is
			// still pending.
			<-kr.killed
		case "2":
			close(started2)
			<-release2
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, kr)
	s := settle.NewFake()

	var err error
	out := captureStdout(t, func() {
		resultCh := make(chan error, 1)
		go func() {
			resultCh <- RunContinuous(c, nil, gate, fc, f, s, fake, fresh)
		}()

		waitOn(t, started1, "issue #1 was never dispatched")
		waitOn(t, started2, "issue #2 was never dispatched")
		close(armed)
		close(release2)

		waitOn(t, gate.entered, "the reclaim never reached its InProgress->Dispatchable transition")
		select {
		case err := <-resultCh:
			t.Fatalf("RunContinuous returned (%v) while #1 was still mid-Reclaim, before it landed back on Dispatchable", err)
		case <-time.After(300 * time.Millisecond):
		}
		close(gate.release)

		select {
		case err = <-resultCh:
		case <-time.After(2 * time.Second):
			t.Fatal("RunContinuous did not return after the transition was released")
		}
	})

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
	}
	assertReclaimedToDispatchable(t, fc, "1")
	if !strings.Contains(out, "==> abort requested; terminating 1 outstanding Box(es)") {
		t.Fatalf("stdout: got %q, want the abort-terminating line for #1 alone", out)
	}
}

// preLaunchGate parks a Box's goroutine in the claim-to-container window
// (#3521): Dispatch.Run calls IsRunning on the way to runOnce, which is the
// last point before the kill latch is checked and a container created, so
// holding there lets a test land an abort strictly inside that window instead
// of racing the goroutine to it.
type preLaunchGate struct {
	*killHook
	name    string
	release chan struct{}
	once    sync.Once
}

func (g *preLaunchGate) IsRunning(name string) bool {
	if name == g.name {
		g.once.Do(func() { <-g.release })
	}
	return g.killHook.IsRunning(name)
}

// TestRunContinuous_AbortBeforeContainerCreated_StopsBoxAndReleasesIssue
// closes the last window the #3521 review named: an issue joins inflight at
// claim time but its container only exists once runner.Run is entered, so an
// abort landing between the two reaps nothing by name. The kill latch is what
// makes it stick — the Box must never launch, and its issue must still come
// back to Dispatchable with RunContinuous returning promptly rather than
// waiting the Box out.
func TestRunContinuous_AbortBeforeContainerCreated_StopsBoxAndReleasesIssue(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fake := NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}}

	abort := make(chan struct{})
	c.Abort = abort
	// fresh runs under mu at the top of each refill, past that refill's own
	// observeAbort, so closing Abort from the launching call puts the abort
	// after #1 joins inflight and before its Box can create anything: the next
	// mu-held observeAbort reclaims an issue whose container does not exist.
	var freshOnce sync.Once
	fresh := func() (bool, bool, string) {
		freshOnce.Do(func() { close(abort) })
		return true, true, "fresh"
	}

	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error {
		t.Errorf("runner.Run was entered for #%s; the abort reclaimed it before its container existed, so it must never launch", box.Issue)
		return nil
	}
	gate := &preLaunchGate{
		killHook: newKillHook(fr),
		name:     dispatch.BoxName("1"),
		release:  make(chan struct{}),
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, gate)
	s := settle.NewFake()

	var err error
	out := captureStdout(t, func() {
		resultCh := make(chan error, 1)
		go func() {
			resultCh <- RunContinuous(c, nil, fc, fc, f, s, fake, fresh)
		}()

		if got := waitOn(t, gate.killed, "Kill was never called after abort"); got != "agent-issue-1" {
			t.Fatalf("Kill: got %q, want agent-issue-1", got)
		}
		close(gate.release)

		select {
		case err = <-resultCh:
		case <-time.After(2 * time.Second):
			t.Fatal("RunContinuous did not return after the reclaimed Box was released")
		}
	})

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %v, want none (the abort landed before any container existed)", fr.RunCalls)
	}
	if len(gate.Fake.KillCalls) != 1 || gate.Fake.KillCalls[0] != "agent-issue-1" {
		t.Fatalf("KillCalls: got %v, want exactly [agent-issue-1]", gate.Fake.KillCalls)
	}
	assertReclaimedToDispatchable(t, fc, "1")
	if len(s.FailCalls) != 0 || len(s.SettleCalls) != 0 {
		t.Fatalf("Fail/Settle calls: got %+v / %+v, want none (abort abandons)", s.FailCalls, s.SettleCalls)
	}
	if !strings.Contains(out, "terminated by operator; abandoning") {
		t.Fatalf("stdout: got %q, want the abandon line from the reclaimed Box's own goroutine", out)
	}
}

// awaitingSettle wraps settle.Fake so a Settle call behaves like a real
// in-flight settle that is genuinely still polling when the abort lands
// (#3523), mirroring how the real settle.Settle's watch.poll loop rechecks
// s.terminated() at every checkpoint. The poll (see Settle below) never
// sleeps, so there is no timer for a test to hide timing assumptions behind.
// release lets a test end a Settle call the other way, covering the
// graceful-drain case where nothing interrupts it.
type awaitingSettle struct {
	*settle.Fake
	reg *terminate.Registry

	started chan string
	release chan struct{}

	mu      sync.Mutex
	outcome map[string]string
}

func newAwaitingSettle(reg *terminate.Registry) *awaitingSettle {
	return &awaitingSettle{
		Fake:    settle.NewFake(),
		reg:     reg,
		started: make(chan string, 4),
		release: make(chan struct{}),
		outcome: map[string]string{},
	}
}

// awaitingSettleSpinDeadline bounds the busy-spin in Settle below well past
// every caller's own 2s waitOn timeout, so a regression that never marks the
// registry or closes release stops this goroutine instead of pinning a core
// for the rest of the package run.
const awaitingSettleSpinDeadline = 10 * time.Second

// Settle spins on reg.Marked via runtime.Gosched rather than a channel
// receive because terminate.Registry exposes no notification seam
// (Begin/Mark/Marked only), and adding one would be production code grown
// solely for this test's need.
func (a *awaitingSettle) Settle(d dispatch.Dispatcher, num string, gen uint64, result dispatch.Result) {
	a.started <- num
	deadline := time.Now().Add(awaitingSettleSpinDeadline)
	for {
		if a.reg.Marked(num, gen) {
			a.recordOutcome(num, "marked")
			a.Fake.Settle(d, num, gen, result)
			return
		}
		select {
		case <-a.release:
			a.recordOutcome(num, "released")
			a.Fake.Settle(d, num, gen, result)
			return
		default:
			if time.Now().After(deadline) {
				a.recordOutcome(num, "spin-deadline-exceeded")
				return
			}
			runtime.Gosched()
		}
	}
}

func (a *awaitingSettle) recordOutcome(num, why string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.outcome[num] = why
}

func (a *awaitingSettle) outcomeFor(num string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.outcome[num]
}

// setupAbortDrainSettleTest builds the shared fixture for the abort and
// drain settle tests below: a single dispatchable issue #1, a Box that
// finishes immediately (so it is Settle, not the Box, still in flight when
// termination lands), and an awaitingSettle wired to a fresh registry.
func setupAbortDrainSettleTest(t *testing.T) (c Config, session *Session, fc *forge.Fake, f *dispatch.Factory, as *awaitingSettle, fake *FakeQueue, fresh func() (bool, bool, string)) {
	c = baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1

	fc = forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fake = NewFakeQueue()
	fake.DiscoverReturn = Batch{Issues: []Issue{{Number: "1"}}}
	fresh = func() (bool, bool, string) { return true, true, "fresh" }

	// RunFunc unset: the Box finishes immediately, so it is Settle, not the
	// Box, that is still running when termination lands.
	fr := runner.NewFake()
	dir := tempLogDir(t)
	f = testFactory(t, dir, fr)

	reg := terminate.NewRegistry()
	as = newAwaitingSettle(reg)
	session = &Session{Terminated: reg}
	return c, session, fc, f, as, fake, fresh
}

// TestRunContinuous_AbortWhileSettleInFlight_StopsPromptlyAndReleasesIssue
// covers #3523's end-to-end case none of #3521's own abort tests exercise:
// the Box has already finished and it is the *settle*, not the Box, still in
// flight when Abort closes. continuous.go calls s.Settle before
// delete(inflight, num), so a still-polling settle's issue is still in
// reclaimInFlight's snapshot — this pins that the reclaim's mark is what lets
// the polling settle stop, not some unrelated timeout or race.
func TestRunContinuous_AbortWhileSettleInFlight_StopsPromptlyAndReleasesIssue(t *testing.T) {
	c, session, fc, f, as, fake, fresh := setupAbortDrainSettleTest(t)

	abort := make(chan struct{})
	c.Abort = abort

	var err error
	out := captureStdout(t, func() {
		resultCh := make(chan error, 1)
		go func() {
			resultCh <- RunContinuous(c, session, fc, fc, f, as, fake, fresh)
		}()

		waitOn(t, as.started, "settle was never invoked for issue #1")
		close(abort)

		err = waitOn(t, resultCh, "RunContinuous did not return")
	})

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
	}
	if got := as.outcomeFor("1"); got != "marked" {
		t.Fatalf("settle outcome: got %q, want %q (it must stop because it observed the abort's mark, not for any other reason)", got, "marked")
	}
	assertReclaimedToDispatchable(t, fc, "1")
	iss, _ := fc.Issue("1")
	if containsLabel(iss.Labels, c.CompleteLabel) {
		t.Fatalf("labels: got %v, want no %s label (settle was abandoned, not completed)", iss.Labels, c.CompleteLabel)
	}
	if containsLabel(iss.Labels, c.FailedLabel) {
		t.Fatalf("labels: got %v, want no %s label (abort never fails an issue)", iss.Labels, c.FailedLabel)
	}
	if !strings.Contains(out, "==> abort requested; terminating 1 outstanding Box(es)") {
		t.Fatalf("stdout: got %q, want the abort-terminating line", out)
	}
}

// TestRunContinuous_DrainDuringSettle_NotInterruptedRunsToCompletion is the
// negative case #3523 pins alongside the abort test above: a graceful drain
// (cfg.Stop alone, never cfg.Abort) must let an in-flight settle finish on
// its own, never cutting it short the way an abort does. If a future change
// ever made a drain interrupt a settle, this fails.
func TestRunContinuous_DrainDuringSettle_NotInterruptedRunsToCompletion(t *testing.T) {
	c, session, fc, f, as, fake, fresh := setupAbortDrainSettleTest(t)

	stop := make(chan struct{})
	c.Stop = stop

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- RunContinuous(c, session, fc, fc, f, as, fake, fresh)
	}()

	waitOn(t, as.started, "settle was never invoked for issue #1")
	close(stop)

	// RunContinuous joins on outstanding dropping to 0 before it can return
	// (continuous.go's `for outstanding > 0 { idle.Wait() }`), and outstanding
	// only drops once Settle returns, so it still being outstanding here,
	// well after Stop closed, is itself proof the drain did not cut the
	// settle short.
	select {
	case got := <-resultCh:
		t.Fatalf("RunContinuous returned (%v) while the settle was still in flight after Stop alone", got)
	case <-time.After(100 * time.Millisecond):
	}

	close(as.release)

	err := waitOn(t, resultCh, "RunContinuous did not return after the settle was released")

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("RunContinuous: got %v, want ErrSignalledStop", err)
	}
	if got := as.outcomeFor("1"); got != "released" {
		t.Fatalf("settle outcome: got %q, want %q (a graceful drain must never mark the registry)", got, "released")
	}
	for _, call := range fc.TransitionStateCalls {
		if call.Num == "1" && call.To == forge.Dispatchable {
			t.Fatalf("TransitionStateCalls: got a Dispatchable transition of #1 under a plain drain: %+v", call)
		}
	}
}
