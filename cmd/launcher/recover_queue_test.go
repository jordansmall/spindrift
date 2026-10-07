package main

import (
	"errors"
	"fmt"
	"io"
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
	"spindrift.dev/launcher/internal/seambundle"
	"spindrift.dev/launcher/internal/settle"
)

// queueRecoverFixture is a github-shaped tracker plus a working dir whose
// outbox and logs recoverQueueOne reads. The working dir is also the process's
// cwd, because the settler resolves the outbox through os.Getwd.
type queueRecoverFixture struct {
	c  config
	fc *forge.Fake
	// tracker is what recoverQueueOne drives: fc itself by default, or fc in
	// another adapter shape.
	tracker forge.IssueTracker
	cf      forge.CodeForge
	dir     string
}

func newQueueRecoverFixture(t *testing.T) *queueRecoverFixture {
	t.Helper()
	c := reconcileConfig()
	c.maxRecoverAttempts = 3
	fc := forge.NewFake(dispatchLabels(c))
	fc.BranchPrefix = c.branchPrefix
	fc.CreateDraftPRURL = testReconcilePR
	fc.SetCheckStates(testReconcilePR, []forge.RollupState{forge.StatePending, forge.StateSuccess, forge.StateSuccess})
	dir := tempLogDir(t)
	t.Chdir(dir)
	return &queueRecoverFixture{c: c, fc: fc, tracker: fc, cf: fc.AsGithubReadOnly(), dir: dir}
}

