// This file drives Dispatch's one-shot wave through the same cfg.Stop/
// cfg.Abort seam continuous_test.go uses for RunContinuous (#3522), so there
// is one way to exercise operator shutdown in tests rather than two. It
// reuses continuous_test.go's killHook and waitOn helpers.
package waves

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/terminate"
	"spindrift.dev/launcher/internal/testutil"
)

// TestDispatch_StopPreClosed_NoLaunchesReturnsErrSignalledStop pins the first
// stage of the two-stage latch: a Stop already closed before Dispatch ever
// runs its wave means no Box launches at all, and the caller learns why
// through ErrSignalledStop rather than a bare nil or ErrOpenNoneDispatchable.
func TestDispatch_StopPreClosed_NoLaunchesReturnsErrSignalledStop(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	stop := make(chan struct{})
	close(stop)
	c.Stop = stop

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	in := Input{Origin: OriginDiscovered, Batch: Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}}
	err := Dispatch(c, nil, fc, fc, dir, f, s, in, claimer)

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("Dispatch: got %v, want ErrSignalledStop", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %d, want 0 (a pre-closed Stop must launch nothing)", len(fr.RunCalls))
	}
}

// TestDispatch_StopClosedMidWave_FinishesInFlightNoFurtherLaunch pins the
// drain half of the first stage: a Box already running when Stop closes
// still runs to completion and settles, but the slot it frees never launches
// another issue. MaxParallel=1 forces issue #2's Acquire to block until #1's
// Release, so #2's own Allowed check is guaranteed to run only after Stop
// is already closed -- no sleep needed to make the race deterministic.
func TestDispatch_StopClosedMidWave_FinishesInFlightNoFurtherLaunch(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	stop := make(chan struct{})
	c.Stop = stop

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	// MaxParallel=1 admits only one of #1/#2's goroutines to Run at a time, but
	// which one wins the race to acquire that single slot is not deterministic
	// (both goroutines start concurrently), so the gate below is keyed on
	// whichever issue actually starts rather than assuming it is #1.
	started := make(chan string, 1)
	release := make(chan struct{})
	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error {
		started <- box.Issue
		<-release
		return nil
	}

	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	in := Input{Origin: OriginDiscovered, Batch: Batch{Issues: []Issue{{Number: "1", Title: "first"}, {Number: "2", Title: "second"}}}}
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- Dispatch(c, nil, fc, fc, dir, f, s, in, claimer)
	}()

	first := waitOn(t, started, "neither issue was ever dispatched")
	close(stop)
	close(release)

	err := waitOn(t, resultCh, "Dispatch did not return")
	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("Dispatch: got %v, want ErrSignalledStop", err)
	}
	// The second slot never frees until the first Run returns, by which point
	// stop is already closed, so exactly one issue -- whichever won the race
	// above -- ever launches.
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != first {
		t.Fatalf("RunCalls: got %v, want exactly issue %s (no launch after stop)", fr.RunCalls, first)
	}
	if len(s.SettleCalls) != 1 || s.SettleCalls[0].Num != first {
		t.Fatalf("SettleCalls: got %+v, want exactly one settle of issue %s (in-flight work still settles normally)", s.SettleCalls, first)
	}
}

// TestDispatch_AbortPreClosed_NoLaunchesReturnsErrSignalledStop mirrors the
// Stop-preclosed case for the second stage: Abort alone, with no prior Stop,
// still gates every launch and reports ErrSignalledStop.
func TestDispatch_AbortPreClosed_NoLaunchesReturnsErrSignalledStop(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	abort := make(chan struct{})
	close(abort)
	c.Abort = abort

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	in := Input{Origin: OriginDiscovered, Batch: Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}}
	err := Dispatch(c, nil, fc, fc, dir, f, s, in, claimer)

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("Dispatch: got %v, want ErrSignalledStop", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %d, want 0 (a pre-closed Abort must launch nothing)", len(fr.RunCalls))
	}
}

