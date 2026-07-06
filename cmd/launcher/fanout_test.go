package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// countingForge wraps a forge.Client and counts in-progress claim swaps atomically.
type countingForge struct {
	forge.Client
	claimCount *int32
	inProg     string
}

func (f *countingForge) SwapLabel(num, add, remove string) error {
	if add == f.inProg {
		atomic.AddInt32(f.claimCount, 1)
	}
	return f.Client.SwapLabel(num, add, remove)
}

// signalRunner blocks the first Run call until released; subsequent calls return immediately.
type signalRunner struct {
	firstStarted chan struct{}
	release      chan struct{}
	once         sync.Once
}

func (r *signalRunner) EnsureReady() error { return nil }
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
	fc := &countingForge{Client: inner, claimCount: &count, inProg: c.inProgressLabel}
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
