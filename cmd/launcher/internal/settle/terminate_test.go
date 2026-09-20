package settle

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/terminate"
)

// assertNoTransitionOrComment fails t if either call slice is non-empty,
// the shared "must not act after termination" check repeated across the
// tests below.
func assertNoTransitionOrComment(t *testing.T, fc *forge.Fake) {
	t.Helper()
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionState must not be called after termination; got %+v", fc.TransitionStateCalls)
	}
	if len(fc.CommentCalls) != 0 {
		t.Errorf("Comment must not be called after termination; got %+v", fc.CommentCalls)
	}
}

// A termination marked before gateToGreen's first poll makes it bail without
// ever confirming green or swapping agent-complete. This is ADR 0024's
// "abandons the settle wherever it stands" applied to the CI-watch phase.
func TestGateToGreen_TerminatedAbandonsWithoutTransition(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)
	reg.Mark("1")

	got, _ := s.gateToGreen("1", 0, testPR, false)

	if got.outcome != gateAbandoned {
		t.Errorf("gateToGreen = %v, want gateAbandoned", got.outcome)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionState must not be called after termination; got %+v", fc.TransitionStateCalls)
	}
}

// Termination lands between the proactive stale-base rebase and
// rewaitAfterForcePush's CI poll (added by fda1a20, in preflightStaleBase), so
// mergeImmediate must report errAbandoned and never reach its own Merge call.
func TestMergeImmediate_TerminatedDuringRewaitAfterStaleBasePreflight(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetNeedsUpdate(testPR, true)
	reg := terminate.NewRegistry()
	tf := terminatingForge{Fake: fc, reg: reg, num: "1"}
	s := newTestSettle(c, tf, tf)
	s.SetTerminated(reg)

	err := s.mergeImmediate("1", 0, testPR, nil)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("mergeImmediate err = %v, want errAbandoned", err)
	}
	if errors.Is(err, errLandingNeverGreen) {
		t.Errorf("mergeImmediate err = %v, must not also match errLandingNeverGreen", err)
	}
	if len(fc.RebasedURLs) != 1 {
		t.Errorf("Rebase called %d times, want 1 (proactive stale-base rebase)", len(fc.RebasedURLs))
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called after termination; fc.Merged=%q", fc.Merged)
	}
}

// A termination marked before mergeImmediate's first attempt stops it from ever
// calling Merge or Rebase, the merge-gate phase of "abandons the settle wherever
// it stands."
func TestMergeImmediate_TerminatedStopsRebaseRetry(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 5
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	s := newTestSettle(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)
	reg.Mark("1")

	err := s.mergeImmediate("1", 0, testPR, dispatch.NewFake())

	if !errors.Is(err, errAbandoned) {
		t.Errorf("mergeImmediate err = %v, want errAbandoned", err)
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called after termination; fc.Merged=%q", fc.Merged)
	}
	if len(fc.RebasedURLs) != 0 {
		t.Errorf("Rebase must not be called after termination; got %v", fc.RebasedURLs)
	}
}

// A termination marked before mergeImmediate is even called stops
// preflightStaleBase from issuing its proactive rebase at all (issue #943), not
// merely from retrying after one already force-pushed. Before the fix,
// mergeImmediate called preflightStaleBase ahead of its own first s.terminated
// check, so a terminated issue with a stale base still got one rebase pushed.
func TestMergeImmediate_TerminatedBeforeStaleBasePreflightSkipsRebase(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetNeedsUpdate(testPR, true)
	s := newTestSettle(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)
	reg.Mark("1")

	err := s.mergeImmediate("1", 0, testPR, nil)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("mergeImmediate err = %v, want errAbandoned", err)
	}
	if len(fc.RebasedURLs) != 0 {
		t.Errorf("Rebase must not be called after termination; got %v", fc.RebasedURLs)
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called after termination; fc.Merged=%q", fc.Merged)
	}
}

// terminatingAfterPolls wraps a forge.Fake so its CheckState call marks num
// terminated once it has been called n times, simulating a signal landing
// mid-poll rather than before the wait even starts (issue #3523).
type terminatingAfterPolls struct {
	*forge.Fake
	reg   *terminate.Registry
	num   string
	n     int
	calls int
}

func (f *terminatingAfterPolls) CheckState(url string) (forge.RollupState, error) {
	f.calls++
	state, err := f.Fake.CheckState(url)
	if f.calls >= f.n {
		f.reg.Mark(f.num)
	}
	return state, err
}

