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
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/testutil"
)

// countingForge wraps a *forge.Fake and counts InProgress transitions
// atomically. It embeds the concrete *Fake rather than an interface so that one
// value satisfies whichever of IssueTracker, CodeForge and PRForge a call site
// needs.
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
func (r *signalRunner) RegistryProxyTransport() (registrymanifest.Endpoint, bool, error) {
	return registrymanifest.NewUnixEndpoint(""), false, nil
}
func (r *signalRunner) Run(_ runner.Box) error {
	isFirst := false
	r.once.Do(func() { isFirst = true })
	if isFirst {
		close(r.firstStarted)
		<-r.release
	}
	return nil
}

// TestDispatchWave_ClaimsGatedByMaxParallel pins that claimer.Claim runs only
// after the goroutine acquires its semaphore slot, so at most maxParallel
// issues are claimed at any point in time.
func TestDispatchWave_ClaimsGatedByMaxParallel(t *testing.T) {
	c := baseConfig()
	c.MaxParallel = 1
	label := "agent-trigger"

	inner := forge.NewFake()
	inner.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	inner.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	var count int32
	fc := &countingForge{Fake: inner, claimCount: &count}
	fr := &signalRunner{
		firstStarted: make(chan struct{}),
		release:      make(chan struct{}),
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	waveDone := make(chan struct{})
	go func() {
		dispatchWave(c, fc, f, s, []Issue{
			{Number: "1", Title: "first"},
			{Number: "2", Title: "second"},
		}, claimer)
		close(waveDone)
	}()

	// While the first run holds the only semaphore slot, the second goroutine
	// cannot claim yet.
	select {
	case <-fr.firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first run never started")
	}

	// Before the fix, both claims happened before any goroutine acquired the
	// semaphore.
	got := atomic.LoadInt32(&count)
	if got != 1 {
		t.Errorf("claims while first run is active: got %d, want 1", got)
	}

	close(fr.release)
	select {
	case <-waveDone:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatchWave did not complete")
	}

	if got = atomic.LoadInt32(&count); got != 2 {
		t.Errorf("total claims after dispatchWave: got %d, want 2", got)
	}
}

// TestDispatchWave_FailingContainerReleasesSemaphoreForLaterClaim pins that a
// failing first container still frees its semaphore slot, so the next issue is
// claimed once the slot frees.
func TestDispatchWave_FailingContainerReleasesSemaphoreForLaterClaim(t *testing.T) {
	const prURL = "https://github.com/owner/repo/pull/2"

	c := baseConfig()
	c.MaxParallel = 1
	label := "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	var count int32
	cfc := &countingForge{Fake: fc, claimCount: &count}

	fr := runner.NewFake()
	// The succeeding box must still report an outcome, because an empty log
	// also demotes to failedLabel (since #1605) and would muddy this test's
	// target of semaphore release. The line lands in both boxes' logs, but the
	// failing box returns a non-nil Run error, which routes through
	// classification instead of outcome parsing, so the line is inert there.
	var calls int32
	fr.RunFunc = func(box runner.Box) error {
		n := atomic.AddInt32(&calls, 1)
		if box.Output != nil {
			fmt.Fprintf(box.Output, "SPINDRIFT_OUTCOME issue=2 landing=%s status=ready note=ok nonce=%s\n",
				prURL, box.Env["RUN_NONCE"])
		}
		if n == 1 {
			return boxErr
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(cfc, cfc)
	claimer := NewLabelClaimer(cfc, label, testInProgressLabel)
	dispatchWave(c, cfc, f, s, []Issue{
		{Number: "1", Title: "first"},
		{Number: "2", Title: "second"},
	}, claimer)

	if got := atomic.LoadInt32(&count); got != 2 {
		t.Errorf("total claims after dispatchWave with failing first box: got %d, want 2", got)
	}

	// Which issue number carries failedLabel is not asserted, because goroutine
	// scheduling is non-deterministic.
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

// TestDispatchWave_AlreadyInFlightSkipsWithoutFailedTransition pins that when
// the runner reports the container is already running, dispatchWave skips the
// issue without a failed transition or a settle attempt, leaves the live run's
// in-progress claim alone, and prints a line naming the issue (issue #562).
func TestDispatchWave_AlreadyInFlightSkipsWithoutFailedTransition(t *testing.T) {
	c := baseConfig()
	c.MaxParallel = 1
	label := "" // empty: this test never sets a Dispatchable label

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{testInProgressLabel}})

	fr := runner.NewFake()
	fr.IsRunningRet = true

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	out := testutil.CaptureStdout(t, func() {
		dispatchWave(c, fc, f, s, []Issue{{Number: "1", Title: "first"}}, claimer)
	})

	iss, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(%q): %v", "1", err)
	}
	if containsLabel(iss.Labels, c.FailedLabel) {
		t.Errorf("issue must NOT have %q when already in flight; labels=%v", c.FailedLabel, iss.Labels)
	}
	if !containsLabel(iss.Labels, testInProgressLabel) {
		t.Errorf("issue must remain %q (live run's claim stands); labels=%v", testInProgressLabel, iss.Labels)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("runner.Run: want 0 calls when already running, got %d", len(fr.RunCalls))
	}
	if !strings.Contains(out, "#1") || !strings.Contains(out, "already in flight") {
		t.Errorf("want a distinct 'already in flight' line naming #1; got output=%q", out)
	}
}

// TestDispatchWave_FailedBoxWithEmptyLogPrintsErrToStderr pins that a box which
// never launched (RunErr with no log output, so Result.Err is populated) has
// its reason printed on stderr next to the terse FAILED line, matching
// retry.go's "?? #N: %v" convention (issue #3119).
func TestDispatchWave_FailedBoxWithEmptyLogPrintsErrToStderr(t *testing.T) {
	c := baseConfig()
	c.MaxParallel = 1
	label := "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fr := runner.NewFake()
	fr.RunErr = boxErr

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	errOut := testutil.CaptureStderr(t, func() {
		dispatchWave(c, fc, f, s, []Issue{{Number: "1", Title: "first"}}, claimer)
	})

	if !strings.Contains(errOut, "?? #1: ") || !strings.Contains(errOut, boxErr.Error()) {
		t.Errorf("want a '?? #1: <err>' diagnostic line on stderr; got stderr=%q", errOut)
	}
}

// TestDispatchWave_FailedBoxWithLogOutputPrintsNoExtraStderr pins that a box
// which ran and genuinely failed (leaving content in its log) leaves Result.Err
// nil, so dispatchWave prints no extra "??" diagnostic beyond the terse FAILED
// line (issue #3119).
func TestDispatchWave_FailedBoxWithLogOutputPrintsNoExtraStderr(t *testing.T) {
	c := baseConfig()
	c.MaxParallel = 1
	label := "agent-trigger"

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fr := runner.NewFake()
	fr.RunErr = boxErr
	fr.WriteToOutput = []byte("some box output before it failed\n")

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	errOut := testutil.CaptureStderr(t, func() {
		dispatchWave(c, fc, f, s, []Issue{{Number: "1", Title: "first"}}, claimer)
	})

	if strings.Contains(errOut, "?? #1") {
		t.Errorf("want no '?? #1' diagnostic when the box ran and produced log output; got stderr=%q", errOut)
	}
}

// TestDispatchWave_GatesEachIssueAfterBoxCompletes pins that the merge gate
// runs inside each goroutine right after its own box exits, so an issue with a
// "ready" outcome and green CI reaches completeLabel without waiting for
// sibling boxes.
func TestDispatchWave_GatesEachIssueAfterBoxCompletes(t *testing.T) {
	const prURL = "https://github.com/owner/repo/pull/10"

	c := baseConfig()
	c.MaxParallel = 2
	label := "" // empty: this test never sets a Dispatchable label

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{testInProgressLabel}})
	fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})

	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error {
		if box.Output != nil {
			fmt.Fprintf(box.Output, "SPINDRIFT_OUTCOME issue=1 landing=%s status=ready note=ok nonce=%s\n",
				prURL, box.Env["RUN_NONCE"])
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	dispatchWave(c, fc, f, s, []Issue{{Number: "1", Title: "first"}}, claimer)

	iss, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(%q): %v", "1", err)
	}
	if !containsLabel(iss.Labels, c.CompleteLabel) {
		t.Errorf("issue 1 must have %q after dispatchWave; got labels=%v", c.CompleteLabel, iss.Labels)
	}
}

