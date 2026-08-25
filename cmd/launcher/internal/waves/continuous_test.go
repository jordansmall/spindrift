package waves

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
	"spindrift.dev/launcher/internal/testutil"
)

// TestRunContinuous_RefillsFreedSlotWhileOthersRunning verifies the core
// slot-refill behavior (#527 AC1): with MaxParallel=2 and three ready
// issues, the third issue launches into the slot #1 frees while #2 is still
// running — a batch-shaped implementation would deadlock here, since #2
// only unblocks after #3 has already started.
func TestRunContinuous_RefillsFreedSlotWhileOthersRunning(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.Label}})

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
	s := newSettle(fc, fc)

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, nil, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	resultCh := make(chan error, 1)
	go func() { resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh) }()

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
}

// TestRunContinuous_RefillPicksUpIssueUnblockedMidRun verifies #527 AC2: a
// blocked issue's blocker resolving mid-run (merged/closed after dispatch
// started) makes it dispatchable on the very next refill, without a fresh
// invocation.
func TestRunContinuous_RefillPicksUpIssueUnblockedMidRun(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
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
	s := newSettle(fc, fc)

	edges := map[string][]string{"2": {"3"}}
	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, edges, nil, nil, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	resultCh := make(chan error, 1)
	go func() { resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh) }()

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
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	limiter := NewLimiter(1)
	session := &Session{Limiter: limiter}

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})

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
	s := newSettle(fc, fc)

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, nil, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	resultCh := make(chan error, 1)
	go func() { resultCh <- RunContinuous(c, session, fc, fc, dir, f, s, discover, fresh) }()

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
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	limiter := NewLimiter(1)
	session := &Session{Limiter: limiter}

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.Label}})

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
	s := newSettle(fc, fc)

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, nil, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	resultCh := make(chan error, 1)
	go func() { resultCh <- RunContinuous(c, session, fc, fc, dir, f, s, discover, fresh) }()

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
	c.Label = "agent-trigger"
	c.MaxParallel = 2
	limiter := NewLimiter(2)
	session := &Session{Limiter: limiter}

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.Label}})

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
	s := newSettle(fc, fc)

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, nil, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	resultCh := make(chan error, 1)
	go func() { resultCh <- RunContinuous(c, session, fc, fc, dir, f, s, discover, fresh) }()

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
	c.Label = "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})

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
	s := newSettle(fc, fc)

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, nil, nil
	}

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
	go func() { resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh) }()

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
	c.Label = "agent-trigger"
	c.MaxParallel = 2

	// Deterministic clock (issue #2678 mutation-testing gap: replacing the
	// freeSlotSecs accumulation at continuous.go with a literal 0 left the
	// whole waves suite green, since the two assertions below only checked
	// >=0). The mu.Lock()/idle.Wait() pairing in RunContinuous's bootstrap
	// section guarantees this scenario reads the clock exactly twice, in
	// order: once to set drainStart when the stale verdict fires (still
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

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})

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
	s := newSettle(fc, fc)

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, nil, nil
	}

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
	var err error
	stdout := testutil.CaptureStdout(t, func() {
		go func() { resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh) }()
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
	if !strings.Contains(stdout, "1 issue(s) held back") {
		t.Fatalf("stdout: got %q, want a drain report line mentioning 1 issue(s) held back", stdout)
	}
	if !strings.Contains(stdout, "==> drain: ") {
		t.Fatalf("stdout: got %q, want a drain report line", stdout)
	}

	logPath := filepath.Join(dispatch.HostLogDirFor(dir), drainMarker)
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	log := string(logBytes)
	drainLines := strings.Count(log, "DRAIN ")
	if drainLines != 1 {
		t.Fatalf("drain.log: got %d DRAIN line(s), want exactly 1: %q", drainLines, log)
	}
	if !strings.Contains(log, "heldBack=1") {
		t.Fatalf("drain.log: got %q, want heldBack=1", log)
	}

	dur := parseDrainField(t, log, "durationSeconds=")
	// The clock advances by exactly one tick between the two reads
	// (drainStart, then the completion checkpoint that becomes drainEnd),
	// so Duration() == tick exactly.
	wantDur := tick.Seconds()
	if dur != wantDur {
		t.Fatalf("durationSeconds: got %v, want exactly %v (base+%v clock, two reads)", dur, wantDur, tick)
	}

	free := parseDrainField(t, log, "freeSlotSeconds=")
	// freeSlotSecs accumulates (limiter.Cap()-outstanding)*elapsed across
	// the single interval between the two clock reads: Cap()=2,
	// outstanding=1 (box #1 still counted before its own decrement) over
	// the one tick between drainStart and the completion checkpoint, so
	// the exact expected value is 1*tick, not merely >=0 -- reverting the
	// real accumulation to a literal 0 (or any other wrong formula) must
	// fail this assertion.
	wantFree := float64(2-1) * tick.Seconds()
	if free != wantFree {
		t.Fatalf("freeSlotSeconds: got %v, want exactly %v ((cap-outstanding)*tick = (2-1)*%v)", free, wantFree, tick)
	}

	if clockCalls != 2 {
		t.Fatalf("clock reads: got %d, want exactly 2 (drainStart + completion checkpoint) -- test assumptions about the deterministic sequence no longer hold", clockCalls)
	}
}