// A termination marked mid-poll — after the loop has already made a few
// CheckState calls, unlike TestGateToGreen_TerminatedAbandonsWithoutTransition
// above which marks before the first poll — must abandon at the very next
// checkpoint rather than run out MergePollTimeout (issue #3523). baseConfig's
// MergePollTimeout(100)/MergePollInterval(1) gives a deadline poll count far
// past wantPolls, so a got.elapsed anywhere near 100 would mean the mark was
// missed and the loop ran to the deadline instead of stopping promptly.
func TestGateToGreen_TerminatedMidPollAbandonsPromptly(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	reg := terminate.NewRegistry()
	const wantPolls = 5
	tf := &terminatingAfterPolls{Fake: fc, reg: reg, num: "1", n: wantPolls}
	s := newTestSettle(c, tf, tf)
	s.SetTerminated(reg)

	got, _ := s.gateToGreen("1", 0, testPR, false)

	if got.outcome != gateAbandoned {
		t.Errorf("gateToGreen = %v, want gateAbandoned", got.outcome)
	}
	if got.elapsed != wantPolls {
		t.Errorf("elapsed = %d, want %d — a mid-poll termination must stop at the next checkpoint, not run toward MergePollTimeout=%d", got.elapsed, wantPolls, c.MergePollTimeout)
	}
	assertNoTransitionOrComment(t, fc)
}

// The graceful-drain counterpart to TestGateToGreen_TerminatedMidPollAbandonsPromptly:
// with nothing ever marked, gateToGreen must run the wait out to its full
// MergePollTimeout and return gateTerminal, never cut short. A drain that
// never escalates to a signal must not shorten an in-flight settle
// (issue #3523).
func TestGateToGreen_NeverTerminatedRunsFullDeadline(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	s := newTestSettle(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)

	got, _ := s.gateToGreen("1", 0, testPR, false)

	if got.outcome != gateTerminal {
		t.Errorf("gateToGreen = %v, want gateTerminal", got.outcome)
	}
	if got.elapsed != c.MergePollTimeout {
		t.Errorf("elapsed = %d, want %d (MergePollTimeout) — a never-terminated wait must run the full deadline", got.elapsed, c.MergePollTimeout)
	}
}

// terminatingForge wraps a forge.Fake so its Rebase call marks num terminated
// after returning, simulating Terminate reaping the settle goroutine while a
// force-push is in flight.
type terminatingForge struct {
	*forge.Fake
	reg *terminate.Registry
	num string
}

func (f terminatingForge) Rebase(url string) error {
	err := f.Fake.Rebase(url)
	f.reg.Mark(f.num)
	return err
}

// A termination landing between a successful rebase force-push and
// rewaitAfterForcePush's CI poll must be reported as errAbandoned, not wrapped
// into errLandingNeverGreen, the gap issue #805 closes. Without the fix,
// gateToGreen's own gateAbandoned result collapses into a generic "never went
// green" error, which selfHeal mis-routes to landingFailed, not landingAbandoned.
func TestMergeImmediate_TerminatedDuringRewaitAfterPlainRebase(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 5
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	reg := terminate.NewRegistry()
	tf := terminatingForge{Fake: fc, reg: reg, num: "1"}
	s := newTestSettle(c, tf, tf)
	s.SetTerminated(reg)

	err := s.mergeImmediate("1", 0, testPR, dispatch.NewFake())

	if !errors.Is(err, errAbandoned) {
		t.Errorf("mergeImmediate err = %v, want errAbandoned", err)
	}
	if errors.Is(err, errLandingNeverGreen) {
		t.Errorf("mergeImmediate err = %v, must not also match errLandingNeverGreen", err)
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called again after termination; fc.Merged=%q", fc.Merged)
	}
}

// terminatingConflictResolver wraps a dispatch.Fake so its ResolveConflict call
// marks num terminated after returning, simulating Terminate reaping the settle
// goroutine right after a successful agent-assisted conflict resolve, before the
// post-force-push CI re-wait runs.
type terminatingConflictResolver struct {
	*dispatch.Fake
	reg *terminate.Registry
	num string
}

func (d terminatingConflictResolver) ResolveConflict(pr string) error {
	err := d.Fake.ResolveConflict(pr)
	d.reg.Mark(d.num)
	return err
}

// Termination lands between a successful agent conflict-resolve and
// rewaitAfterForcePush's CI poll (in mergeImmediate's conflict-retry branch), so
// mergeImmediate must report errAbandoned rather than errLandingNeverGreen.
func TestMergeImmediate_TerminatedDuringRewaitAfterConflictResolve(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 5
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrMergeConflict
	reg := terminate.NewRegistry()
	s := newTestSettle(c, fc, fc)
	s.SetTerminated(reg)
	d := terminatingConflictResolver{Fake: dispatch.NewFake(), reg: reg, num: "1"}

	err := s.mergeImmediate("1", 0, testPR, d)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("mergeImmediate err = %v, want errAbandoned", err)
	}
	if errors.Is(err, errLandingNeverGreen) {
		t.Errorf("mergeImmediate err = %v, must not also match errLandingNeverGreen", err)
	}
	if len(d.ResolveConflictCalls) != 1 {
		t.Errorf("want exactly 1 ResolveConflict call, got %d", len(d.ResolveConflictCalls))
	}
	if fc.Merged != "" {
		t.Errorf("Merge must not be called again after termination; fc.Merged=%q", fc.Merged)
	}
}

