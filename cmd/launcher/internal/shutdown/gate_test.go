package shutdown_test

import (
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/shutdown"
)

// stubReaper is a minimal terminate.Reaper: it counts Kill calls per issue
// and, when killSignal is non-nil, signals it after each Kill so a watcher
// test can wait on the reclaim without a sleep.
type stubReaper struct {
	mu         sync.Mutex
	killed     []string
	killSignal chan string
}

func (s *stubReaper) Kill(number string) error {
	s.mu.Lock()
	s.killed = append(s.killed, number)
	s.mu.Unlock()
	if s.killSignal != nil {
		s.killSignal <- number
	}
	return nil
}

func (s *stubReaper) AppendTerminalLine(number, note string) error { return nil }

func (s *stubReaper) killCount(number string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, k := range s.killed {
		if k == number {
			n++
		}
	}
	return n
}

func newFakeForge(t *testing.T, nums ...string) *forge.Fake {
	t.Helper()
	labels := forge.DispatchLabels{Dispatchable: "ready-for-agent", InProgress: "agent-in-progress"}
	fc := forge.NewFake(labels)
	fc.BranchPrefix = "agent/issue-"
	for _, num := range nums {
		fc.SetIssue(forge.Issue{Number: num, Title: "issue " + num, Labels: []string{"agent-in-progress"}})
	}
	return fc
}

// inProgressToDispatchableCount counts only the InProgress->Dispatchable
// transitions Reclaim issues (it also always issues a harmless
// Complete->Dispatchable one), so a per-issue assertion isn't fooled into
// double-counting.
func inProgressToDispatchableCount(fc *forge.Fake, num string) int {
	n := 0
	for _, call := range fc.TransitionStateCalls {
		if call.Num == num && call.From == forge.InProgress && call.To == forge.Dispatchable {
			n++
		}
	}
	return n
}

// reclaimCommentCount counts the "Terminated by operator" comments Reclaim
// posts for num, the operator-visible half of a release: a transition without
// one leaves the issue silently re-dispatchable.
func reclaimCommentCount(fc *forge.Fake, num string) int {
	n := 0
	for _, call := range fc.CommentCalls {
		if call.Num == num && strings.Contains(call.Body, "Terminated by operator") {
			n++
		}
	}
	return n
}

func TestAllowed_StopClosed_DeniesAndSignalsWithoutReclaim(t *testing.T) {
	fc := newFakeForge(t, "1")
	reaper := &stubReaper{}
	stop := make(chan struct{})
	close(stop)

	g := shutdown.NewGate(stop, nil, fc, fc, reaper, nil, "agent-complete")

	if g.Allowed("1") {
		t.Fatal("Allowed: want false once stop is closed")
	}
	if !g.Signalled() {
		t.Fatal("Signalled: want true once stop is closed")
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls: want none, got %+v", fc.TransitionStateCalls)
	}
	if len(reaper.killed) != 0 {
		t.Errorf("killed: want none, got %v", reaper.killed)
	}
	g.Settle()
}

func TestAbort_ReclaimsEveryInFlightIssueExactlyOnce(t *testing.T) {
	fc := newFakeForge(t, "1", "2")
	reaper := &stubReaper{}
	abort := make(chan struct{})

	g := shutdown.NewGate(nil, abort, fc, fc, reaper, nil, "agent-complete")

	if !g.Launch("1", func() {}) {
		t.Fatal("Launch #1: want true before abort")
	}
	if !g.Launch("2", func() {}) {
		t.Fatal("Launch #2: want true before abort")
	}

	close(abort)

	if g.Allowed("1") {
		t.Fatal("Allowed: want false once abort is closed")
	}
	g.Settle()

	for _, num := range []string{"1", "2"} {
		if got := inProgressToDispatchableCount(fc, num); got != 1 {
			t.Errorf("#%s: InProgress->Dispatchable transitions = %d, want 1", num, got)
		}
		if got := reaper.killCount(num); got != 1 {
			t.Errorf("#%s: kill count = %d, want 1", num, got)
		}
	}
	if !g.Signalled() {
		t.Fatal("Signalled: want true once abort is closed")
	}
}

func TestLeave_BeforeAbort_IssueNotReclaimed(t *testing.T) {
	fc := newFakeForge(t, "1", "2")
	reaper := &stubReaper{}
	abort := make(chan struct{})

	g := shutdown.NewGate(nil, abort, fc, fc, reaper, nil, "agent-complete")

	if !g.Launch("1", func() {}) {
		t.Fatal("Launch #1: want true")
	}
	if !g.Launch("2", func() {}) {
		t.Fatal("Launch #2: want true")
	}
	g.Leave("1")

	close(abort)
	g.Allowed("1")
	g.Settle()

	if got := inProgressToDispatchableCount(fc, "1"); got != 0 {
		t.Errorf("#1 (left before abort): InProgress->Dispatchable transitions = %d, want 0", got)
	}
	if got := reaper.killCount("1"); got != 0 {
		t.Errorf("#1 (left before abort): kill count = %d, want 0", got)
	}
	if got := inProgressToDispatchableCount(fc, "2"); got != 1 {
		t.Errorf("#2 (still in flight): InProgress->Dispatchable transitions = %d, want 1", got)
	}
}