// addFailed seeds an agent-failed issue with a bundle and a self-report line.
// An empty report writes no log at all.
func (x *queueRecoverFixture) addFailed(t *testing.T, num, report string) {
	t.Helper()
	x.fc.SetIssue(forge.Issue{Number: num, Labels: []string{x.c.failedLabel}})
	outbox := dispatch.OutboxDirFor(x.dir, num)
	if err := os.MkdirAll(outbox, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outbox, seambundle.FileName), []byte("bundle"), 0o644); err != nil {
		t.Fatal(err)
	}
	if report == "" {
		return
	}
	// The marker a real Box run's Run() leaves, so recover's EnsureRunLineage
	// keeps this log rather than quarantining it as a prior run's.
	if err := os.WriteFile(filepath.Join(dispatch.HostLogDirFor(x.dir), "issue-"+num+".run-lineage"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dispatch.HostLogDirFor(x.dir), "issue-"+num+".log")
	if err := os.WriteFile(log, []byte("SPINDRIFT_OUTCOME: issue="+num+" landing="+x.fc.AgentBranch(num)+" status="+report+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (x *queueRecoverFixture) run(t *testing.T) (int, string) {
	t.Helper()
	return x.runWith(t, capsFor(x.tracker, x.cf))
}

func (x *queueRecoverFixture) runWith(t *testing.T, caps forge.Capabilities) (int, string) {
	t.Helper()
	var out strings.Builder
	err := recoverQueueOne(x.c, x.tracker, x.cf, caps, x.dir, testFactory(t, x.dir, nil), newWorkSettle(x.c, x.tracker, testWired(x.tracker), x.cf), &out, io.Discard)
	return exitCodeFor(err), out.String()
}

func (x *queueRecoverFixture) labels(t *testing.T, num string) []string {
	t.Helper()
	iss, err := x.fc.Issue(num)
	if err != nil {
		t.Fatalf("issue %s: %v", num, err)
	}
	return iss.Labels
}

func (x *queueRecoverFixture) assertUntouched(t *testing.T) {
	t.Helper()
	if len(x.fc.TransitionStateCalls) != 0 || len(x.fc.CommentCalls) != 0 {
		t.Errorf("tracker written: transitions=%v comments=%v", x.fc.TransitionStateCalls, x.fc.CommentCalls)
	}
}

// seedRecord saves seed as num's attempt record; an empty seed.Bundle means the
// issue's current outbox bundle.
func (x *queueRecoverFixture) seedRecord(t *testing.T, num string, seed recoverAttempts) {
	t.Helper()
	bundle := seed.Bundle
	if bundle == "" {
		id, err := recoverBundleID(x.dir, num)
		if err != nil {
			t.Fatal(err)
		}
		bundle = id
	}
	rec := loadRecoverAttempts(x.dir, num, bundle)
	rec.Count, rec.Last, rec.GaveUp = seed.Count, seed.Last, seed.GaveUp
	if err := rec.save(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverQueueOne_ReadyBundleNoPRMergesAndCompletes(t *testing.T) {
	shapes := []struct {
		name    string
		tracker func(*forge.Fake) forge.IssueTracker
	}{
		{"github", func(fc *forge.Fake) forge.IssueTracker { return fc }},
		{"forgejo", func(fc *forge.Fake) forge.IssueTracker { return fc.AsForgejoShaped() }},
	}
	for _, shape := range shapes {
		t.Run(shape.name, func(t *testing.T) {
			x := newQueueRecoverFixture(t)
			x.tracker = shape.tracker(x.fc)
			x.addFailed(t, "42", "ready")

			code, _ := x.run(t)

			if code != 0 {
				t.Errorf("exit = %d, want 0", code)
			}
			if x.fc.Merged != testReconcilePR {
				t.Errorf("Merged = %q, want %q", x.fc.Merged, testReconcilePR)
			}
			got := x.labels(t, "42")
			if !slices.Contains(got, x.c.completeLabel) || slices.Contains(got, x.c.failedLabel) || slices.Contains(got, x.c.inProgressLabel) {
				t.Errorf("labels = %v, want %q only of the lifecycle labels", got, x.c.completeLabel)
			}
			if len(x.fc.TransitionStateCalls) == 0 {
				t.Fatalf("no tracker transitions recorded, want a 42 Failed->InProgress claim first")
			}
			first := x.fc.TransitionStateCalls[0]
			if first.Num != "42" || first.From != forge.Failed || first.To != forge.InProgress {
				t.Errorf("first transition = %+v, want 42 Failed->InProgress", first)
			}
		})
	}
}

func TestRecoverQueueOne_IneligibleIssuesAreLeftUntouched(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, x *queueRecoverFixture)
	}{
		{"blocked self-report", func(t *testing.T, x *queueRecoverFixture) { x.addFailed(t, "42", "blocked") }},
		{"no self-report", func(t *testing.T, x *queueRecoverFixture) { x.addFailed(t, "42", "") }},
		{"open PR", func(t *testing.T, x *queueRecoverFixture) {
			x.addFailed(t, "42", "ready")
			x.fc.SetPR(x.fc.AgentBranch("42"), forge.PR{URL: testReconcilePR})
		}},
		{"no bundle", func(t *testing.T, x *queueRecoverFixture) {
			x.addFailed(t, "42", "ready")
			if err := os.Remove(filepath.Join(dispatch.OutboxDirFor(x.dir, "42"), seambundle.FileName)); err != nil {
				t.Fatal(err)
			}
		}},
		{"not labelled agent-failed", func(t *testing.T, x *queueRecoverFixture) {
			x.addFailed(t, "42", "ready")
			x.fc.SetIssue(forge.Issue{Number: "42", Labels: []string{x.c.completeLabel}})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := newQueueRecoverFixture(t)
			tc.setup(t, x)

			code, _ := x.run(t)

			if code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			x.assertUntouched(t)
			if x.fc.Merged != "" {
				t.Errorf("Merged = %q, want none", x.fc.Merged)
			}
		})
	}
}

func TestRecoverQueueOne_HeldHostClaimIsSkipped(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	release, err := dispatch.ClaimIssue(x.dir, "42")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	code, out := x.run(t)

	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(out, "status=skipped") {
		t.Errorf("output = %q, want a skipped line", out)
	}
	x.assertUntouched(t)
}

func TestRecoverQueueOne_RelayFailureEndsAgentFailed(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.fc.RelayBundleErr = errors.New("relay: boom")

	code, _ := x.run(t)

	if code != 0 {
		t.Errorf("exit = %d, want 0 (an issue was attempted)", code)
	}
	if len(x.fc.CommentCalls) != 0 {
		t.Errorf("comments = %v, want none for an intermediate failure", x.fc.CommentCalls)
	}
	got := x.labels(t, "42")
	if !slices.Contains(got, x.c.failedLabel) || slices.Contains(got, x.c.inProgressLabel) || slices.Contains(got, x.c.completeLabel) {
		t.Errorf("labels = %v, want %q only of the lifecycle labels", got, x.c.failedLabel)
	}
}

func TestRecoverQueueOne_StopSignalExitsSevenWithoutParking(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	withClosedStopSignal(t)

	code, _ := x.run(t)

	if code != exitSignalledStop {
		t.Errorf("exit = %d, want %d", code, exitSignalledStop)
	}
	x.assertUntouched(t)
	if got := x.labels(t, "42"); !slices.Contains(got, x.c.failedLabel) {
		t.Errorf("labels = %v, want agent-failed kept", got)
	}
}

func TestRecoverQueueOne_AttemptsOnlyOneEligibleIssue(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "41", "ready")
	x.addFailed(t, "42", "ready")

	code, _ := x.run(t)

	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	var started []string
	for _, call := range x.fc.TransitionStateCalls {
		if call.From == forge.Failed && call.To == forge.InProgress {
			started = append(started, call.Num)
		}
	}
	if len(started) != 1 {
		t.Errorf("issues claimed = %v, want exactly one", started)
	}
}

func TestRecoverQueueOne_PRLessForgeRefuses(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	local := x.fc.AsLocal()

	code, _ := x.runWith(t, capsFor(x.fc, local))

	if code != 1 {
		t.Errorf("exit = %d, want 1 (a refusal, not an empty queue)", code)
	}
	x.assertUntouched(t)
}

// withControlledStop stubs the signal install with a stop channel the test
// closes mid-run, so a stop can land at an exact seam rather than before the
// first checkpoint. The returned func is safe to call more than once.
func withControlledStop(t *testing.T) (stop func()) {
	t.Helper()
	stop, _ = withControlledSignals(t)
	return stop
}

// withControlledSignals is withControlledStop plus the second-signal abort
// func, equally safe to call more than once.
func withControlledSignals(t *testing.T) (stop, abort func()) {
	t.Helper()
	stopCh := make(chan struct{})
	abortCh := make(chan struct{})
	orig := installStopSignal
	installStopSignal = func() (<-chan struct{}, <-chan struct{}, func()) {
		return stopCh, abortCh, func() {}
	}
	t.Cleanup(func() { installStopSignal = orig })
	var stopOnce, abortOnce sync.Once
	return func() { stopOnce.Do(func() { close(stopCh) }) },
		func() { abortOnce.Do(func() { close(abortCh) }) }
}

// stopOnPRLookup stops the run from inside the PR lookup. It embeds PRForge as
// well because forge.ResolveOpenPR finds it by type assertion on the CodeForge,
// which a bare embedded CodeForge would not satisfy.
type stopOnPRLookup struct {
	forge.CodeForge
	forge.PRForge
	stop func()
}

func (w stopOnPRLookup) OpenPRForBranch(branch string) (forge.PR, bool, error) {
	w.stop()
	return w.PRForge.OpenPRForBranch(branch)
}

// stopAfterClaim stops the run right after a Failed->InProgress transition.
type stopAfterClaim struct {
	forge.IssueTracker
	stop func()
}

func (w stopAfterClaim) TransitionState(num string, from, to forge.DispatchState) error {
	err := w.IssueTracker.TransitionState(num, from, to)
	if from == forge.Failed && to == forge.InProgress {
		w.stop()
	}
	return err
}

// failRestore fails the InProgress->Failed transition (restore or park) and
// passes every other transition through, so the claim still succeeds.
type failRestore struct {
	forge.IssueTracker
}

func (w failRestore) TransitionState(num string, from, to forge.DispatchState) error {
	if from == forge.InProgress && to == forge.Failed {
		return errors.New("transition: boom")
	}
	return w.IssueTracker.TransitionState(num, from, to)
}

func (x *queueRecoverFixture) runOn(t *testing.T, it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities) int {
	t.Helper()
	return x.runOnSettler(t, it, cf, caps, newWorkSettle(x.c, x.tracker, testWired(x.tracker), x.cf))
}

func (x *queueRecoverFixture) runOnSettler(t *testing.T, it forge.IssueTracker, cf forge.CodeForge, caps forge.Capabilities, s settle.WorkSettler) int {
	t.Helper()
	err := recoverQueueOne(x.c, it, cf, caps, x.dir, testFactory(t, x.dir, nil), s, io.Discard, io.Discard)
	return exitCodeFor(err)
}

// stopInSettle drain-stops the run from inside the settle, then settles as
// normal, so the stop lands after the settle began but before its verdict.
type stopInSettle struct {
	settle.WorkSettler
	stop func()
}

func (w stopInSettle) SettleRelayedBranch(d dispatch.Dispatcher, num string, gen uint64, sit settle.Situation, result dispatch.Result) bool {
	w.stop()
	return w.WorkSettler.SettleRelayedBranch(d, num, gen, sit, result)
}

// A drain stop does not abandon the settle, so a settle that then fails to
// land must still park the issue and count the attempt before the stop exit
// (#4679); otherwise it is stranded on in-progress.
func TestRecoverQueueOne_StopDuringFailedSettleParksAndExitsSeven(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.fc.RelayBundleErr = errors.New("relay: boom")
	stop := withControlledStop(t)
	s := stopInSettle{WorkSettler: newWorkSettle(x.c, x.tracker, testWired(x.tracker), x.cf), stop: stop}

	code := x.runOnSettler(t, x.fc, x.cf, capsFor(x.tracker, x.cf), s)

	if code != exitSignalledStop {
		t.Errorf("exit = %d, want %d", code, exitSignalledStop)
	}
	got := x.labels(t, "42")
	if !slices.Contains(got, x.c.failedLabel) || slices.Contains(got, x.c.inProgressLabel) {
		t.Errorf("labels = %v, want %q and not %q", got, x.c.failedLabel, x.c.inProgressLabel)
	}
	id, err := recoverBundleID(x.dir, "42")
	if err != nil {
		t.Fatal(err)
	}
	if rec := loadRecoverAttempts(x.dir, "42", id); rec.Count != 1 {
		t.Errorf("attempt count = %d, want 1", rec.Count)
	}
}

// abortInSettle aborts from inside the settle and waits for the watcher's
// reclaim to start, so the abort provably lands while the issue is in flight.
type abortInSettle struct {
	settle.WorkSettler
	abort  func()
	killed <-chan string
}

func (w abortInSettle) SettleRelayedBranch(d dispatch.Dispatcher, num string, gen uint64, sit settle.Situation, result dispatch.Result) bool {
	w.abort()
	select {
	case <-w.killed:
	case <-time.After(5 * time.Second):
		panic("timed out waiting for the watcher to reclaim " + num)
	}
	return w.WorkSettler.SettleRelayedBranch(d, num, gen, sit, result)
}

// An abort abandons the settle and reclaims the issue to dispatchable, so a
// settle that fails under it must not also park the issue or count an attempt.
func TestRecoverQueueOne_AbortDuringFailedSettleReclaimsAndDoesNotPark(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.fc.RelayBundleErr = errors.New("relay: boom")
	_, abort := withControlledSignals(t)
	rf := newKillHook(runner.NewFake())
	s := abortInSettle{WorkSettler: newWorkSettle(x.c, x.tracker, testWired(x.tracker), x.cf), abort: abort, killed: rf.killed}

	err := recoverQueueOne(x.c, x.fc, x.cf, capsFor(x.tracker, x.cf), x.dir, testFactory(t, x.dir, rf), s, io.Discard, io.Discard)
	code := exitCodeFor(err)

	if code != exitSignalledStop {
		t.Errorf("exit = %d, want %d", code, exitSignalledStop)
	}
	got := x.labels(t, "42")
	if !slices.Contains(got, x.c.label) {
		t.Errorf("labels = %v, want dispatchable %q", got, x.c.label)
	}
	for _, unwanted := range []string{x.c.failedLabel, x.c.inProgressLabel} {
		if slices.Contains(got, unwanted) {
			t.Errorf("labels = %v, want no %q", got, unwanted)
		}
	}
	id, err := recoverBundleID(x.dir, "42")
	if err != nil {
		t.Fatal(err)
	}
	if rec := loadRecoverAttempts(x.dir, "42", id); rec.Count != 0 {
		t.Errorf("attempt count = %d, want 0", rec.Count)
	}
}

func TestRecoverQueueOne_StopDuringLandingSettleRemovesRecordAndExitsSeven(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.seedRecord(t, "42", recoverAttempts{Count: 1, Last: time.Now().Add(-time.Hour)})
	stop := withControlledStop(t)
	s := stopInSettle{WorkSettler: newWorkSettle(x.c, x.tracker, testWired(x.tracker), x.cf), stop: stop}

	code := x.runOnSettler(t, x.fc, x.cf, capsFor(x.tracker, x.cf), s)

	if code != exitSignalledStop {
		t.Errorf("exit = %d, want %d", code, exitSignalledStop)
	}
	if _, err := os.Stat(recoverAttemptsPath(x.dir, "42")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("record stat err = %v, want not-exist", err)
	}
}

// A stop landing during the eligibility reads is only seen by the one handler
// installed ahead of them, so the run must still end in a stop, not a landing.
func TestRecoverQueueOne_StopDuringEligibilityReadsExitsSeven(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	stop := withControlledStop(t)
	caps := capsFor(x.tracker, x.cf)

	code := x.runOn(t, x.fc, stopOnPRLookup{CodeForge: x.cf, PRForge: x.cf.(forge.PRForge), stop: stop}, caps)

	if code != exitSignalledStop {
		t.Errorf("exit = %d, want %d", code, exitSignalledStop)
	}
	x.assertUntouched(t)
	if x.fc.Merged != "" {
		t.Errorf("Merged = %q, want none", x.fc.Merged)
	}
	if got := x.labels(t, "42"); !slices.Contains(got, x.c.failedLabel) {
		t.Errorf("labels = %v, want agent-failed kept", got)
	}
}

func TestRecoverQueueOne_StopAfterClaimRestoresAgentFailed(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	stop := withControlledStop(t)

	code := x.runOn(t, stopAfterClaim{IssueTracker: x.fc, stop: stop}, x.cf, capsFor(x.tracker, x.cf))

	if code != exitSignalledStop {
		t.Errorf("exit = %d, want %d", code, exitSignalledStop)
	}
	if x.fc.Merged != "" {
		t.Errorf("Merged = %q, want none", x.fc.Merged)
	}
	if len(x.fc.CommentCalls) != 0 {
		t.Errorf("comments = %v, want none", x.fc.CommentCalls)
	}
	got := x.labels(t, "42")
	if !slices.Contains(got, x.c.failedLabel) || slices.Contains(got, x.c.inProgressLabel) {
		t.Errorf("labels = %v, want agent-failed and not agent-in-progress", got)
	}
}

// A stop after the claim whose label restore also fails must not exit 7: the
// issue is stranded on agent-in-progress, which queue mode never revisits.
func TestRecoverQueueOne_StopAfterClaimFailedRestoreExitsOne(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	stop := withControlledStop(t)
	id, err := recoverBundleID(x.dir, "42")
	if err != nil {
		t.Fatal(err)
	}

	it := failRestore{IssueTracker: stopAfterClaim{IssueTracker: x.fc, stop: stop}}
	code := x.runOn(t, it, x.cf, capsFor(x.tracker, x.cf))

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	got := x.labels(t, "42")
	if !slices.Contains(got, x.c.inProgressLabel) || slices.Contains(got, x.c.failedLabel) {
		t.Errorf("labels = %v, want agent-in-progress and not agent-failed", got)
	}
	if rec := loadRecoverAttempts(x.dir, "42", id); rec.Count != 0 {
		t.Errorf("attempt count = %d, want 0", rec.Count)
	}
}

// A landing failure whose park transition fails exits 1, not 0, but the
// attempt record is saved first so the next pass still backs off.
func TestRecoverQueueOne_FailedParkExitsOneAndKeepsRecord(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.fc.RelayBundleErr = errors.New("relay: boom")
	id, err := recoverBundleID(x.dir, "42")
	if err != nil {
		t.Fatal(err)
	}

	code := x.runOn(t, failRestore{IssueTracker: x.fc}, x.cf, capsFor(x.tracker, x.cf))

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if rec := loadRecoverAttempts(x.dir, "42", id); rec.Count != 1 {
		t.Errorf("attempt count = %d, want 1", rec.Count)
	}
}

func TestRecoverQueueOne_LostClaimRaceIsSkipped(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.fc.TransitionStateErr = forge.ErrAlreadyClaimed

	code, out := x.run(t)

	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(out, "status=skipped") {
		t.Errorf("output = %q, want a skipped line", out)
	}
	if x.fc.Merged != "" {
		t.Errorf("Merged = %q, want none", x.fc.Merged)
	}
}

// A lookup outage must surface, not read as an empty queue. Queue mode has
// claimed nothing before these reads, so a stale prior Complete state must not
// let the failure path write the tracker or mark the issue complete.
func TestRecoverQueueOne_PreClaimErrorFailsWithoutTransition(t *testing.T) {
	cases := []struct {
		name   string
		inject func(*testing.T, *queueRecoverFixture)
	}{
		{"issue lookup", func(_ *testing.T, x *queueRecoverFixture) { x.fc.IssueErr = errors.New("forge: boom") }},
		{"PR resolve", func(_ *testing.T, x *queueRecoverFixture) { x.fc.OpenPRForBranchErr = errors.New("forge: boom") }},
		{"host claim", func(t *testing.T, x *queueRecoverFixture) {
			// A directory at the lock path makes ClaimIssue's OpenFile fail with
			// EISDIR, an error other than ErrIssueClaimed.
			if err := os.MkdirAll(filepath.Join(dispatch.HostLogDirFor(x.dir), "issue-42.lock"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := newQueueRecoverFixture(t)
			x.addFailed(t, "42", "ready")
			x.fc.PriorClaimStates = map[string]forge.DispatchState{"42": forge.Complete}
			tc.inject(t, x)

			code, _ := x.run(t)

			if code != 1 {
				t.Errorf("exit = %d, want 1", code)
			}
			x.assertUntouched(t)
			x.fc.IssueErr = nil // labels() Fatalf's on a lookup error; clear the issue-lookup case's injected error
			got := x.labels(t, "42")
			if !slices.Contains(got, x.c.failedLabel) || slices.Contains(got, x.c.completeLabel) {
				t.Errorf("labels = %v, want agent-failed and not agent-complete", got)
			}
		})
	}
}

func queueLaunchContext(t *testing.T, x *queueRecoverFixture, cleaned *bool) *launchContext {
	t.Helper()
	return &launchContext{
		config:       x.c,
		pwd:          x.dir,
		issueTracker: x.tracker,
		codeForge:    x.cf,
		capabilities: capsFor(x.tracker, x.cf),
		factory:      testFactory(t, x.dir, nil),
		settle:       testNewSettle(x.c, x.tracker, testWired(x.tracker), x.cf),
		cleanup:      func() { *cleaned = true },
	}
}

// cmdRecoverQueue maps recoverQueueOne's verdicts onto exit codes (0
// attempted, 2 none eligible, 1 refusal) and runs cleanup on each; an empty
// queue is a verdict, so it prints nothing to stderr.
func TestCmdRecoverQueue_ExitCodesAndCleanup(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(*testing.T, *queueRecoverFixture) *launchContext
		want       int
		wantStderr string
	}{
		{"attempted", func(t *testing.T, x *queueRecoverFixture) *launchContext {
			x.addFailed(t, "42", "ready")
			return nil
		}, 0, ""},
		{"nothing eligible", func(t *testing.T, x *queueRecoverFixture) *launchContext { return nil }, 2, ""},
		{"no PR forge", func(t *testing.T, x *queueRecoverFixture) *launchContext {
			x.addFailed(t, "42", "ready")
			lc := queueLaunchContext(t, x, new(bool))
			lc.capabilities = forge.Capabilities{}
			return lc
		}, 1, "PR-shaped Code Forge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := newQueueRecoverFixture(t)
			var cleaned bool
			lc := tc.setup(t, x)
			if lc == nil {
				lc = queueLaunchContext(t, x, &cleaned)
			} else {
				lc.cleanup = func() { cleaned = true }
			}
			var stdout, stderr strings.Builder
			if got := cmdRecoverQueue(lc, &stdout, &stderr); got != tc.want {
				t.Errorf("cmdRecoverQueue = %d, want %d (stderr %q)", got, tc.want, stderr.String())
			}
			if tc.wantStderr == "" {
				if stderr.Len() != 0 {
					t.Errorf("stderr = %q, want empty", stderr.String())
				}
			} else if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want %q", stderr.String(), tc.wantStderr)
			}
			if !cleaned {
				t.Error("cmdRecoverQueue did not run lc.cleanup()")
			}
		})
	}
}

func TestRecoverQueueOne_InsideBackoffWindowIsSkipped(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.c.transientBackoffSecs = 3600
	x.addFailed(t, "42", "ready")
	x.seedRecord(t, "42", recoverAttempts{Count: 1, Last: time.Now()})

	code, out := x.run(t)

	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(out, "status=skipped") {
		t.Errorf("output = %q, want a skipped line", out)
	}
	x.assertUntouched(t)
	if x.fc.Merged != "" {
		t.Errorf("Merged = %q, want none", x.fc.Merged)
	}
}

func TestRecoverQueueOne_OutsideBackoffWindowIsAttempted(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.c.transientBackoffSecs = 3600
	x.addFailed(t, "42", "ready")
	x.seedRecord(t, "42", recoverAttempts{Count: 1, Last: time.Now().Add(-24 * time.Hour)})

	code, _ := x.run(t)

	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if x.fc.Merged == "" {
		t.Error("issue not merged")
	}
}

func TestRecoverQueueOne_GivesUpAfterBoundWithOneComment(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.fc.RelayBundleErr = errors.New("relay: boom")

	for run := 1; run <= 3; run++ {
		if code, _ := x.run(t); code != 0 {
			t.Fatalf("run %d: exit = %d, want 0", run, code)
		}
		want := 0
		if run == 3 {
			want = 1
		}
		if len(x.fc.CommentCalls) != want {
			t.Fatalf("after run %d: comments = %v, want %d", run, x.fc.CommentCalls, want)
		}
	}
	body := fmt.Sprint(x.fc.CommentCalls[0])
	for _, sub := range []string{"Auto-recover gave up", "3 attempts", "spindrift recover 42"} {
		if !strings.Contains(body, sub) {
			t.Errorf("comment %q missing %q", body, sub)
		}
	}

	transitions := len(x.fc.TransitionStateCalls)
	code, out := x.run(t)

	if code != 2 {
		t.Errorf("4th run exit = %d, want 2", code)
	}
	if strings.Contains(out, "#42") {
		t.Errorf("output = %q, want no per-issue line once the give-up comment is posted", out)
	}
	if len(x.fc.CommentCalls) != 1 || len(x.fc.TransitionStateCalls) != transitions {
		t.Errorf("4th run wrote the tracker: comments=%v transitions=%v", x.fc.CommentCalls, x.fc.TransitionStateCalls[transitions:])
	}
}

// The Last stamp is what keeps a failed bundle out of the very next pass: with
// a long backoff, the second run must not touch the issue at all.
func TestRecoverQueueOne_FailedAttemptBacksOffTheNextRun(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.c.transientBackoffSecs = 3600
	x.addFailed(t, "42", "ready")
	x.fc.RelayBundleErr = errors.New("relay: boom")

	if code, _ := x.run(t); code != 0 {
		t.Fatalf("first run exit = %d, want 0", code)
	}
	transitions := len(x.fc.TransitionStateCalls)

	code, _ := x.run(t)

	if code != 2 {
		t.Errorf("second run exit = %d, want 2", code)
	}
	if len(x.fc.TransitionStateCalls) != transitions || len(x.fc.CommentCalls) != 0 {
		t.Errorf("second run wrote the tracker: transitions=%v comments=%v", x.fc.TransitionStateCalls[transitions:], x.fc.CommentCalls)
	}
	if x.fc.Merged != "" {
		t.Errorf("Merged = %q, want none", x.fc.Merged)
	}
}

// A give-up comment that fails to post is retried by the next pass, once, and
// never again after it lands.
func TestRecoverQueueOne_GiveUpCommentFailureIsRetriedOnce(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.fc.RelayBundleErr = errors.New("relay: boom")
	x.fc.CommentErr = errors.New("comment: boom")

	for run := 1; run <= 3; run++ {
		if code, _ := x.run(t); code != 0 {
			t.Fatalf("run %d: exit = %d, want 0", run, code)
		}
	}
	if len(x.fc.CommentCalls) != 1 {
		t.Fatalf("comments after failing give-up = %d, want 1 attempt", len(x.fc.CommentCalls))
	}
	x.fc.CommentErr = nil
	transitions := len(x.fc.TransitionStateCalls)

	if code, _ := x.run(t); code != 2 {
		t.Errorf("retry run exit = %d, want 2", code)
	}
	if len(x.fc.CommentCalls) != 2 {
		t.Errorf("comments after retry = %d, want 2", len(x.fc.CommentCalls))
	}
	if code, out := x.run(t); code != 2 || strings.Contains(out, "#42") {
		t.Errorf("further run = %d %q, want 2 and no per-issue line", code, out)
	}
	if len(x.fc.CommentCalls) != 2 || len(x.fc.TransitionStateCalls) != transitions {
		t.Errorf("further runs wrote the tracker: comments=%d transitions=%v", len(x.fc.CommentCalls), x.fc.TransitionStateCalls[transitions:])
	}
}

// An operator lowering MAX_RECOVER_ATTEMPTS below an existing count must still
// get the give-up comment, with no claim.
func TestRecoverQueueOne_RecordPastBoundGetsOneComment(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.seedRecord(t, "42", recoverAttempts{Count: 5, Last: time.Now().Add(-time.Hour)})

	for run := 1; run <= 2; run++ {
		if code, _ := x.run(t); code != 2 {
			t.Fatalf("run %d: exit = %d, want 2", run, code)
		}
	}

	if len(x.fc.CommentCalls) != 1 {
		t.Errorf("comments = %v, want exactly one", x.fc.CommentCalls)
	}
	if len(x.fc.TransitionStateCalls) != 0 {
		t.Errorf("transitions = %v, want none", x.fc.TransitionStateCalls)
	}
}

// An unwritable record must not strand the issue on agent-in-progress: it is
// parked first and the save failure is reported after.
func TestRecoverQueueOne_UnsavableRecordStillParksAndFails(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.fc.RelayBundleErr = errors.New("relay: boom")
	if err := os.Mkdir(recoverAttemptsPath(x.dir, "42"), 0o755); err != nil {
		t.Fatal(err)
	}

	code, _ := x.run(t)

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	calls := x.fc.TransitionStateCalls
	if len(calls) == 0 {
		t.Fatal("no transitions recorded")
	}
	if last := calls[len(calls)-1]; last.From != forge.InProgress || last.To != forge.Failed {
		t.Errorf("last transition = %+v, want InProgress->Failed", last)
	}
}

// A gave-up record stays ineligible for its bundle even when the bound is
// raised above its count: the give-up is final, not a function of the bound.
func TestRecoverQueueOne_GaveUpRecordStaysIneligibleAfterRaisedBound(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.fc.RelayBundleErr = errors.New("relay: boom")
	x.seedRecord(t, "42", recoverAttempts{Count: 3, Last: time.Now().Add(-time.Hour), GaveUp: true})
	x.c.maxRecoverAttempts = 4

	if code, _ := x.run(t); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}

	if len(x.fc.CommentCalls) != 0 {
		t.Errorf("comments = %v, want none", x.fc.CommentCalls)
	}
	if len(x.fc.TransitionStateCalls) != 0 {
		t.Errorf("transitions = %v, want none", x.fc.TransitionStateCalls)
	}
}