// TestDispatch_ClaimedBlocked_StopPreClosed_ReturnsErrSignalledStopAndWritesMarker
// pins the reviewer's exact BLOCKING finding on #3522: OriginClaimed's own
// blocked-issue return inside drainMaxJobs is a bare nil, so it never reached
// dispatchWave's Gate at all. With #1 claimed and blocked by open #3, and Stop
// already closed before Dispatch runs, Dispatch must still report
// ErrSignalledStop -- but the blocked marker must still land first, since the
// release pipeline needs it to free the claim regardless of why the wave
// never launched.
func TestDispatch_ClaimedBlocked_StopPreClosed_ReturnsErrSignalledStopAndWritesMarker(t *testing.T) {
	c := baseConfig()
	c.MaxParallel = 1
	stop := make(chan struct{})
	close(stop)
	c.Stop = stop

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{testInProgressLabel}})
	fc.SetIssue(forge.Issue{Number: "3", State: "OPEN"})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, "agent-trigger", testInProgressLabel)

	edges := map[string][]string{"1": {"3"}}
	sources := Sources{"1": {"3": forge.DepSourceNative}}
	in := Input{Origin: OriginClaimed, Batch: Batch{
		Issues:  []Issue{{Number: "1", Title: "claimed issue"}},
		Edges:   edges,
		Sources: sources,
	}}

	err := Dispatch(c, nil, fc, fc, dir, f, s, in, claimer)

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("Dispatch: got %v, want ErrSignalledStop", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %d, want 0 (a pre-closed Stop must launch nothing)", len(fr.RunCalls))
	}
	b, readErr := os.ReadFile(filepath.Join(dir, ".spindrift", "logs", blockedMarker))
	if readErr != nil {
		t.Fatalf("reading blocked marker: %v", readErr)
	}
	if got := string(b); got != "#3 (native)" {
		t.Errorf("blocked marker = %q, want %q", got, "#3 (native)")
	}
}

// TestDispatch_SelectiveAllBlocked_StopPreClosed_ReturnsErrSignalledStop is
// the companion for the other pre-wave nil-adjacent return drainMaxJobs can
// take: OriginSelective with everything held (here, overlap-deferred) returns
// ErrOpenNoneDispatchable, a non-nil error but one dispatchWave's Gate never
// observes either, since it is never called. Stop pre-closed must still win.
func TestDispatch_SelectiveAllBlocked_StopPreClosed_ReturnsErrSignalledStop(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	c.OverlapGate = "defer"
	stop := make(chan struct{})
	close(stop)
	c.Stop = stop

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{label},
	})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{testInProgressLabel},
	})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	in := Input{Origin: OriginSelective, Batch: Batch{
		Issues: []Issue{{Number: "10", Title: "candidate"}},
		Edges:  map[string][]string{},
	}}

	err := Dispatch(c, nil, fc, fc, dir, f, s, in, claimer)

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("Dispatch: got %v, want ErrSignalledStop", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %d, want 0 (a pre-closed Stop must launch nothing)", len(fr.RunCalls))
	}
}

