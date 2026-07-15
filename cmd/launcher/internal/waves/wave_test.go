package waves

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// countingForge wraps a *forge.Fake and counts InProgress transitions
// atomically. Embedding the concrete *Fake (rather than an interface)
// promotes its full IssueTracker + CodeForge + PRForge surface, so a
// countingForge value satisfies whichever seam(s) a call site needs.
type countingForge struct {
	*forge.Fake
	claimCount *int32
}

func (f *countingForge) TransitionState(num string, from, to forge.DispatchState) error {
	if to == forge.InProgress {
		atomic.AddInt32(f.claimCount, 1)
	}
	return f.Fake.TransitionState(num, from, to)
}

// signalRunner blocks the first Run call until released; subsequent calls return immediately.
type signalRunner struct {
	firstStarted chan struct{}
	release      chan struct{}
	once         sync.Once
}

func (r *signalRunner) EnsureReady() error             { return nil }
func (r *signalRunner) IsReady() error                 { return nil }
func (r *signalRunner) Reap(string) error              { return nil }
func (r *signalRunner) Kill(string) error              { return nil }
func (r *signalRunner) IsRunning(string) bool          { return false }
func (r *signalRunner) ListRunning() ([]string, error) { return nil, nil }
func (r *signalRunner) Run(_ runner.Box) error {
	isFirst := false
	r.once.Do(func() { isFirst = true })
	if isFirst {
		close(r.firstStarted)
		<-r.release
	}
	return nil
}

// TestDispatchWave_ClaimsGatedByMaxParallel verifies that claimIssue is called only
// after acquiring the semaphore slot, so at most maxParallel issues are claimed
// at any point in time.
func TestDispatchWave_ClaimsGatedByMaxParallel(t *testing.T) {
	c := baseConfig()
	c.MaxParallel = 1
	c.Label = "agent-trigger"

	inner := forge.NewFake()
	inner.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	inner.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})

	var count int32
	fc := &countingForge{Fake: inner, claimCount: &count}
	fr := &signalRunner{
		firstStarted: make(chan struct{}),
		release:      make(chan struct{}),
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	waveDone := make(chan struct{})
	go func() {
		dispatchWave(c, fc, f, s, []Issue{
			{Number: "1", Title: "first"},
			{Number: "2", Title: "second"},
		})
		close(waveDone)
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
	case <-waveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatchWave did not complete")
	}

	// Both issues must have been claimed by the end.
	if got = atomic.LoadInt32(&count); got != 2 {
		t.Errorf("total claims after dispatchWave: got %d, want 2", got)
	}
}

// TestDispatchWave_FailingContainerReleasesSemaphoreForLaterClaim verifies that when the
// first container fails, the semaphore slot is freed and the next issue can be
// claimed. This is the acceptance-criteria scenario: MAX_PARALLEL=1, failing first
// container, later issues only claimed after the slot frees.
func TestDispatchWave_FailingContainerReleasesSemaphoreForLaterClaim(t *testing.T) {
	c := baseConfig()
	c.MaxParallel = 1
	c.Label = "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.Label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.Label}})

	var count int32
	cfc := &countingForge{Fake: fc, claimCount: &count}

	fr := runner.NewFake()
	fr.RunErrs = []error{boxErr, nil} // first slot: fail; second: succeed

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(cfc, cfc)
	dispatchWave(c, cfc, f, s, []Issue{
		{Number: "1", Title: "first"},
		{Number: "2", Title: "second"},
	})

	// Both issues must have been claimed — the failing first container must not
	// prevent the second issue from being dispatched.
	if got := atomic.LoadInt32(&count); got != 2 {
		t.Errorf("total claims after dispatchWave with failing first box: got %d, want 2", got)
	}

	// Exactly one issue must carry failedLabel (the one whose box exited non-zero).
	// We don't assert which number failed — goroutine scheduling is non-deterministic.
	failed := 0
	for _, num := range []string{"1", "2"} {
		iss, err := fc.Issue(num)
		if err != nil {
			t.Fatalf("Issue(%q): %v", num, err)
		}
		if containsLabel(iss.Labels, c.FailedLabel) {
			failed++
		}
	}
	if failed != 1 {
		t.Errorf("exactly 1 issue should carry failedLabel; got %d", failed)
	}
}

// TestDispatchWave_AlreadyInFlightSkipsWithoutFailedTransition verifies that
// when the runner reports the issue's container is already running,
// dispatchWave skips it as a distinct outcome: no failed-transition, the
// live run's in-progress claim stands untouched, no settle/merge attempt is
// made, and a distinct output line names the issue (issue #562).
func TestDispatchWave_AlreadyInFlightSkipsWithoutFailedTransition(t *testing.T) {
	c := baseConfig()
	c.MaxParallel = 1

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.InProgressLabel}})

	fr := runner.NewFake()
	fr.IsRunningRet = true

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)

	out := captureStdout(t, func() {
		dispatchWave(c, fc, f, s, []Issue{{Number: "1", Title: "first"}})
	})

	iss, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(%q): %v", "1", err)
	}
	if containsLabel(iss.Labels, c.FailedLabel) {
		t.Errorf("issue must NOT have %q when already in flight; labels=%v", c.FailedLabel, iss.Labels)
	}
	if !containsLabel(iss.Labels, c.InProgressLabel) {
		t.Errorf("issue must remain %q (live run's claim stands); labels=%v", c.InProgressLabel, iss.Labels)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("runner.Run: want 0 calls when already running, got %d", len(fr.RunCalls))
	}
	if !strings.Contains(out, "#1") || !strings.Contains(out, "already in flight") {
		t.Errorf("want a distinct 'already in flight' line naming #1; got output=%q", out)
	}
}