// TestDispatchWave_GitForge_ImmediateLandsWithoutVerifyingAPR pins that a
// CODE_FORGE=git outcome carrying a branch ref instead of a PR URL still lands
// through the dispatchWave and settle.Settle path: the issue reaches
// agent-complete and no PR-shaped post-merge check demotes it to agent-failed.
func TestDispatchWave_GitForge_ImmediateLandsWithoutVerifyingAPR(t *testing.T) {
	const branch = "agent/issue-1"

	c := baseConfig()
	c.MaxParallel = 2
	label := "" // empty: this test never sets a Dispatchable label

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{testInProgressLabel}})
	// The real git Code Forge has no PR concept, so PRState always errors. A
	// settle path that wrongly called verifyMerged for a push-only forge would
	// read that error as "not merged" and demote the issue to failed.
	fc.PRStateErr = errors.New("PRState: not supported by the git Code Forge (push-only, no PR concept)")

	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error {
		if box.Output != nil {
			fmt.Fprintf(box.Output, "SPINDRIFT_OUTCOME issue=1 landing=%s status=ready note=ok nonce=%s\n",
				branch, box.Env["RUN_NONCE"])
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc.AsPushOnly())
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	dispatchWave(c, fc, f, s, []Issue{{Number: "1", Title: "first"}}, claimer)

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

// TestDispatchWave_GitForge_MergedStatusDoesNotDemoteToFailed pins that a
// CODE_FORGE=git outcome carrying status=merged (valid per the grammar in
// outcome.go) never reaches verifyMerged's PR-state check: the git Code Forge's
// PRState always errors, so an unguarded call would demote a healthy issue to
// agent-failed.
func TestDispatchWave_GitForge_MergedStatusDoesNotDemoteToFailed(t *testing.T) {
	const branch = "agent/issue-1"

	c := baseConfig()
	c.MaxParallel = 2
	label := "" // empty: this test never sets a Dispatchable label

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{testInProgressLabel}})
	fc.PRStateErr = errors.New("PRState: not supported by the git Code Forge (push-only, no PR concept)")

	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error {
		if box.Output != nil {
			fmt.Fprintf(box.Output, "SPINDRIFT_OUTCOME issue=1 landing=%s status=merged note=ok nonce=%s\n",
				branch, box.Env["RUN_NONCE"])
		}
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc.AsPushOnly())
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)
	dispatchWave(c, fc, f, s, []Issue{{Number: "1", Title: "first"}}, claimer)

	iss, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(%q): %v", "1", err)
	}
	if containsLabel(iss.Labels, c.FailedLabel) {
		t.Errorf("issue 1 must NOT have %q; got labels=%v", c.FailedLabel, iss.Labels)
	}
}