// TestRunContinuous_StaleDrainDiscoverErrorReportsHeldBackUnknown verifies a
// review finding on #2678: when the stale-transition branch's
// reporting-only discover() call fails (a transient tracker hiccup), the
// emitted DrainReport must say the held-back count is unknown, not silently
// fabricate a confirmed-looking zero. The discover fake distinguishes the
// normal bootstrap discover call (which launches #1 and must succeed, or
// nothing would ever get in flight to exercise the stale path) from the
// stale-transition's own re-discover call (which fails here) by call count.
func TestRunContinuous_StaleDrainDiscoverErrorReportsHeldBackUnknown(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})

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
	s := newSettle(fc, fc)

	errDiscover := errors.New("tracker rate limited")
	var discoverMu sync.Mutex
	discoverCalls := 0
	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		discoverMu.Lock()
		discoverCalls++
		n := discoverCalls
		discoverMu.Unlock()
		if n > 1 {
			// The stale-transition's reporting-only re-discover call: a
			// transient tracker hiccup, distinct from the bootstrap call
			// (n == 1) that must succeed to get #1 in flight at all.
			return nil, nil, nil, nil, errDiscover
		}
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, nil, nil
	}

	// Fresh for the first refill (fills #1's slot), stale for every refill
	// after -- including the second initial slot -- so the stale-transition
	// branch's re-discover call (n == 2) is the one that fails.
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
	stdout := testutil.CaptureStdout(t, func() {
		go func() { resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh) }()
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
	if !strings.Contains(stdout, "held back: unknown") {
		t.Fatalf("stdout: got %q, want a drain report line reporting held-back as unknown", stdout)
	}
	if strings.Contains(stdout, "0 issue(s) held back") {
		t.Fatalf("stdout: got %q, must not fabricate a confirmed-looking 0 issue(s) held back after a discover error", stdout)
	}

	logPath := filepath.Join(dispatch.HostLogDirFor(dir), drainMarker)
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	log := string(logBytes)
	drainLines := strings.Count(log, "DRAIN ")
	if drainLines != 1 {
		t.Fatalf("drain.log: got %d DRAIN line(s), want exactly 1: %q", drainLines, log)
	}
	if !strings.Contains(log, "heldBack=unknown") {
		t.Fatalf("drain.log: got %q, want heldBack=unknown", log)
	}
	if strings.Contains(log, "heldBack=0") {
		t.Fatalf("drain.log: got %q, must not fabricate heldBack=0 after a discover error", log)
	}
}