// TestDispatchWave_GatesEachIssueAfterBoxCompletes verifies that the merge gate runs
// inside each goroutine immediately after its box exits. An issue with a "ready"
// outcome and green CI must reach completeLabel before dispatchWave returns, without
// waiting for sibling boxes to finish.
func TestDispatchWave_GatesEachIssueAfterBoxCompletes(t *testing.T) {
	const prURL = "https://github.com/owner/repo/pull/10"

	c := baseConfig()
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.InProgressLabel}})
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	// The fake runner writes the outcome line into the log file (via box.Output)
	// before returning, simulating a box that ran successfully and emitted its result.
	fr := runner.NewFake()
	fr.WriteToOutput = []byte(fmt.Sprintf(
		"SPINDRIFT_OUTCOME issue=1 landing=%s status=ready note=ok\n", prURL,
	))

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	dispatchWave(c, fc, f, s, []Issue{{Number: "1", Title: "first"}})

	iss, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(%q): %v", "1", err)
	}
	if !containsLabel(iss.Labels, c.CompleteLabel) {
		t.Errorf("issue 1 must have %q after dispatchWave; got labels=%v", c.CompleteLabel, iss.Labels)
	}
}

// TestDispatchWave_GitForge_ImmediateLandsWithoutVerifyingAPR verifies that a
// CODE_FORGE=git outcome carrying a branch ref (not a PR URL) lands cleanly
// through the same dispatchWave→settle.Settle path used for github: the issue reaches
// agent-complete and is never demoted to agent-failed by a PR-shaped
// post-merge check that does not apply to a push-only forge.
func TestDispatchWave_GitForge_ImmediateLandsWithoutVerifyingAPR(t *testing.T) {
	const branch = "agent/issue-1"

	c := baseConfig()
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.InProgressLabel}})
	// The real git Code Forge has no PR concept — PRState always errors. A
	// settle path that (incorrectly) called verifyMerged for a push-only
	// forge would read this as "not merged" and wrongly demote the issue to
	// failed.
	fc.PRStateErr = errors.New("PRState: not supported by the git Code Forge (push-only, no PR concept)")

	fr := runner.NewFake()
	fr.WriteToOutput = []byte(fmt.Sprintf(
		"SPINDRIFT_OUTCOME issue=1 landing=%s status=ready note=ok\n", branch,
	))

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc.AsPushOnly())
	dispatchWave(c, fc, f, s, []Issue{{Number: "1", Title: "first"}})

	iss, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(%q): %v", "1", err)
	}
	if !containsLabel(iss.Labels, c.CompleteLabel) {
		t.Errorf("issue 1 must have %q after dispatchWave; got labels=%v", c.CompleteLabel, iss.Labels)
	}
	if containsLabel(iss.Labels, c.FailedLabel) {
		t.Errorf("issue 1 must NOT have %q; got labels=%v", c.FailedLabel, iss.Labels)
	}
	if fc.Merged != branch {
		t.Errorf("expected Merge(%q) for MERGE_MODE=immediate; fc.Merged=%q", branch, fc.Merged)
	}
}

// TestDispatchWave_GitForge_MergedStatusDoesNotDemoteToFailed verifies that a
// CODE_FORGE=git outcome carrying status=merged (a status the grammar
// documents as valid, outcome.go:24) never reaches verifyMerged's PR-state
// check: the git Code Forge's PRState always errors, so an unguarded call
// would wrongly demote the issue to agent-failed even though nothing is
// actually wrong.
func TestDispatchWave_GitForge_MergedStatusDoesNotDemoteToFailed(t *testing.T) {
	const branch = "agent/issue-1"

	c := baseConfig()
	c.MaxParallel = 2

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.InProgressLabel}})
	fc.PRStateErr = errors.New("PRState: not supported by the git Code Forge (push-only, no PR concept)")

	fr := runner.NewFake()
	fr.WriteToOutput = []byte(fmt.Sprintf(
		"SPINDRIFT_OUTCOME issue=1 landing=%s status=merged note=ok\n", branch,
	))

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc.AsPushOnly())
	dispatchWave(c, fc, f, s, []Issue{{Number: "1", Title: "first"}})

	iss, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(%q): %v", "1", err)
	}
	if containsLabel(iss.Labels, c.FailedLabel) {
		t.Errorf("issue 1 must NOT have %q; got labels=%v", c.FailedLabel, iss.Labels)
	}
}