// TestAbort_SettledIssueNotReclaimed pins the window Leave alone cannot
// close: a settler writes its complete label and returns, and the abort lands
// before the caller's next statement (Leave) runs, so the issue is still in
// the in-flight snapshot. Reclaiming it would drag a merged issue back to
// dispatchable, which is what the recover path saw flake (#3522).
func TestAbort_SettledIssueNotReclaimed(t *testing.T) {
	fc := newFakeForge(t, "1", "2")
	fc.SetIssue(forge.Issue{Number: "1", Title: "issue 1", Labels: []string{"agent-complete"}})
	reaper := &stubReaper{}
	abort := make(chan struct{})

	g := shutdown.NewGate(nil, abort, fc, fc, reaper, nil, "agent-complete")

	if !g.Launch("1", func() {}) {
		t.Fatal("Launch #1: want true")
	}
	if !g.Launch("2", func() {}) {
		t.Fatal("Launch #2: want true")
	}

	close(abort)
	g.Settle()

	if got := inProgressToDispatchableCount(fc, "1"); got != 0 {
		t.Errorf("#1 (already settled): InProgress->Dispatchable transitions = %d, want 0", got)
	}
	if got := reclaimCommentCount(fc, "1"); got != 0 {
		t.Errorf("#1 (already settled): reclaim comments = %d, want 0", got)
	}
	if got := reaper.killCount("1"); got != 0 {
		t.Errorf("#1 (already settled): kill count = %d, want 0", got)
	}
	if got := inProgressToDispatchableCount(fc, "2"); got != 1 {
		t.Errorf("#2 (still in flight): InProgress->Dispatchable transitions = %d, want 1", got)
	}
}

func TestLaunch_DecliningAfterSignal_ReclaimsHeldClaimAndSkipsArm(t *testing.T) {
	fc := newFakeForge(t, "1")
	reaper := &stubReaper{}
	stop := make(chan struct{})
	close(stop)

	g := shutdown.NewGate(stop, nil, fc, fc, reaper, nil, "agent-complete")
	g.Hold("1")

	armed := false
	if g.Launch("1", func() { armed = true }) {
		t.Fatal("Launch: want false once stop is closed")
	}
	if armed {
		t.Error("arm: must not be called when Launch declines")
	}
	if got := inProgressToDispatchableCount(fc, "1"); got != 1 {
		t.Errorf("#1: InProgress->Dispatchable transitions = %d, want 1 (reclaimed, not stranded)", got)
	}
	if got := reaper.killCount("1"); got != 0 {
		t.Errorf("#1: kill count = %d, want 0 (arm never ran, so there is no Box to kill)", got)
	}
	if got := reclaimCommentCount(fc, "1"); got != 1 {
		t.Errorf("#1: reclaim comments = %d, want 1", got)
	}
	g.Settle()
}

func TestGate_BothChannelsNil_IsInert(t *testing.T) {
	fc := newFakeForge(t)
	reaper := &stubReaper{}

	g := shutdown.NewGate(nil, nil, fc, fc, reaper, nil, "agent-complete")

	if !g.Allowed("1") {
		t.Fatal("Allowed: want true when both channels are nil")
	}
	if g.Signalled() {
		t.Fatal("Signalled: want false when both channels are nil")
	}
	g.Watch()
	g.Settle()
	if g.Signalled() {
		t.Fatal("Signalled: want false after Settle with both channels nil")
	}
}