// TestRunContinuous_StaleDrainResizeBelowOutstandingClampsFreeSlotSecs
// verifies a review finding on #2678: the completion goroutine's
// freeSlotSecs accumulation multiplies the elapsed interval by
// drainCap-outstanding, with no floor. ResizeDelta (limiter.go) never
// revokes a slot already claimed/outstanding, so an operator lowering the
// live cap mid-drain below the outstanding count makes that term negative,
// corrupting the running total with a negative contribution instead of
// crediting zero free slots for an interval that had none.
//
// A second review finding on #2678 fixed drainCap itself: it now tracks the
// cap actually in effect since the last checkpoint (frozen at drainStart,
// refreshed only after each checkpoint closes out the interval that just
// ended) rather than reading limiter.Cap() live at checkpoint time -- see
// TestRunContinuous_StaleDrainResizeUpCheckpointsBeforeCapChange for that
// half of the fix. A THIRD review finding fixed the resize listener itself:
// it now wakes on Limiter.Resized() (fires on either direction), not just
// Grown() (raise-only) -- ResizeDelta's lower here checkpoints immediately,
// exactly like a raise does, instead of leaving drainCap frozen until
// whichever Box happens to complete next. This scenario now proves all
// three fixes compose: with 3 Boxes launched against an initial cap of 4,
// then resized straight down to the Limiter's floor of 1 while all 3 are
// still outstanding, the resize listener's own checkpoint is the FIRST of
// the four checkpoints this drain sees (drainStart, then one checkpoint
// each for the resize and the three completions -- five now() calls total,
// not four) -- and one of the three completion checkpoints after it must
// still be clamped, because drainCap can fall below outstanding even after
// the refresh.
//
// The initial cap is 4, not 3, because the staleness probe that trips a
// drain can only succeed while a slot is still free (Limiter.TryAcquire
// requires cap > live) -- tripping it with all 3 Boxes already outstanding
// forces the frozen drainCap at drainStart to be at least outstanding+1,
// never outstanding itself. That's an inherent floor of the "extra
// TryAcquire probe" mechanism RunContinuous uses to detect staleness, not a
// choice made for this test.
//
// The deterministic clock's `now` closure is the synchronization point,
// exactly as in TestRunContinuous_StaleDrainResizeDownAboveOutstandingCheckpointsBeforeCapChange
// below: it signals sawResizeCheckpoint the moment its SECOND call happens,
// which by construction can only be the resize listener's own
// checkpointDrain (nothing else calls now() between drainStart and the
// test's own ResizeDelta) -- and because that call happens while the
// resize listener still holds the shared mutex the completion goroutines
// also need, waiting for the signal before releasing any of the three
// Boxes guarantees the resize listener's checkpoint (and its drainCap
// refresh) has already run to completion before any completion goroutine's
// own checkpoint can start. Without that barrier, the resize listener's
// checkpoint is only taken at all if drainInProgress() is still true when
// it acquires mu (continuous.go's `case <-limiter.Resized():` branch) --
// if all three completions raced ahead and drained outstanding to 0
// first, the checkpoint would be skipped outright, so the barrier is load
// bearing, not merely a nicety.
//
// With that ordering pinned, the math comes out as follows: only the FIRST
// of the four checkpoints -- now guaranteed to be the resize listener's,
// not a completion's -- ever sees the still-frozen drainCap=4 and
// outstanding=3 (the count before any completion has decremented it) --
// (4-3)*tick = one tick, correctly positive, no clamp needed -- because
// that first checkpoint is also what refreshes drainCap to the
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
	c.Label = "agent-trigger"
	c.MaxParallel = 4
	limiter := NewLimiter(4)
	session := &Session{Limiter: limiter}

	// Deterministic clock (same pattern as
	// TestRunContinuous_StaleDrainWithInFlightBoxReportsHeldBack): this
	// scenario reads the clock exactly five times -- once to set drainStart
	// when the stale verdict fires (bootstrap, still holding mu), once for
	// the resize listener's own checkpoint (triggered by the ResizeDelta
	// below), then once per completion checkpoint as box #1, #2, and #3
	// each settle. sawResizeCheckpoint closes the moment the SECOND now()
	// call happens. Between drainStart (the first call) and the test's own
	// limiter.ResizeDelta below, nothing else calls now() -- so the second
	// call can only be the resize listener's checkpointDrain, and it fires
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

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.Label}})

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
	s := newSettle(fc, fc)

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, nil, nil
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
	stdout := testutil.CaptureStdout(t, func() {
		go func() { resultCh <- RunContinuous(c, session, fc, fc, dir, f, s, discover, fresh) }()

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
	})

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}
	if !strings.Contains(stdout, "==> drain: ") {
		t.Fatalf("stdout: got %q, want a drain report line", stdout)
	}

	logPath := filepath.Join(dispatch.HostLogDirFor(dir), drainMarker)
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	log := string(logBytes)
	drainLines := strings.Count(log, "DRAIN ")
	if drainLines != 1 {
		t.Fatalf("drain.log: got %d DRAIN line(s), want exactly 1: %q", drainLines, log)
	}

	free := parseDrainField(t, log, "freeSlotSeconds=")
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
		t.Fatalf("clock reads: got %d, want exactly 5 (drainStart + the resize listener's own checkpoint + three completion checkpoints) -- test assumptions about the deterministic sequence no longer hold", clockCalls)
	}
}

