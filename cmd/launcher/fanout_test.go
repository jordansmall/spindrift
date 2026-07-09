package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// countingForge wraps a forge.Client and counts InProgress transitions atomically.
type countingForge struct {
	forge.Client
	claimCount *int32
}

func (f *countingForge) TransitionState(num string, from, to forge.DispatchState) error {
	if to == forge.InProgress {
		atomic.AddInt32(f.claimCount, 1)
	}
	return f.Client.TransitionState(num, from, to)
}

// signalRunner blocks the first Run call until released; subsequent calls return immediately.
type signalRunner struct {
	firstStarted chan struct{}
	release      chan struct{}
	once         sync.Once
}

func (r *signalRunner) EnsureReady() error { return nil }
func (r *signalRunner) IsReady() error     { return nil }
func (r *signalRunner) Reap(string) error  { return nil }
func (r *signalRunner) Run(_ runner.Box) error {
	isFirst := false
	r.once.Do(func() { isFirst = true })
	if isFirst {
		close(r.firstStarted)
		<-r.release
	}
	return nil
}

// TestFanOut_ClaimsGatedByMaxParallel verifies that claimIssue is called only
// after acquiring the semaphore slot, so at most maxParallel issues are claimed
// at any point in time.
func TestFanOut_ClaimsGatedByMaxParallel(t *testing.T) {
	c := baseConfig()
	c.maxParallel = 1
	c.label = "agent-trigger"

	inner := forge.NewFake()
	inner.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	inner.SetIssue(forge.Issue{Number: "2", Labels: []string{c.label}})

	var count int32
	fc := &countingForge{Client: inner, claimCount: &count}
	fr := &signalRunner{
		firstStarted: make(chan struct{}),
		release:      make(chan struct{}),
	}

	dir := tempLogDir(t)
	fanDone := make(chan struct{})
	go func() {
		fanOut(c, fc, dir, fr, []issue{
			{number: "1", title: "first"},
			{number: "2", title: "second"},
		})
		close(fanDone)
	}()

	// Block until the first run starts (sem is held; second goroutine cannot claim yet).
	select {
	case <-fr.firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first run never started")
	}

	// With the fix: claim happens after sem acquire, so exactly 1 claim so far.
	// With the bug: both claims happen before any goroutine acquires the sem.
	got := atomic.LoadInt32(&count)
	if got != 1 {
		t.Errorf("claims while first run is active: got %d, want 1", got)
	}

	// Release the first run and wait for all work to finish.
	close(fr.release)
	select {
	case <-fanDone:
	case <-time.After(5 * time.Second):
		t.Fatal("fanOut did not complete")
	}

	// Both issues must have been claimed by the end.
	if got = atomic.LoadInt32(&count); got != 2 {
		t.Errorf("total claims after fanOut: got %d, want 2", got)
	}
}

// TestFanOut_FailingContainerReleasesSemaphoreForLaterClaim verifies that when the
// first container fails, the semaphore slot is freed and the next issue can be
// claimed. This is the acceptance-criteria scenario: MAX_PARALLEL=1, failing first
// container, later issues only claimed after the slot frees.
func TestFanOut_FailingContainerReleasesSemaphoreForLaterClaim(t *testing.T) {
	c := baseConfig()
	c.maxParallel = 1
	c.label = "agent-trigger"

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.label}})

	var count int32
	cfc := &countingForge{Client: fc, claimCount: &count}

	fr := runner.NewFake()
	fr.RunErrs = []error{boxErr, nil} // first slot: fail; second: succeed

	dir := tempLogDir(t)
	fanOut(c, cfc, dir, fr, []issue{
		{number: "1", title: "first"},
		{number: "2", title: "second"},
	})

	// Both issues must have been claimed — the failing first container must not
	// prevent the second issue from being dispatched.
	if got := atomic.LoadInt32(&count); got != 2 {
		t.Errorf("total claims after fanOut with failing first box: got %d, want 2", got)
	}

	// Exactly one issue must carry failedLabel (the one whose box exited non-zero).
	// We don't assert which number failed — goroutine scheduling is non-deterministic.
	failed := 0
	for _, num := range []string{"1", "2"} {
		iss, err := fc.Issue(num)
		if err != nil {
			t.Fatalf("Issue(%q): %v", num, err)
		}
		if containsLabel(iss.Labels, c.failedLabel) {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("exactly 1 issue should carry failedLabel; got %d", failed)
	}
}

// TestFanOut_GatesEachIssueAfterBoxCompletes verifies that the merge gate runs
// inside each goroutine immediately after its box exits. An issue with a "ready"
// outcome and green CI must reach completeLabel before fanOut returns, without
// waiting for sibling boxes to finish.
func TestFanOut_GatesEachIssueAfterBoxCompletes(t *testing.T) {
	const prURL = "https://github.com/owner/repo/pull/10"

	c := baseConfig()
	c.maxParallel = 2
	c.branchPrefix = "agent/issue-"
	c.mergePollInterval = 0
	c.mergePollTimeout = 100

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.inProgressLabel}})
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	// The fake runner writes the outcome line into the log file (via box.Output)
	// before returning, simulating a box that ran successfully and emitted its result.
	fr := runner.NewFake()
	fr.WriteToOutput = []byte(fmt.Sprintf(
		"SPINDRIFT_OUTCOME issue=1 pr=%s status=ready note=ok\n", prURL,
	))

	dir := tempLogDir(t)
	fanOut(c, fc, dir, fr, []issue{{number: "1", title: "first"}})

	iss, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(%q): %v", "1", err)
	}
	if !containsLabel(iss.Labels, c.completeLabel) {
		t.Errorf("issue 1 must have %q after fanOut; got labels=%v", c.completeLabel, iss.Labels)
	}
}
