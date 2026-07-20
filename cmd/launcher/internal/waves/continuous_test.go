package waves

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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

	// Simulate two '+' presses landing faster than the grow listener can
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
	// send.
	limiter.mu.Lock()
	limiter.cap = 3
	limiter.mu.Unlock()
	limiter.cond.Broadcast()
	select {
	case limiter.grow <- struct{}{}:
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