// TestRunContinuous_StaleDrainResizeUpCheckpointsBeforeCapChange verifies
// the other half of the #2678 review finding fixed above: freeSlotSecs must
// never read limiter.Cap() live at checkpoint time and apply it
// retroactively to the whole interval that just ended. A Console operator
// can raise the live cap mid-drain via ResizeDelta (ADR 0023) at any
// moment; RunContinuous's grow listener (the `case <-limiter.Grown():`
// branch) must checkpoint the interval that just ended -- at the OLD cap --
// before it ever lets drainCap see the raised value, so the raise is only
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
// grow listener's checkpointDrain (nothing else calls now() between
// drainStart and the resize) -- and because that call happens while the
// grow listener still holds the shared mutex the completion goroutine also
// needs, waiting for the signal before releasing the Box guarantees the
// grow listener's checkpoint (and its drainCap refresh) has already run to
// completion before the Box's own completion checkpoint can start, with no
// sleep or poll required.
//
// Interval 1 (drainStart -> the grow listener's checkpoint, triggered by
// ResizeDelta itself, not a Box completion): drainCap is still the OLD cap,
// 2 (frozen since drainStart), outstanding=1 -- (2-1)*tick = one tick.
// Interval 2 (that checkpoint -> the Box's own completion): drainCap has
// refreshed to the NEW cap, 10, outstanding=1 -- (10-1)*tick = nine ticks.
// Total: ten ticks (50s) -- neither the ~90s crediting the whole two-tick
// drain at the raised cap 10 (the pre-fix bug this pins: reading
// limiter.Cap() live at the single completion checkpoint would apply 10 to
// the entire interval since drainStart) nor the ~10s crediting it all at
// the original cap 2.
func TestRunContinuous_StaleDrainResizeUpCheckpointsBeforeCapChange(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2
	limiter := NewLimiter(2)
	session := &Session{Limiter: limiter}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	const tick = 5 * time.Second
	var clockMu sync.Mutex
	clockCalls := 0
	// sawGrowCheckpoint closes the moment the SECOND now() call happens.
	// Between drainStart (the first call) and the test's own
	// limiter.ResizeDelta below, nothing else calls now() -- so the second
	// call can only be the grow listener's checkpointDrain, and it fires
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

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})

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
	s := newSettle(fc, fc)

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, nil, nil
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
	stdout := testutil.CaptureStdout(t, func() {
		go func() { resultCh <- RunContinuous(c, session, fc, fc, dir, f, s, discover, fresh) }()

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
	})

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}
	if !strings.Contains(stdout, "==> drain: ") {
		t.Fatalf("stdout: got %q, want a drain report line", stdout)
	}

	logPath := filepath.Join(dispatch.HostLogDirFor(dir), drainMarker)
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	log := string(logBytes)
	drainLines := strings.Count(log, "DRAIN ")
	if drainLines != 1 {
		t.Fatalf("drain.log: got %d DRAIN line(s), want exactly 1: %q", drainLines, log)
	}

	dur := parseDrainField(t, log, "durationSeconds=")
	wantDur := 2 * tick.Seconds()
	if dur != wantDur {
		t.Fatalf("durationSeconds: got %v, want exactly %v (drainStart + two checkpoints, one tick apart each)", dur, wantDur)
	}

	free := parseDrainField(t, log, "freeSlotSeconds=")
	// See the function doc comment above for the interval-by-interval
	// derivation: (2-1)*tick + (10-1)*tick == one tick + nine ticks == ten
	// ticks (50s).
	wantFree := (1 + 9) * tick.Seconds()
	if free != wantFree {
		t.Fatalf("freeSlotSeconds: got %v, want exactly %v (old cap credited before the raise, new cap only after it)", free, wantFree)
	}

	if clockCalls != 3 {
		t.Fatalf("clock reads: got %d, want exactly 3 (drainStart + grow checkpoint + completion checkpoint) -- test assumptions about the deterministic sequence no longer hold", clockCalls)
	}
}