// A record at the bound whose give-up cannot be saved exits 1 after posting
// the comment once. The record stays readable so load still sees the count.
func TestRecoverQueueOne_GiveUpSaveFailureExitsOne(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only mode this test relies on")
	}
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.seedRecord(t, "42", recoverAttempts{Count: x.c.maxRecoverAttempts, Last: time.Now().Add(-time.Hour)})
	path := recoverAttemptsPath(x.dir, "42")
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	code, _ := x.run(t)

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if len(x.fc.CommentCalls) != 1 {
		t.Errorf("comments = %v, want exactly one", x.fc.CommentCalls)
	}
}

func TestRecoverQueueOne_NewBundleResetsTheCount(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.seedRecord(t, "42", recoverAttempts{Count: 3, Last: time.Now(), Bundle: "sha-of-an-older-bundle"})

	code, _ := x.run(t)

	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if x.fc.Merged == "" {
		t.Error("issue not merged")
	}
}

func TestRecoverQueueOne_SuccessRemovesRecord(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.seedRecord(t, "42", recoverAttempts{Count: 1, Last: time.Now().Add(-time.Hour)})

	if code, out := x.run(t); code != 0 {
		t.Log(out)
		t.Fatalf("exit = %d, want 0", code)
	}
	if _, err := os.Stat(recoverAttemptsPath(x.dir, "42")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("record stat err = %v, want not-exist", err)
	}
}

func TestRecoverByNumber_IgnoresAttemptRecord(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.c.transientBackoffSecs = 3600
	x.seedRecord(t, "42", recoverAttempts{Count: 3, Last: time.Now()})
	before, err := os.ReadFile(recoverAttemptsPath(x.dir, "42"))
	if err != nil {
		t.Fatal(err)
	}
	x.fc.SetIssue(forge.Issue{Number: "42", Labels: []string{x.c.inProgressLabel}})

	err = recoverByNumber(x.c, x.tracker, x.cf, capsFor(x.tracker, x.cf), x.dir, testFactory(t, x.dir, nil), newWorkSettle(x.c, x.tracker, testWired(x.tracker), x.cf), "42", io.Discard, io.Discard)

	if err != nil {
		t.Fatalf("recoverByNumber: %v", err)
	}
	if x.fc.Merged == "" {
		t.Error("issue not merged")
	}
	after, err := os.ReadFile(recoverAttemptsPath(x.dir, "42"))
	if err != nil || string(after) != string(before) {
		t.Errorf("record changed: err=%v before=%q after=%q", err, before, after)
	}
}