// TestDispatch_AbortClosedWithBoxInFlight_ReclaimsNeitherFailsNorSettles pins
// the second stage's own launch path (#3522): an Abort closed while a Box is
// in flight reaps it rather than waiting for it to finish, moves the issue
// InProgress -> Dispatchable, marks the shared registry Session carries so
// the reclaimed Box's own completion goroutine abandons instead of failing
// or settling, and Dispatch still reports ErrSignalledStop.
func TestDispatch_AbortClosedWithBoxInFlight_ReclaimsNeitherFailsNorSettles(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	abort := make(chan struct{})
	c.Abort = abort

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

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
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	registry := terminate.NewRegistry()
	session := &Session{Terminated: registry}

	in := Input{Origin: OriginDiscovered, Batch: Batch{Issues: []Issue{{Number: "1", Title: "first"}}}}
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- Dispatch(c, session, fc, fc, dir, f, s, in, claimer)
	}()

	waitOn(t, started1, "issue #1 was never dispatched")
	close(abort)
	if got := waitOn(t, kr.killed, "Kill was never called after abort"); got != "agent-issue-1" {
		t.Fatalf("Kill: got %q, want agent-issue-1", got)
	}
	close(release1)

	err := waitOn(t, resultCh, "Dispatch did not return")
	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("Dispatch: got %v, want ErrSignalledStop", err)
	}

	var toDispatchable int
	for _, call := range fc.TransitionStateCalls {
		if call.Num == "1" && call.To == forge.Dispatchable {
			toDispatchable++
		}
	}
	if toDispatchable == 0 {
		t.Fatalf("TransitionStateCalls: got %+v, want at least one transition of #1 to Dispatchable", fc.TransitionStateCalls)
	}
	if !registry.Marked("1", 0) {
		t.Fatal("registry.Marked(\"1\", 0): got false, want true (Reclaim must mark the shared registry Session carries)")
	}
	if len(s.FailCalls) != 0 {
		t.Fatalf("FailCalls: got %+v, want none (abort abandons, never fails)", s.FailCalls)
	}
	if len(s.SettleCalls) != 0 {
		t.Fatalf("SettleCalls: got %+v, want none (abort abandons, never settles)", s.SettleCalls)
	}
}

// TestDispatch_AbortClosedWithBoxInFlight_NilSession_ReclaimsNeitherFailsNorSettles
// is the nil-Session twin of the test above: Dispatch(cfg, nil, ...) is the
// shape Dispatch's own doc blesses (a nil session, or a nil session.Terminated,
// means dispatchWave's Gate builds its own registry). dispatchWave's abort
// result switch must observe that same fresh registry's mark, not the caller's
// nil, or a reclaimed issue falls through to the !result.Success case and gets
// a spurious Failed transition on top of Reclaim's own Dispatchable one
// (#3522 review finding).
func TestDispatch_AbortClosedWithBoxInFlight_NilSession_ReclaimsNeitherFailsNorSettles(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	abort := make(chan struct{})
	c.Abort = abort

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

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
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	in := Input{Origin: OriginDiscovered, Batch: Batch{Issues: []Issue{{Number: "1", Title: "first"}}}}
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- Dispatch(c, nil, fc, fc, dir, f, s, in, claimer)
	}()

	waitOn(t, started1, "issue #1 was never dispatched")
	close(abort)
	if got := waitOn(t, kr.killed, "Kill was never called after abort"); got != "agent-issue-1" {
		t.Fatalf("Kill: got %q, want agent-issue-1", got)
	}
	close(release1)

	err := waitOn(t, resultCh, "Dispatch did not return")
	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("Dispatch: got %v, want ErrSignalledStop", err)
	}

	var toDispatchable, toFailed int
	for _, call := range fc.TransitionStateCalls {
		if call.Num != "1" {
			continue
		}
		if call.To == forge.Dispatchable {
			toDispatchable++
		}
		if call.To == forge.Failed {
			toFailed++
		}
	}
	if toDispatchable == 0 {
		t.Fatalf("TransitionStateCalls: got %+v, want at least one transition of #1 to Dispatchable", fc.TransitionStateCalls)
	}
	if toFailed != 0 {
		t.Fatalf("TransitionStateCalls: got %+v, want no Failed transition of #1 (abort's mark must be visible to the result switch even with a nil Session)", fc.TransitionStateCalls)
	}
	if len(s.FailCalls) != 0 {
		t.Fatalf("FailCalls: got %+v, want none (abort abandons, never fails)", s.FailCalls)
	}
	if len(s.SettleCalls) != 0 {
		t.Fatalf("SettleCalls: got %+v, want none (abort abandons, never settles)", s.SettleCalls)
	}
}