// TestRunContinuous_StaleDrainResizeDownAboveOutstandingCheckpointsBeforeCapChange
// verifies a review finding on #2678: the fix that made the resize listener
// checkpoint on a raise (TestRunContinuous_StaleDrainResizeUpCheckpointsBeforeCapChange
// above) left the mirror-image case unfixed. RunContinuous's resize listener
// used to wake only on Limiter.Grown(), which never signals for a lower
// (limiter.go's signalGrow returns early when the resize didn't grow the
// cap) -- so a mid-drain lower sat unnoticed until whichever Box happened to
// complete next, and that completion's checkpoint credited the ENTIRE
// interval since the last checkpoint at the stale, pre-lower drainCap,
// over-crediting every second between the lower and that completion as if
// the higher cap had still been in effect. TestRunContinuous_
// StaleDrainResizeBelowOutstandingClampsFreeSlotSecs above never caught this
// because it lowers the cap to the Limiter's floor of 1, below the 3
// outstanding Boxes -- the clamp erases the over-credit along with
// everything else. This scenario instead lowers a cap of 6 to 3 with only 2
// Boxes outstanding (the review finding's own repro numbers), so the
// lowered cap stays ABOVE outstanding, the clamp never engages, and an
// over-credit would show up directly in the asserted total.
//
// Both Boxes are held outstanding at cap 6 (the review finding's own
// numbers), then a Console-style ResizeDelta(-3) lowers the cap to 3 while
// both are still running -- 3 stays above the 2 outstanding Boxes, so
// nothing here is clamped. The deterministic clock's `now` closure is the
// synchronization point, exactly as in the Up-checkpoints test above: it
// signals sawResizeCheckpoint the moment its SECOND call happens, which by
// construction can only be the resize listener's checkpointDrain (nothing
// else calls now() between drainStart and the test's own ResizeDelta) --
// and because that call happens while the resize listener still holds the
// shared mutex the completion goroutines also need, waiting for the signal
// before releasing either Box guarantees the resize listener's checkpoint
// (and its drainCap refresh) has already run to completion before either
// Box's own completion checkpoint can start.
//
// Interval 1 (drainStart -> the resize listener's checkpoint, triggered by
// ResizeDelta itself, not a Box completion): drainCap is still the OLD cap,
// 6 (frozen since drainStart), outstanding=2 (both Boxes still running) --
// (6-2)*tick = four ticks. Interval 2 (that checkpoint -> the first Box to
// complete): drainCap has refreshed to the NEW cap, 3, outstanding=2
// (pre-decrement, neither Box has completed yet) -- (3-2)*tick = one tick.
// Interval 3 (-> the second Box's completion): drainCap=3, outstanding=1
// (pre-decrement, the one remaining Box) -- (3-1)*tick = two ticks. Total:
// seven ticks (35s) -- not the ~45s (nine ticks: (6-2)+(6-1)... crediting
// the whole pre-completion span at the stale cap 6) the pre-fix bug this
// pins would produce by leaving drainCap frozen at 6 until a Box completion
// happened to refresh it.
//
// Which of the two Boxes completes first is never pinned down -- both are
// symmetric, so the math is identical either way: the first completion
// checkpoint always sees outstanding=2 (pre-decrement, neither has
// completed yet) and the second always sees outstanding=1, regardless of
// Box identity.
func TestRunContinuous_StaleDrainResizeDownAboveOutstandingCheckpointsBeforeCapChange(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 6
	limiter := NewLimiter(6)
	session := &Session{Limiter: limiter}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	const tick = 5 * time.Second
	var clockMu sync.Mutex
	clockCalls := 0
	// sawResizeCheckpoint closes the moment the SECOND now() call happens.
	// Between drainStart (the first call) and the test's own
	// limiter.ResizeDelta below, nothing else calls now() -- so the second
	// call can only be the resize listener's checkpointDrain, and it fires
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

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})

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
	s := newSettle(fc, fc)

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, nil, nil
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
	stdout := testutil.CaptureStdout(t, func() {
		go func() { resultCh <- RunContinuous(c, session, fc, fc, dir, f, s, discover, fresh) }()

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
	})

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}
	if !strings.Contains(stdout, "==> drain: ") {
		t.Fatalf("stdout: got %q, want a drain report line", stdout)
	}

	logPath := filepath.Join(dispatch.HostLogDirFor(dir), drainMarker)
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	log := string(logBytes)
	drainLines := strings.Count(log, "DRAIN ")
	if drainLines != 1 {
		t.Fatalf("drain.log: got %d DRAIN line(s), want exactly 1: %q", drainLines, log)
	}

	dur := parseDrainField(t, log, "durationSeconds=")
	wantDur := 3 * tick.Seconds()
	if dur != wantDur {
		t.Fatalf("durationSeconds: got %v, want exactly %v (drainStart + three checkpoints, one tick apart each)", dur, wantDur)
	}

	free := parseDrainField(t, log, "freeSlotSeconds=")
	// See the function doc comment above for the interval-by-interval
	// derivation: (6-2)*tick + (3-2)*tick + (3-1)*tick == four ticks + one
	// tick + two ticks == seven ticks (35s).
	wantFree := (4 + 1 + 2) * tick.Seconds()
	if free != wantFree {
		t.Fatalf("freeSlotSeconds: got %v, want exactly %v (old cap credited before the lower, new cap only after it -- an over-credited total here would mean the resize listener never checkpointed the lower)", free, wantFree)
	}

	if clockCalls != 4 {
		t.Fatalf("clock reads: got %d, want exactly 4 (drainStart + resize checkpoint + two completion checkpoints) -- test assumptions about the deterministic sequence no longer hold", clockCalls)
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
	c.Label = "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", State: "OPEN"}) // blocker, not complete

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	edges := map[string][]string{"1": {"2"}}
	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, edges, nil, nil, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	err := RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh)
	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("RunContinuous: got %v, want ErrOpenNoneDispatchable", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0", len(fr.RunCalls))
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
	c.Label = "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Body: "blocked by #3", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"}) // #2's blocker, unmet

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	var gotSources Sources
	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		result, err := NewReadiness(fc, out)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		gotSources = result.Sources
		return out, result.Edges, result.Sources, result.Failed, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh); err != nil {
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
	if !containsLabel(iss2.Labels, c.Label) {
		t.Errorf("issue 2 must remain %q (held, not cascaded) — sources threading must not change selection; labels=%v", c.Label, iss2.Labels)
	}
}

