// This file drives recoverByNumber/cmdRecover through the same
// installStopSignal seam dispatch_stop_test.go and continuous_stop_test.go
// use for the other Box-launching paths (#3522).
package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
	"spindrift.dev/launcher/internal/waves"
)

// TestCmdRecover_SignalledStop_NoFailedLabel pins the first checkpoint: a
// stop closed before recoverByNumber acts on it.Issue must exit
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

	got := cmdRecover(lc, "42", io.Discard, io.Discard)

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

// The abort counterpart of the stop case above, in the both-closed shape a
// real second Ctrl-C delivers. It does not isolate abort (the closed stop wins
// first); TestRecoverByNumber_MidFlightAbort_ReclaimsToDispatchable isolates
// recover's abort wiring.
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

	got := cmdRecover(lc, "42", io.Discard, io.Discard)

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

	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, rf), s, "42", io.Discard, io.Discard)

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

// TestRecoverByNumber_MidFlightAbort_ParkedFailedLabelStillReaped pins that a
// hand-run recover, which never claims the issue, keeps the agent-failed it
// was parked with; the Gate must not read that as this run's own settle and
// spare the in-flight adopt (issue #4888).
func TestRecoverByNumber_MidFlightAbort_ParkedFailedLabelStillReaped(t *testing.T) {
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
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.failedLabel}})
	fc.SetPR(fc.AgentBranch("42"), forge.PR{URL: testReconcilePR})

	dir := tempLogDir(t)
	killSignal := make(chan string, 1)
	rf := &killHook{Fake: runner.NewFake(), killed: killSignal}
	s := &abortDuringSettle{Fake: settle.NewFake(), abortCh: abortCh, killSignal: killSignal}

	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, rf), s, "42", io.Discard, io.Discard)

	if !errors.Is(err, waves.ErrSignalledStop) {
		t.Fatalf("recoverByNumber: got %v, want ErrSignalledStop", err)
	}
	if len(s.SettleAdoptedCalls) != 1 {
		t.Fatalf("SettleAdoptedCalls = %d, want 1", len(s.SettleAdoptedCalls))
	}
	var reclaimed bool
	for _, call := range fc.TransitionStateCalls {
		if call.Num == "42" && call.From == forge.InProgress && call.To == forge.Dispatchable {
			reclaimed = true
		}
	}
	if !reclaimed {
		t.Errorf("TransitionStateCalls = %+v, want #42 InProgress->Dispatchable (parked failed label is not a settle)", fc.TransitionStateCalls)
	}
	var commented bool
	for _, call := range fc.CommentCalls {
		if call.Num == "42" && strings.Contains(call.Body, terminate.CommentSuffix) {
			commented = true
		}
	}
	if !commented {
		t.Errorf("CommentCalls = %+v, want a reclaim comment on #42", fc.CommentCalls)
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

	if err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, nil), settle.NewFake(), "42", io.Discard, io.Discard); err == nil {
		t.Fatal("recoverByNumber: want error for a missing issue, got nil")
	}

	runtime.GC()
	if got := pollGoroutines(baseline); got > baseline {
		t.Errorf("goroutines after recoverByNumber returned = %d, want <= baseline %d (watcher leaked)", got, baseline)
	}
}

// abortAfterFakeSettle closes abortCh only once the underlying SettleAdopted
// or SettleRelayedBranch call has returned, standing in for a second operator
// signal landing the instant after the settle labelled the issue
// agent-complete -- the blocking finding's own scenario (issue #3522,
// recoverByNumber's SettleAdopted arm). It also flips the issue to Complete
// first, mirroring what a real settle does on that path, so a wrongly-triggered
// reclaim is visible as a Complete->Dispatchable transition plus a reclaim
// comment under any trigger, not just a missing one.
type abortAfterFakeSettle struct {
	*settle.Fake
	fc      *forge.Fake
	num     string
	abortCh chan struct{}
}

func (a *abortAfterFakeSettle) SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string) {
	a.Fake.SettleAdopted(d, num, gen, prURL)
	if err := a.fc.TransitionState(a.num, forge.InProgress, forge.Complete); err != nil {
		panic(err)
	}
	close(a.abortCh)
}

func (a *abortAfterFakeSettle) SettleRelayedBranch(d dispatch.Dispatcher, num string, gen uint64, sit settle.Situation, result dispatch.Result) (bool, error) {
	settled, err := a.Fake.SettleRelayedBranch(d, num, gen, sit, result)
	if terr := a.fc.TransitionState(a.num, forge.InProgress, forge.Complete); terr != nil {
		panic(terr)
	}
	close(a.abortCh)
	return settled, err
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
	s := &abortAfterFakeSettle{Fake: settle.NewFake(), fc: fc, num: "42", abortCh: abortCh}

	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, runner.NewFake()), s, "42", io.Discard, io.Discard)

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
		if strings.Contains(comment.Body, terminate.CommentSuffix) {
			t.Errorf("issue #42 got a reclaim comment after it already settled: %q", comment.Body)
		}
	}
}