// terminatingDispatcher wraps a dispatch.Fake so its Fix call marks num
// terminated after returning, simulating Terminate reaping the fix-pass Box
// mid-flight: the caller sees Fix's own failure result, then notices the
// termination on its next loop iteration.
type terminatingDispatcher struct {
	*dispatch.Fake
	reg *terminate.Registry
	num string
}

func (d terminatingDispatcher) Fix(pass int, ciFailureSummary string) dispatch.Result {
	res := d.Fake.Fix(pass, ciFailureSummary)
	d.reg.Mark(d.num)
	return res
}

// A termination landing while a fix-pass Box is running stops selfHeal from
// dispatching a second fix pass or re-polling CI. selfHeal abandons on the very
// next checkpoint instead of continuing the attempt loop.
func TestSelfHeal_TerminatedDuringFixPass_StopsRetryLoop(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	// Genuine red on every poll if the loop were ever allowed to continue, so
	// termination is what stops it, not exhausted fix attempts.
	fc.SetCheckStates(testPR, []forge.RollupState{
		forge.StateFailure, forge.StateFailure, forge.StateFailure, forge.StateFailure,
	})
	s := newTestSettle(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)
	d := terminatingDispatcher{Fake: dispatch.NewFake(), reg: reg, num: "1"}

	landing, _ := s.selfHeal(d, "1", 0, testPR)

	if landing != landingAbandoned {
		t.Errorf("selfHeal = %v, want landingAbandoned", landing)
	}
	if len(d.FixCalls) != 1 {
		t.Errorf("want exactly 1 fix call (termination stops the retry loop), got %d: %+v", len(d.FixCalls), d.FixCalls)
	}
}

// For a termination landing during the post-force-push re-wait, selfHeal must
// report landingAbandoned, not landingFailed, and take none of landingFailed's
// side effects (no agent-failed transition, no "landing failed" comment). That
// is the "no further action, Terminate already handled it" contract on
// landingAbandoned.
func TestSelfHeal_TerminatedDuringRewaitAfterForcePush_ReportsAbandoned(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 5
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	reg := terminate.NewRegistry()
	tf := terminatingForge{Fake: fc, reg: reg, num: "1"}
	s := newTestSettle(c, tf, tf)
	s.SetTerminated(reg)

	landing, _ := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)

	if landing != landingAbandoned {
		t.Errorf("selfHeal = %v, want landingAbandoned", landing)
	}
	assertNoTransitionOrComment(t, fc)
}

// Reproduces the issue #743 race at the settle seam: an old, still-in-flight
// settle goroutine (holding the generation its dispatch was launched under) must
// keep seeing itself as terminated even after a re-pick has begun a fresh
// generation for the same issue number. The old blind Unmark would have erased
// the mark out from under it here; Begin must not.
func TestGateToGreen_RepickDoesNotClearAnAbandonedSettlesMark(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)

	oldGen := reg.Begin("1")
	reg.Mark("1")
	newGen := reg.Begin("1") // a re-pick's discover claims a fresh incarnation, mid-race

	if got, _ := s.gateToGreen("1", oldGen, testPR, false); got.outcome != gateAbandoned {
		t.Errorf("gateToGreen(oldGen) = %v, want gateAbandoned — a re-pick must not erase an in-flight settle's own mark", got.outcome)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionState must not be called for the abandoned generation; got %+v", fc.TransitionStateCalls)
	}
	if reg.Marked("1", newGen) {
		t.Error("Marked(1, newGen) = true, want false — the re-pick's own fresh generation was never terminated")
	}
}

// Settle's "ready" branch posts no usage comment when selfHeal reports
// landingAbandoned: Terminate already recorded its own comment, and a second,
// unrelated one from the orphaned settle goroutine would be noise the operator
// never asked for.
func TestSettle_AbandonedSkipsUsageComment(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)
	reg.Mark("1")

	d := dispatch.NewFake()
	s.Settle(d, "1", 0, dispatch.Result{
		Success: true,
		Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "1", Landing: testPR, Status: "ready", Note: "ok"},
		},
	})

	if len(fc.CommentCalls) != 0 {
		t.Errorf("no comment expected after termination; got %+v", fc.CommentCalls)
	}
}

// Guards against #1174 regressing: doc-comments here once pinned another file's
// exact line number, which rots the instant that file shifts. Symbol names
// (function, type, const) are stable across edits; line numbers are not.
func TestDocComments_NoHardcodedLineNumbers(t *testing.T) {
	src, err := os.ReadFile("terminate_test.go")
	if err != nil {
		t.Fatalf("ReadFile(terminate_test.go) = %v", err)
	}

	re := regexp.MustCompile(`\b\w+\.go:\d+`)
	var found []string
	for _, line := range strings.Split(string(src), "\n") {
		if _, comment, ok := strings.Cut(line, "//"); ok {
			found = append(found, re.FindAllString(comment, -1)...)
		}
	}
	if found != nil {
		t.Errorf("doc-comments must reference symbols, not line numbers; found %v", found)
	}
}