// TestRunContinuous_RefillCycleGuardSkipsAndReports verifies #571: a refill
// whose re-discovery returns an edge set with a cycle among in-batch issues
// must not launch a Box for any of them, must surface the offending issue
// number, and must return through RunContinuous's normal completion path
// rather than hanging.
func TestRunContinuous_RefillCycleGuardSkipsAndReports(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.Label}})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	// Cyclic dependency among all three in-batch issues: 1 -> 2 -> 3 -> 1.
	edges := map[string][]string{
		"1": {"2"},
		"2": {"3"},
		"3": {"1"},
	}
	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, edges, nil, nil, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	var err error
	resultCh := make(chan error, 1)
	errOut := testutil.CaptureStderr(t, func() {
		resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh)
	})

	select {
	case err = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RunContinuous did not return — cycle guard may have hung a refill")
	}

	if !errors.Is(err, ErrOpenNoneDispatchable) {
		t.Fatalf("RunContinuous: got %v, want ErrOpenNoneDispatchable (no issue in the cycle is ever dispatchable)", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %d, want 0 (no Box may launch for a cyclic batch)", len(fr.RunCalls))
	}
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
	c.Label = "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})

	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error { return nil }

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	// Always reports #1 as dispatchable, regardless of the claim already
	// made against it — a stale search result, not a live forge query.
	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		return []Issue{{Number: "1", Title: "stale"}}, map[string][]string{}, nil, nil, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	var err error
	out := testutil.CaptureStdout(t, func() {
		err = RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh)
	})
	if err != nil {
		t.Fatalf("RunContinuous: got %v, want nil", err)
	}

	if len(fr.RunCalls) != 1 {
		t.Fatalf("RunCalls: got %d, want 1 (stale re-discovery of #1 must not double-dispatch)", len(fr.RunCalls))
	}
	// Two transitions are expected from the single live dispatch: the claim
	// (Dispatchable -> InProgress) and settle's demotion of the box's
	// outcome-less, PR-less run (InProgress -> Failed, issue #1605). A third
	// would mean the suppressed stale re-discovery re-attempted the claim.
	if len(fc.TransitionStateCalls) != 2 {
		t.Fatalf("TransitionStateCalls: got %d, want 2 (suppressed stale entry must not re-attempt the claim)", len(fc.TransitionStateCalls))
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
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	reg := terminate.NewRegistry()
	reg.Mark("1")
	session := &Session{Terminated: reg}

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})

	fr := runner.NewFake()
	fr.RunErr = boxErr

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	fakeSettle := settle.NewFake()

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, nil, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, session, fc, fc, dir, f, fakeSettle, discover, fresh); err != nil {
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
	c.Label = "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})

	fr := runner.NewFake()
	fr.RunErr = boxErr

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	fakeSettle := settle.NewFake()

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, nil, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, nil, fc, fc, dir, f, fakeSettle, discover, fresh); err != nil {
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
	c.Label = "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	failed := map[string]bool{"1": true}
	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, failed, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh); err != nil {
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
	c.Label = "agent-trigger"
	c.MaxParallel = 3

	fc := forge.NewFake(dispatchLabels(c))
	for _, n := range []string{"1", "2", "3", "4", "5"} {
		fc.SetIssue(forge.Issue{Number: n, Labels: []string{c.Label}})
	}

	var visMu sync.Mutex
	visible := []string{"1", "2", "3"}
	calls := make(chan struct{}, 100)
	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		visMu.Lock()
		nums := append([]string(nil), visible...)
		visMu.Unlock()
		out := make([]Issue, len(nums))
		for i, n := range nums {
			out[i] = Issue{Number: n, Title: "issue " + n}
		}
		calls <- struct{}{}
		return out, map[string][]string{}, nil, nil, nil
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
	s := newSettle(fc, fc)

	resultCh := make(chan error, 1)
	go func() { resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh) }()

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
	c.Label = "agent-trigger"
	c.MaxParallel = 2
	c.pollInterval = 10 * time.Millisecond

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})

	var visMu sync.Mutex
	visible := []string{"1"}
	calls := make(chan struct{}, 100)
	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		visMu.Lock()
		nums := append([]string(nil), visible...)
		visMu.Unlock()
		out := make([]Issue, len(nums))
		for i, n := range nums {
			out[i] = Issue{Number: n, Title: "issue " + n}
		}
		calls <- struct{}{}
		return out, map[string][]string{}, nil, nil, nil
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
	s := newSettle(fc, fc)

	resultCh := make(chan error, 1)
	go func() { resultCh <- RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh) }()

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
	c.Label = "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})                            // Normal (unlabeled)
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label, "agent-priority-low"}})      // Low
	fc.SetIssue(forge.Issue{Number: "3", Labels: []string{c.Label, "agent-priority-critical"}}) // Critical
	fc.SetIssue(forge.Issue{Number: "4", Labels: []string{c.Label, "agent-priority-high"}})     // High
	fc.SetIssue(forge.Issue{Number: "5", Labels: []string{c.Label, "agent-priority-critical"}}) // Critical, tiebreaks after #3

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title, Priority: fi.Priority}
		}
		return out, map[string][]string{}, nil, nil, nil
	}
	fresh := func() (bool, bool, string) { return true, true, "fresh" }

	if err := RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh); err != nil {
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
	c.Label = "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, map[string][]string{}, nil, nil, nil
	}
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	var err error
	stdout := testutil.CaptureStdout(t, func() {
		err = RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh)
	})

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %v, want none (stale fired before any launch)", fr.RunCalls)
	}
	if !strings.Contains(stdout, "==> drain: 0s idle, 0.0 free-slot-s, 1 issue(s) held back\n") {
		t.Fatalf("stdout: got %q, want a zero-duration drain report line", stdout)
	}

	logPath := filepath.Join(dispatch.HostLogDirFor(dir), drainMarker)
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "DRAIN ") || !strings.Contains(log, "durationSeconds=0.000") || !strings.Contains(log, "freeSlotSeconds=0.000") || !strings.Contains(log, "heldBack=1") {
		t.Fatalf("drain.log: got %q, want a DRAIN line with durationSeconds=0.000, freeSlotSeconds=0.000, heldBack=1", log)
	}
}

