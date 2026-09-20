// This file drives recoverByNumber/cmdRecover through the same
// installStopSignal seam dispatch_stop_test.go and continuous_stop_test.go
// use for the other Box-launching paths (#3522).
package main

import (
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/waves"
)

// TestCmdRecover_SignalledStop_NoFailedLabel pins the first checkpoint: a
// stop closed before recoverByNumber ever calls it.Issue must exit
// exitSignalledStop, write no recover-reason output, leave the issue off the
// failed label, and still run cleanup -- a requested stop is not a recover
// failure (issue #3522).
func TestCmdRecover_SignalledStop_NoFailedLabel(t *testing.T) {
	withClosedStopSignal(t)

	outputPath := t.TempDir() + "/output"
	t.Setenv("GITHUB_OUTPUT", outputPath)

	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})

	dir := tempLogDir(t)
	var cleaned bool
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       testNewSettle(c, fc, testWired(fc), fc),
		cleanup:      func() { cleaned = true },
	}

	got := cmdRecover(lc, "42")

	if got != exitSignalledStop {
		t.Errorf("cmdRecover(lc, \"42\") = %d, want %d (exitSignalledStop)", got, exitSignalledStop)
	}
	if !cleaned {
		t.Error("cmdRecover did not run lc.cleanup()")
	}
	iss, err := fc.Issue("42")
	if err != nil {
		t.Fatalf("fc.Issue(42): %v", err)
	}
	if containsLabel(iss.Labels, c.failedLabel) {
		t.Errorf("issue #42 labels = %v, want no failed label", iss.Labels)
	}
	if _, err := os.Stat(outputPath); err == nil {
		t.Error("GITHUB_OUTPUT was written, want no recover-reason output for a signalled stop")
	}
}

// The abort counterpart of the stop case above.
func TestCmdRecover_SignalledAbort_NoFailedLabel(t *testing.T) {
	withClosedAbortSignal(t)

	outputPath := t.TempDir() + "/output"
	t.Setenv("GITHUB_OUTPUT", outputPath)

	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})

	dir := tempLogDir(t)
	var cleaned bool
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       testNewSettle(c, fc, testWired(fc), fc),
		cleanup:      func() { cleaned = true },
	}

	got := cmdRecover(lc, "42")

	if got != exitSignalledStop {
		t.Errorf("cmdRecover(lc, \"42\") = %d, want %d (exitSignalledStop)", got, exitSignalledStop)
	}
	if !cleaned {
		t.Error("cmdRecover did not run lc.cleanup()")
	}
	iss, err := fc.Issue("42")
	if err != nil {
		t.Fatalf("fc.Issue(42): %v", err)
	}
	if containsLabel(iss.Labels, c.failedLabel) {
		t.Errorf("issue #42 labels = %v, want no failed label", iss.Labels)
	}
	if _, err := os.Stat(outputPath); err == nil {
		t.Error("GITHUB_OUTPUT was written, want no recover-reason output for a signalled abort")
	}
}

// abortDuringSettle wraps settle.Fake so the mid-flight test below can close
// abortCh from inside the settle call itself: unlike selective dispatch's
// Run, recoverByNumber has no Box goroutine to synchronize an external abort
// against, so the settle stub is the one seam standing in for "CI is being
// watched" when the operator's second signal lands. It then waits on
// killSignal so the watcher's whole reclaimInFlight call -- not just the
// Kill inside it -- has provably started before SettleAdopted returns.
type abortDuringSettle struct {
	*settle.Fake
	abortCh    chan struct{}
	killSignal chan string
}

func (a *abortDuringSettle) SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string) {
	close(a.abortCh)
	select {
	case <-a.killSignal:
	case <-time.After(5 * time.Second):
		panic("timed out waiting for the watcher to reclaim " + num)
	}
	a.Fake.SettleAdopted(d, num, gen, prURL)
}

// TestRecoverByNumber_MidFlightAbort_ReclaimsToDispatchable proves the
// Gate's watcher reaches terminate.Reclaim's TransitionState(InProgress,
// Dispatchable) call for the adopt path exactly as it does for a wave: an
// abort that lands while the settle is watching CI must release the issue
// back to dispatchable, never leaving it on agent-complete or agent-failed
// (issue #3522).
func TestRecoverByNumber_MidFlightAbort_ReclaimsToDispatchable(t *testing.T) {
	orig := installStopSignal
	stopCh := make(chan struct{})
	abortCh := make(chan struct{})
	installStopSignal = func() (<-chan struct{}, <-chan struct{}, func()) {
		return stopCh, abortCh, func() {}
	}
	t.Cleanup(func() { installStopSignal = orig })

	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := fc.AgentBranch("42")
	fc.SetPR(branch, forge.PR{URL: testReconcilePR})

	dir := tempLogDir(t)
	killSignal := make(chan string, 1)
	rf := &killHook{Fake: runner.NewFake(), killed: killSignal}
	s := &abortDuringSettle{Fake: settle.NewFake(), abortCh: abortCh, killSignal: killSignal}

	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, rf), s, "42")

	if !errors.Is(err, waves.ErrSignalledStop) {
		t.Fatalf("recoverByNumber: got %v, want ErrSignalledStop", err)
	}
	if len(s.SettleAdoptedCalls) != 1 {
		t.Fatalf("SettleAdoptedCalls = %d, want 1 (settle ran before the abort landed)", len(s.SettleAdoptedCalls))
	}

	iss, ierr := fc.Issue("42")
	if ierr != nil {
		t.Fatalf("fc.Issue(42): %v", ierr)
	}
	if !containsLabel(iss.Labels, c.label) {
		t.Errorf("issue #42 labels = %v, want dispatchable label %q (reaped and reclaimed)", iss.Labels, c.label)
	}
	for _, terminal := range []string{c.completeLabel, c.failedLabel} {
		if containsLabel(iss.Labels, terminal) {
			t.Errorf("issue #42 labels = %v, want no terminal label %q", iss.Labels, terminal)
		}
	}
}