// TestRecoverByNumber_AbortAfterSettleRelayed_ExitsStopWithoutRecoverFailed
// pins issue #4694: an abort landing after the relayed-branch settle finished,
// which the watcher did not reclaim, must still exit as a stop and never reach
// recoverFailed's agent-failed park.
func TestRecoverByNumber_AbortAfterSettleRelayed_ExitsStopWithoutRecoverFailed(t *testing.T) {
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

	dir := tempLogDir(t)
	fk := settle.NewFake()
	fk.SettleRelayedBranchErr = errors.New("relay: boom")
	s := &abortAfterFakeSettle{Fake: fk, fc: fc, num: "42", abortCh: abortCh}

	err := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, runner.NewFake()), s, "42", io.Discard, io.Discard)

	if !errors.Is(err, waves.ErrSignalledStop) {
		t.Fatalf("recoverByNumber: got %v, want ErrSignalledStop", err)
	}
	if len(s.SettleRelayedBranchCalls) != 1 {
		t.Fatalf("SettleRelayedBranchCalls = %d, want 1", len(s.SettleRelayedBranchCalls))
	}
	iss, ierr := fc.Issue("42")
	if ierr != nil {
		t.Fatalf("fc.Issue(42): %v", ierr)
	}
	if containsLabel(iss.Labels, c.failedLabel) {
		t.Errorf("issue #42 labels = %v, want no %q (recoverFailed must not run)", iss.Labels, c.failedLabel)
	}
}

// TestRecoverByNumber_OwnerSettling_Skips pins issue #4364: a real Dispatch
// keeps its claim after Run returns, until Close, so recover skips an issue
// whose owner is still settling rather than adopting its PR alongside it.
func TestRecoverByNumber_OwnerSettling_Skips(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	fc.SetPR(fc.AgentBranch("42"), forge.PR{URL: testReconcilePR})

	dir := tempLogDir(t)

	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error {
		line := "SPINDRIFT_OUTCOME issue=42 landing=" + testReconcilePR + " status=ready note=ok nonce=" + box.Env["RUN_NONCE"] + "\n"
		box.Output.Write([]byte(line)) //nolint:errcheck
		return nil
	}
	owner := testFactory(t, dir, fr).New("42", "t")
	defer owner.Close()
	succeeded := dispatch.Route(owner.Run(),
		func() bool { return false },
		func(dispatch.Result) bool { return false },
		func(dispatch.Result) bool { return true })
	if !succeeded {
		t.Fatalf("owner.Run: want succeeded")
	}

	s := settle.NewFake()
	var stdout bytes.Buffer
	recErr := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, nil), s, "42", &stdout, io.Discard)
	out := stdout.String()

	if recErr != nil {
		t.Fatalf("recoverByNumber: got %v, want nil (skip, not an error)", recErr)
	}
	if !strings.Contains(out, "status=skipped") || !strings.Contains(out, "run settling") {
		t.Errorf("stdout = %q, want a skip line while the owner's Dispatch is unclosed", out)
	}
	if len(s.SettleAdoptedCalls) != 0 {
		t.Errorf("SettleAdoptedCalls = %d, want 0 (owner is still settling)", len(s.SettleAdoptedCalls))
	}
	if len(s.SettleRelayedBranchCalls) != 0 {
		t.Errorf("SettleRelayedBranchCalls = %d, want 0", len(s.SettleRelayedBranchCalls))
	}
}