// TestRunContinuous_StaleDrainPendingCountOverridesPreResolvedFallback
// verifies a review finding on #2678: a PreResolved caller (Console) cannot
// safely call discover() a second time just to count heldBack, since
// Queue.Discover's claim is an inseparable side effect of discovering a
// ready pick (queue.go) -- doing so would orphan a real issue at InProgress
// with nothing to dispatch it. cfg.PendingCount is the pure alternative such
// a caller supplies instead; when both PreResolved and PendingCount are set,
// the reported heldBack must come from PendingCount(), not from the
// PreResolved fallback (which leaves heldBack at its zero value) and not
// from calling discover() again. discover here always reports zero
// candidates, so a heldBack of 0 would be indistinguishable from "the
// PreResolved fallback fired" or "discover() was consulted and found
// nothing" -- only a non-zero PendingCount()-sourced value proves the new
// switch branch actually took priority.
func TestRunContinuous_StaleDrainPendingCountOverridesPreResolvedFallback(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	c.PreResolved = true
	c.PendingCount = func() int { return 3 }

	fc := forge.NewFake(dispatchLabels(c))

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		// PreResolved's own discover (a stand-in for Console's
		// Queue.Discover) never returns a candidate to count here -- a real
		// Queue.Discover call would instead claim any ready pick, which is
		// exactly the side effect cfg.PendingCount exists to avoid needing.
		return nil, nil, nil, nil, nil
	}
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	var err error
	stdout := testutil.CaptureStdout(t, func() {
		err = RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh)
	})

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %v, want none (stale fired before any launch)", fr.RunCalls)
	}
	if !strings.Contains(stdout, "3 issue(s) held back") {
		t.Fatalf("stdout: got %q, want a drain report line mentioning 3 issue(s) held back (from cfg.PendingCount, not discover() or the PreResolved zero-value fallback)", stdout)
	}

	logPath := filepath.Join(dispatch.HostLogDirFor(dir), drainMarker)
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "heldBack=3") {
		t.Fatalf("drain.log: got %q, want heldBack=3", log)
	}
}