// pollGoroutines waits up to 2s for runtime.NumGoroutine() to settle back to
// at most baseline, giving a just-torn-down watcher goroutine (and the
// runtime's own scheduling housekeeping) a moment to actually exit rather
// than asserting on a single racy sample.
func pollGoroutines(baseline int) int {
	deadline := time.Now().Add(2 * time.Second)
	got := runtime.NumGoroutine()
	for got > baseline && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		runtime.Gosched()
		got = runtime.NumGoroutine()
	}
	return got
}

// TestRecoverByNumber_IssueFetchFails_NoWatcherLeak pins the first
// early-return finding: it.Issue failing must not strand the Gate's abort
// watcher goroutine behind. Before the fix, recoverByNumber's gate.Watch()
// was joined only by the inline gate.Settle() calls further down, so this
// return -- taken before either of those runs -- leaked the watcher (plus
// the Gate/tracker/forge/factory it captures) for the life of the process
// (issue #3522).
func TestRecoverByNumber_IssueFetchFails_NoWatcherLeak(t *testing.T) {
	orig := installStopSignal
	stopCh := make(chan struct{})
	abortCh := make(chan struct{})
	installStopSignal = func() (<-chan struct{}, <-chan struct{}, func()) {
		return stopCh, abortCh, func() {}
	}
	t.Cleanup(func() { installStopSignal = orig })

	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	// No SetIssue for "42": it.Issue returns an error, forcing the
	// early return recoverFailed goes through.
	dir := tempLogDir(t)

	runtime.GC()
	baseline := runtime.NumGoroutine()

	if err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, nil), settle.NewFake(), "42"); err == nil {
		t.Fatal("recoverByNumber: want error for a missing issue, got nil")
	}

	runtime.GC()
	if got := pollGoroutines(baseline); got > baseline {
		t.Errorf("goroutines after recoverByNumber returned = %d, want <= baseline %d (watcher leaked)", got, baseline)
	}
}

// abortAfterSettleAdopted closes abortCh only once the underlying
// SettleAdopted call has returned, standing in for a second operator signal
// landing the instant after the PR merged and the issue was labelled
// agent-complete -- the blocking finding's own scenario (issue #3522,
// recoverByNumber's SettleAdopted arm). It also flips the issue to Complete first, mirroring what a
// real settle does on that path, so a wrongly-triggered reclaim is visible
// as a Complete->Dispatchable transition plus a "Terminated by operator"
// comment, not just a missing one.
type abortAfterSettleAdopted struct {
	*settle.Fake
	fc      *forge.Fake
	num     string
	abortCh chan struct{}
}

func (a *abortAfterSettleAdopted) SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string) {
	a.Fake.SettleAdopted(d, num, gen, prURL)
	if err := a.fc.TransitionState(a.num, forge.InProgress, forge.Complete); err != nil {
		panic(err)
	}
	close(a.abortCh)
}

// TestRecoverByNumber_AbortAfterSettleAdopted_NoReclaim pins the blocking
// finding: an abort that closes after SettleAdopted has already finished
// (PR merged, issue labelled agent-complete) must not drag the issue back
// to Dispatchable. Before the fix, `defer gate.Leave(iss.number)` ran only
// after the inline `gate.Settle()` below it, so Settle's final abort check
// still found #42 in-flight and reclaimed a finished issue (issue #3522,
// in both recoverByNumber's SettleRelayedBranch and SettleAdopted arms).
func TestRecoverByNumber_AbortAfterSettleAdopted_NoReclaim(t *testing.T) {
	orig := installStopSignal
	stopCh := make(chan struct{})
	abortCh := make(chan struct{})
	installStopSignal = func() (<-chan struct{}, <-chan struct{}, func()) {
		return stopCh, abortCh, func() {}
	}
	t.Cleanup(func() { installStopSignal = orig })

	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := fc.AgentBranch("42")
	fc.SetPR(branch, forge.PR{URL: testReconcilePR})

	dir := tempLogDir(t)
	s := &abortAfterSettleAdopted{Fake: settle.NewFake(), fc: fc, num: "42", abortCh: abortCh}

	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, runner.NewFake()), s, "42")

	if !errors.Is(err, waves.ErrSignalledStop) {
		t.Fatalf("recoverByNumber: got %v, want ErrSignalledStop", err)
	}
	if len(s.SettleAdoptedCalls) != 1 {
		t.Fatalf("SettleAdoptedCalls = %d, want 1", len(s.SettleAdoptedCalls))
	}

	iss, ierr := fc.Issue("42")
	if ierr != nil {
		t.Fatalf("fc.Issue(42): %v", ierr)
	}
	if !containsLabel(iss.Labels, c.completeLabel) {
		t.Errorf("issue #42 labels = %v, want completeLabel %q (settle's own decision, untouched by the late abort)", iss.Labels, c.completeLabel)
	}
	if containsLabel(iss.Labels, c.label) {
		t.Errorf("issue #42 labels = %v, want no dispatchable label %q (must not be reclaimed after settling)", iss.Labels, c.label)
	}
	for _, comment := range fc.CommentsFor["42"] {
		if strings.Contains(comment.Body, "Terminated by operator") {
			t.Errorf("issue #42 got a Terminated-by-operator comment after it already settled: %q", comment.Body)
		}
	}
}