// TestRecoverByNumber_IssueClaimedElsewhere_Skips pins the acceptance
// criterion for issue #3885: an issue whose flock claim.go's claim file
// (2baa3b3d) another process still holds -- a live Run() -- must be skipped
// outright, never relayed or settled out from under that owner.
func TestRecoverByNumber_IssueClaimedElsewhere_Skips(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	branch := fc.AgentBranch("42")
	fc.SetPR(branch, forge.PR{URL: testReconcilePR})

	dir := tempLogDir(t)

	// Seeded as a live Run() would leave one mid-flight, to prove recover
	// leaves it untouched rather than quarantining it aside.
	logPath := dispatch.HostLogDirFor(dir) + "/issue-42.log"
	if err := os.WriteFile(logPath, []byte("live run output\n"), 0o644); err != nil {
		t.Fatalf("seed issue-42.log: %v", err)
	}

	release, err := dispatch.ClaimIssue(dir, "42")
	if err != nil {
		t.Fatalf("dispatch.ClaimIssue: %v", err)
	}
	defer release()

	s := settle.NewFake()
	var stdout bytes.Buffer
	recErr := recoverByNumber(c, fc, fc, capsFor(fc, fc), dir, testFactory(t, dir, nil), s, "42", &stdout, io.Discard)
	out := stdout.String()

	if recErr != nil {
		t.Fatalf("recoverByNumber: got %v, want nil (skip, not an error)", recErr)
	}
	if !strings.Contains(out, "status=skipped") || !strings.Contains(out, "another launcher process still owns this issue") {
		t.Errorf("stdout = %q, want a skip line naming the live claim", out)
	}
	if len(s.SettleAdoptedCalls) != 0 {
		t.Errorf("SettleAdoptedCalls = %d, want 0 (a live claim must not be settled)", len(s.SettleAdoptedCalls))
	}
	if len(s.SettleRelayedBranchCalls) != 0 {
		t.Errorf("SettleRelayedBranchCalls = %d, want 0", len(s.SettleRelayedBranchCalls))
	}

	markerPath := dispatch.HostLogDirFor(dir) + "/issue-42.run-lineage"
	if _, statErr := os.Stat(markerPath); statErr == nil {
		t.Error("run-lineage marker created, want none while another process holds the claim")
	}

	got, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("issue-42.log missing after recoverByNumber: %v", readErr)
	}
	if string(got) != "live run output\n" {
		t.Errorf("issue-42.log contents = %q, want untouched", got)
	}
	if _, statErr := os.Stat(logPath + ".1"); statErr == nil {
		t.Error("issue-42.log.1 quarantine sibling created, want no rename while another process holds the claim")
	}

	iss, ierr := fc.Issue("42")
	if ierr != nil {
		t.Fatalf("fc.Issue(42): %v", ierr)
	}
	if !containsLabel(iss.Labels, c.inProgressLabel) {
		t.Errorf("issue #42 labels = %v, want still in-progress (unsettled)", iss.Labels)
	}
}

// issueReadCounter counts tracker Issue reads. The Gate's abort pass reads the
// issue to see whether its settle already wrote a terminal label, so a new read
// is the proof the watcher examined it.
type issueReadCounter struct {
	forge.IssueTracker
	reads atomic.Int64
}

func (r *issueReadCounter) Issue(num string) (forge.Issue, error) {
	r.reads.Add(1)
	return r.IssueTracker.Issue(num)
}

// failThenAbort stands in for a settle that writes InProgress->Failed itself
// (settle/ready.go: red CI after the fix passes), then aborts before
// gate.Leave and waits until the watcher's abort pass has read the issue.
func failThenAbort(it *issueReadCounter, num string, abort func()) {
	before := it.reads.Load()
	if err := it.TransitionState(num, forge.InProgress, forge.Failed); err != nil {
		panic(err)
	}
	abort()
	for deadline := time.Now().Add(5 * time.Second); it.reads.Load() == before; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			panic("timed out waiting for the watcher to examine " + num)
		}
	}
}

type failThenAbortAdopted struct {
	*settle.Fake
	it    *issueReadCounter
	abort func()
}

func (a *failThenAbortAdopted) SettleAdopted(d dispatch.Dispatcher, num string, gen uint64, prURL string) {
	failThenAbort(a.it, num, a.abort)
	a.Fake.SettleAdopted(d, num, gen, prURL)
}

// A hand-run recover the workflow claimed (issue wears only in-progress) whose
// own settle wrote agent-failed must not have that terminal label reclaimed
// when an abort lands before gate.Leave (issue #4888).
func TestRecoverByNumber_MidFlightAbort_OwnFailedSettleSpared(t *testing.T) {
	_, abort := withControlledSignals(t)

	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	fc.SetPR(fc.AgentBranch("42"), forge.PR{URL: testReconcilePR})

	dir := tempLogDir(t)
	rf := newKillHook(runner.NewFake())
	it := &issueReadCounter{IssueTracker: fc}
	s := &failThenAbortAdopted{Fake: settle.NewFake(), it: it, abort: abort}

	err := recoverByNumber(c, it, fc, capsFor(fc, fc), dir, testFactory(t, dir, rf), s, "42", io.Discard, io.Discard)

	if !errors.Is(err, waves.ErrSignalledStop) {
		t.Fatalf("recoverByNumber: got %v, want ErrSignalledStop", err)
	}
	iss, ierr := fc.Issue("42")
	if ierr != nil {
		t.Fatalf("fc.Issue(42): %v", ierr)
	}
	if !containsLabel(iss.Labels, c.failedLabel) || containsLabel(iss.Labels, c.label) {
		t.Errorf("issue #42 labels = %v, want %q kept and no dispatchable %q", iss.Labels, c.failedLabel, c.label)
	}
	select {
	case name := <-rf.killed:
		t.Errorf("reaper killed %q, want the settled issue spared", name)
	default:
	}
}