// TestDispatch_NilStopAndAbort_OrdinarySuccessUnaffected covers the
// nil-channel, nil-Session shape every remaining headless/embedding caller of
// waves.Dispatch still uses (main.go and selective.go's own Dispatch call
// sites now pass a live &waves.Session{Terminated: ...} and set both
// cfg.Stop and cfg.Abort, per #3522): with both channels nil, dispatchWave's
// Gate is permanently inert, so a wave behaves exactly as it did before this
// issue -- ordinary dispatch must never regress for a caller that never
// opted into the two-stage shutdown latch at all.
func TestDispatch_NilStopAndAbort_OrdinarySuccessUnaffected(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	in := Input{Origin: OriginDiscovered, Batch: Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}}
	err := Dispatch(c, nil, fc, fc, dir, f, s, in, claimer)

	if err != nil {
		t.Fatalf("Dispatch: got %v, want nil", err)
	}
	if errors.Is(err, ErrSignalledStop) {
		t.Fatal("Dispatch: got ErrSignalledStop, want nil (neither channel was ever set)")
	}
	if len(fr.RunCalls) != 2 {
		t.Fatalf("RunCalls: got %d, want 2", len(fr.RunCalls))
	}
}

// releasedTo reports how many times fc moved num from InProgress to
// Dispatchable, and whether the operator-termination comment landed on it --
// the two halves of a released claim.
func releasedTo(fc *forge.Fake, num string) (int, bool) {
	var released int
	for _, call := range fc.TransitionStateCalls {
		if call.Num == num && call.From == forge.InProgress && call.To == forge.Dispatchable {
			released++
		}
	}
	var commented bool
	for _, call := range fc.CommentCalls {
		if call.Num == num && strings.Contains(call.Body, "Terminated by operator") {
			commented = true
		}
	}
	return released, commented
}

// TestDispatch_ClaimedUnblocked_StopPreClosed_ReleasesInheritedClaim pins the
// mirror image of the blocked-claim case above: with ISSUE_NUMBER=#42 already
// swapped to in-progress by the workflow and nothing blocking it, the wave
// goroutine's own Allowed check declines before it ever claims. Nobody writes
// a blocked marker on that path, so unless the wave tells the Gate it
// inherited #42's claim, the run exits leaving #42 stranded in-progress with
// no release path (#3522 review finding).
func TestDispatch_ClaimedUnblocked_StopPreClosed_ReleasesInheritedClaim(t *testing.T) {
	c := baseConfig()
	c.MaxParallel = 1
	stop := make(chan struct{})
	close(stop)
	c.Stop = stop

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{testInProgressLabel}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, "agent-trigger", testInProgressLabel)

	in := Input{Origin: OriginClaimed, Batch: Batch{Issues: []Issue{{Number: "42", Title: "claimed issue"}}}}
	err := Dispatch(c, nil, fc, fc, dir, f, s, in, claimer)

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("Dispatch: got %v, want ErrSignalledStop", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %d, want 0 (a pre-closed Stop must launch nothing)", len(fr.RunCalls))
	}
	released, commented := releasedTo(fc, "42")
	if released == 0 || !commented {
		t.Fatalf("release of #42: transitions=%d commented=%v, want at least one InProgress->Dispatchable plus the operator comment (TransitionStateCalls=%+v CommentCalls=%+v)", released, commented, fc.TransitionStateCalls, fc.CommentCalls)
	}
}