// TestRunContinuous_StaleDrainPreResolvedWithoutPendingCountReportsUnknown
// verifies a review finding on #2678: a PreResolved caller that leaves
// cfg.PendingCount nil (no caller does this today, but the switch in
// continuous.go still guards against it) must not fall through to a
// fabricated heldBack of 0 -- that would look like a confirmed "nothing
// held back" reading when it is really "we have no way to know". The
// default branch of the switch must set heldBackUnknown instead, which
// Console()/HostLog() render as "unknown" rather than a bare 0.
func TestRunContinuous_StaleDrainPreResolvedWithoutPendingCountReportsUnknown(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1
	c.PreResolved = true
	// c.PendingCount intentionally left nil.

	fc := forge.NewFake(dispatchLabels(c))

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		// Never consulted on this path: PreResolved with no PendingCount
		// must not fall back to calling discover() again (that would risk
		// the claim-as-side-effect problem PendingCount exists to avoid);
		// it must report heldBackUnknown instead.
		return nil, nil, nil, nil, nil
	}
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	var err error
	stdout := testutil.CaptureStdout(t, func() {
		err = RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh)
	})

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}
	if !strings.Contains(stdout, "held back: unknown") {
		t.Fatalf("stdout: got %q, want a drain report line mentioning held back: unknown", stdout)
	}

	logPath := filepath.Join(dispatch.HostLogDirFor(dir), drainMarker)
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "heldBack=unknown") {
		t.Fatalf("drain.log: got %q, want heldBack=unknown", log)
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
// fail.
func TestRunContinuous_StaleDrainHeldBackExcludesBlockedIssues(t *testing.T) {
	c := baseConfig()
	c.Label = "agent-trigger"
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "9", State: "OPEN"}) // #2's blocker, unmet

	fr := runner.NewFake()

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	edges := map[string][]string{"2": {"9"}}
	discover := func() ([]Issue, map[string][]string, Sources, map[string]bool, error) {
		raw, err := fc.ListIssues(forge.Dispatchable)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		out := make([]Issue, len(raw))
		for i, fi := range raw {
			out[i] = Issue{Number: fi.Number, Title: fi.Title}
		}
		return out, edges, nil, nil, nil
	}
	// Stale on the very first refill, before anything ever launches --
	// MaxParallel=1 means the sole slot is acquired then immediately
	// released by the stale branch's defer, so outstanding never leaves
	// zero and heldBack is computed from the full discovered batch.
	fresh := func() (bool, bool, string) {
		return true, false, "rebuild needed (base tip changed image inputs)"
	}

	var err error
	stdout := testutil.CaptureStdout(t, func() {
		err = RunContinuous(c, nil, fc, fc, dir, f, s, discover, fresh)
	})

	if !errors.Is(err, ErrImageStale) {
		t.Fatalf("RunContinuous: got %v, want ErrImageStale", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %v, want none (stale fired before any launch)", fr.RunCalls)
	}
	if !strings.Contains(stdout, "1 issue(s) held back") {
		t.Fatalf("stdout: got %q, want a drain report line mentioning 1 issue(s) held back (only #1 is ready; #2 is blocked by #9)", stdout)
	}

	logPath := filepath.Join(dispatch.HostLogDirFor(dir), drainMarker)
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", logPath, err)
	}
	log := string(logBytes)
	if !strings.Contains(log, "heldBack=1") {
		t.Fatalf("drain.log: got %q, want heldBack=1 (only #1 is ready; #2 is blocked by #9, so it must not inflate the count)", log)
	}
}

// TestEmitDrainReport_OpenFailureLogsToStderr verifies a review finding on
// #2678: emitDrainReport's drain.log open failure is swallowed to stderr
// rather than failing the run, and does not crash or panic. pwd is a
// directory whose .spindrift/logs subdirectory was never created (unlike
// every real RunContinuous call, which always os.MkdirAll's it first), so
// dispatch.HostLogDirFor(pwd) names a path with no such directory and
// os.OpenFile's O_CREATE cannot create the file inside a missing parent.
func TestEmitDrainReport_OpenFailureLogsToStderr(t *testing.T) {
	dir := t.TempDir()
	report := DrainReport{StaleAt: time.Now(), DrainedAt: time.Now(), HeldBack: 1}

	stderr := testutil.CaptureStderr(t, func() {
		emitDrainReport(Config{}, dir, report)
	})

	if !strings.Contains(stderr, "continuous: open") {
		t.Errorf("stderr: got %q, want an \"continuous: open ...\" line reporting the failure", stderr)
	}

	logPath := filepath.Join(dispatch.HostLogDirFor(dir), drainMarker)
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Errorf("Stat(%s): got err=%v, want a not-exist error (no file should have been created)", logPath, err)
	}
}