func TestWatch_AbortAfterStart_ReclaimsPromptly(t *testing.T) {
	fc := newFakeForge(t, "1")
	killed := make(chan string, 1)
	reaper := &stubReaper{killSignal: killed}
	abort := make(chan struct{})

	g := shutdown.NewGate(nil, abort, fc, fc, reaper, nil, "agent-complete")
	if !g.Launch("1", func() {}) {
		t.Fatal("Launch #1: want true before abort")
	}

	g.Watch()
	close(abort)

	select {
	case num := <-killed:
		if num != "1" {
			t.Fatalf("killSignal: want #1, got #%s", num)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the watcher to reclaim #1")
	}

	g.Settle()

	if got := inProgressToDispatchableCount(fc, "1"); got != 1 {
		t.Errorf("#1: InProgress->Dispatchable transitions = %d, want 1", got)
	}
}

// TestSettle_CalledTwice_DoesNotPanic pins Settle as call-once-safe: every
// early-return call site in recoverByNumber defers Settle right after Watch,
// so a caller that also settles inline on its happy path (main.go's
// SettleRelayedBranch/SettleAdopted arms) must be able to call Settle a
// second time on the way out without the unguarded `close(g.watchDone)`
// panicking (issue #3522, the watchSettled guard in Settle).
func TestSettle_CalledTwice_DoesNotPanic(t *testing.T) {
	fc := newFakeForge(t, "1")
	reaper := &stubReaper{}
	abort := make(chan struct{})

	g := shutdown.NewGate(nil, abort, fc, fc, reaper, nil, "agent-complete")
	g.Watch()

	g.Settle()
	g.Settle()
}

// TestLaunch_DecliningUnheldIssue_TouchesNothing pins recover's shape: the
// agent-recover workflow took #1's claim and its PR is still open, so this
// process never called Hold. A stage-one stop must decline without handing #1
// back to Dispatchable, or the next wave launches a fresh Box on an issue
// whose PR is live (issue #3522).
func TestLaunch_DecliningUnheldIssue_TouchesNothing(t *testing.T) {
	fc := newFakeForge(t, "1")
	reaper := &stubReaper{}
	stop := make(chan struct{})
	close(stop)

	g := shutdown.NewGate(stop, nil, fc, fc, reaper, nil, "agent-complete")

	if g.Launch("1", func() { t.Error("arm: must not be called when Launch declines") }) {
		t.Fatal("Launch: want false once stop is closed")
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls: want none for an unheld claim, got %+v", fc.TransitionStateCalls)
	}
	if got := reclaimCommentCount(fc, "1"); got != 0 {
		t.Errorf("#1: reclaim comments = %d, want 0 for an unheld claim", got)
	}
	g.Settle()
}

// TestAllowed_DecliningHeldIssue_ReleasesClaim covers the pre-launch
// checkpoint of a batch the caller's workflow already claimed: the Gate holds
// the claim there, so declining must release it rather than strand the issue
// on agent-in-progress with no release path (issue #3522).
func TestAllowed_DecliningHeldIssue_ReleasesClaim(t *testing.T) {
	fc := newFakeForge(t, "1")
	reaper := &stubReaper{}
	stop := make(chan struct{})
	close(stop)

	g := shutdown.NewGate(stop, nil, fc, fc, reaper, nil, "agent-complete")
	g.Hold("1")

	if g.Allowed("1") {
		t.Fatal("Allowed: want false once stop is closed")
	}
	if got := inProgressToDispatchableCount(fc, "1"); got != 1 {
		t.Errorf("#1: InProgress->Dispatchable transitions = %d, want 1", got)
	}
	if got := reclaimCommentCount(fc, "1"); got != 1 {
		t.Errorf("#1: reclaim comments = %d, want 1", got)
	}
	if got := reaper.killCount("1"); got != 0 {
		t.Errorf("#1: kill count = %d, want 0 (nothing launched yet)", got)
	}
	if g.Allowed("1") {
		t.Fatal("Allowed: want false on the second call too")
	}
	if got := inProgressToDispatchableCount(fc, "1"); got != 1 {
		t.Errorf("#1 after a second decline: InProgress->Dispatchable transitions = %d, want 1", got)
	}
	g.Settle()
}

func TestAllowed_DecliningUnheldIssue_TouchesNothing(t *testing.T) {
	fc := newFakeForge(t, "1")
	reaper := &stubReaper{}
	stop := make(chan struct{})
	close(stop)

	g := shutdown.NewGate(stop, nil, fc, fc, reaper, nil, "agent-complete")

	if g.Allowed("1") {
		t.Fatal("Allowed: want false once stop is closed")
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionStateCalls: want none for an unheld claim, got %+v", fc.TransitionStateCalls)
	}
	if got := reclaimCommentCount(fc, "1"); got != 0 {
		t.Errorf("#1: reclaim comments = %d, want 0 for an unheld claim", got)
	}
	g.Settle()
}

// TestWatch_CalledTwice_LeavesNoWatcherBehind pins Watch as call-once safe:
// a second call used to overwrite watchDone/watchExited, leaking the first
// watcher goroutine that Settle's sync.Once could then never join, so the
// second call is a no-op and the one watcher Settle joins is the only one.
func TestWatch_CalledTwice_LeavesNoWatcherBehind(t *testing.T) {
	fc := newFakeForge(t, "1")
	reaper := &stubReaper{}
	abort := make(chan struct{})

	before := runtime.NumGoroutine()
	g := shutdown.NewGate(nil, abort, fc, fc, reaper, nil, "agent-complete")
	g.Watch()
	g.Watch()

	done := make(chan struct{})
	go func() {
		defer close(done)
		g.Settle()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out in Settle after a repeated Watch")
	}

	for i := 0; i < 100; i++ {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("goroutines after Settle = %d, want <= %d (a watcher leaked)", runtime.NumGoroutine(), before)
}