// TestDispatch_DiscoveredStopPreClosed_ReleasesNothing is the guard against
// over-releasing: a discovered batch's goroutines decline before they claim,
// so no claim exists to hand back. Releasing anyway would move an issue this
// process never owned.
func TestDispatch_DiscoveredStopPreClosed_ReleasesNothing(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 2
	stop := make(chan struct{})
	close(stop)
	c.Stop = stop

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, label, testInProgressLabel)

	in := Input{Origin: OriginDiscovered, Batch: Batch{Issues: []Issue{{Number: "1"}, {Number: "2"}}}}
	err := Dispatch(c, nil, fc, fc, dir, f, s, in, claimer)

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("Dispatch: got %v, want ErrSignalledStop", err)
	}
	for _, num := range []string{"1", "2"} {
		released, commented := releasedTo(fc, num)
		if released != 0 || commented {
			t.Errorf("#%s: transitions=%d commented=%v, want none (no claim was ever taken)", num, released, commented)
		}
	}
}

// stopOnClaim claims through inner, then closes stop -- so the signal lands
// in the window between a successful claim and gate.Launch, the one place a
// self-claimed issue must still be released.
type stopOnClaim struct {
	inner Claimer
	stop  chan struct{}
	once  sync.Once
}

func (c *stopOnClaim) Claim(num string) error {
	if err := c.inner.Claim(num); err != nil {
		return err
	}
	c.once.Do(func() { close(c.stop) })
	return nil
}

// TestDispatch_StopAfterClaimBeforeLaunch_ReleasesOwnClaim keeps the
// self-claimed half honest: the goroutine took #1's claim itself, so a Launch
// that declines one instruction later must hand it back.
func TestDispatch_StopAfterClaimBeforeLaunch_ReleasesOwnClaim(t *testing.T) {
	c := baseConfig()
	label := "agent-trigger"
	c.MaxParallel = 1
	stop := make(chan struct{})
	c.Stop = stop

	fc := forge.NewFake(dispatchLabels(c, label))
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{label}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := &stopOnClaim{inner: NewLabelClaimer(fc, label, testInProgressLabel), stop: stop}

	in := Input{Origin: OriginDiscovered, Batch: Batch{Issues: []Issue{{Number: "1", Title: "first"}}}}
	err := Dispatch(c, nil, fc, fc, dir, f, s, in, claimer)

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("Dispatch: got %v, want ErrSignalledStop", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("RunCalls: got %d, want 0 (Launch declined before arming)", len(fr.RunCalls))
	}
	released, commented := releasedTo(fc, "1")
	if released == 0 || !commented {
		t.Fatalf("release of #1: transitions=%d commented=%v, want the claim handed back (TransitionStateCalls=%+v CommentCalls=%+v)", released, commented, fc.TransitionStateCalls, fc.CommentCalls)
	}
}

// TestDispatch_CycleErrorWithStopPreClosed_ReportsDisplacedCause pins what the
// boundary override owes the operator: ErrSignalledStop still wins as the
// return value, but the cycle error it displaces must not vanish unprinted
// (#3522 review finding) -- exit 7 with no stated cause is undebuggable.
func TestDispatch_CycleErrorWithStopPreClosed_ReportsDisplacedCause(t *testing.T) {
	c := baseConfig()
	c.MaxParallel = 1
	stop := make(chan struct{})
	close(stop)
	c.Stop = stop

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{testInProgressLabel}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{testInProgressLabel}})

	fr := runner.NewFake()
	dir := tempLogDir(t)
	f := testFactory(t, dir, fr)
	s := newSettle(fc, fc)
	claimer := NewLabelClaimer(fc, "agent-trigger", testInProgressLabel)

	in := Input{Origin: OriginDiscovered, Batch: Batch{
		Issues: []Issue{{Number: "1"}, {Number: "2"}},
		Edges:  map[string][]string{"1": {"2"}, "2": {"1"}},
	}}

	var err error
	stderr := testutil.CaptureStderr(t, func() {
		err = Dispatch(c, nil, fc, fc, dir, f, s, in, claimer)
	})

	if !errors.Is(err, ErrSignalledStop) {
		t.Fatalf("Dispatch: got %v, want ErrSignalledStop", err)
	}
	if !strings.Contains(stderr, "dependency cycle detected") {
		t.Fatalf("stderr = %q, want the displaced cycle error reported", stderr)
	}
}
