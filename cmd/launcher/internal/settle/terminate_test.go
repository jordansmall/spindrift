package settle

import (
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// assertNoTransitionOrComment fails t if TransitionState, Comment, or
// EnqueueAutoMerge was called, the shared "must not act after termination"
// check most of the tests below share. EnqueueAutoMerge only ever fires
// under MergeMode=auto, which no test in this file sets, so folding it in
// here stays truthful at every call site. MarkReady does not: three tests
// below deliberately let termination land during or after MarkReady's own
// round-trip, so a MarkReady check lives only in the narrower
// assertNoPRWriteAfterTermination below, not here.
func assertNoTransitionOrComment(t *testing.T, fc *forge.Fake) {
	t.Helper()
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionState must not be called after termination; got %+v", fc.TransitionStateCalls)
	}
	if len(fc.CommentCalls) != 0 {
		t.Errorf("Comment must not be called after termination; got %+v", fc.CommentCalls)
	}
	if len(fc.EnqueueAutoMergeCalls) != 0 {
		t.Errorf("EnqueueAutoMerge must not be called after termination; got %+v", fc.EnqueueAutoMergeCalls)
	}
}

// assertClaimUntouched checks issue 1 still wears the live run's
// agent-in-progress claim and was never demoted to agent-failed.
func assertClaimUntouched(t *testing.T, fc *forge.Fake) {
	t.Helper()
	iss, _ := fc.Issue("1")
	if !containsLabel(iss.Labels, "agent-in-progress") || containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("live run's claim must stand untouched; labels=%v", iss.Labels)
	}
}

// assertNoPRWriteAfterTermination extends assertNoTransitionOrComment with a
// MarkReady check, for call sites whose path never reaches gateGreen's
// MarkReady flip at all. It must not be used where termination lands during
// or after that flip — TestSelfHeal_TerminatedDuringRewaitAfterForcePush_ReportsAbandoned,
// TestSelfHeal_ConflictResolveBoxReaped_ReportsAbandonedNotFailed, and
// TestSelfHeal_TerminatedDuringMarkReady_ReportsAbandoned all call MarkReady
// before their own mark lands, so they stay on the narrower helper above.
func assertNoPRWriteAfterTermination(t *testing.T, fc *forge.Fake) {
	t.Helper()
	assertNoTransitionOrComment(t, fc)
	if len(fc.MarkReadyCalls) != 0 {
		t.Errorf("MarkReady must not be called after termination; got %+v", fc.MarkReadyCalls)
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
	reg := s.Registry()
	reg.Mark("1")

	got, _ := s.gateToGreen("1", 0, testPR, false)

	if !errors.Is(got.err, errAbandoned) {
		t.Errorf("gateToGreen = %v (err %v), want errAbandoned", got.outcome, got.err)
	}
	assertNoTransitionOrComment(t, fc)
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
	s := newTestSettle(c, fc, fc)
	fc.On("Rebase", func(int) { s.Registry().Mark("1") })

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
	reg := s.Registry()
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
	reg := s.Registry()
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

// A termination marked mid-poll — after the loop has already made a few
// CheckState calls, unlike TestGateToGreen_TerminatedAbandonsWithoutTransition
// above which marks before the first poll — must abandon at the very next
// checkpoint rather than run out MergePollTimeout (issue #3523). got.elapsed
// is watch.poll's elapsed seconds, not a poll count, so wantElapsed converts
// markAfterCall (a call count) via MergePollInterval rather than comparing
// the two directly — the units happen to coincide only because baseConfig
// pins MergePollInterval to 1. baseConfig's MergePollTimeout(100) gives a
// deadline far past wantElapsed, so a got.elapsed anywhere near 100 would
// mean the mark was missed and the loop ran to the deadline instead of
// stopping promptly.
func TestGateToGreen_TerminatedMidPollAbandonsPromptly(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	const markAfterCall = 5
	s := newTestSettle(c, fc, fc)
	// Mark is idempotent, so every poll from the markAfterCall-th on re-marking is harmless.
	fc.On("CheckState", func(n int) {
		if n >= markAfterCall {
			s.Registry().Mark("1")
		}
	})

	got, _ := s.gateToGreen("1", 0, testPR, false)

	if !errors.Is(got.err, errAbandoned) {
		t.Errorf("gateToGreen = %v (err %v), want errAbandoned", got.outcome, got.err)
	}
	wantElapsed := markAfterCall * c.MergePollInterval
	if got.elapsed != wantElapsed {
		t.Errorf("elapsed = %d, want %d — a mid-poll termination must stop at the next checkpoint, not run toward MergePollTimeout=%d", got.elapsed, wantElapsed, c.MergePollTimeout)
	}
	assertNoPRWriteAfterTermination(t, fc)
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

	got, _ := s.gateToGreen("1", 0, testPR, false)

	if got.outcome != gateTerminal {
		t.Errorf("gateToGreen = %v, want gateTerminal", got.outcome)
	}
	if got.elapsed != c.MergePollTimeout {
		t.Errorf("elapsed = %d, want %d (MergePollTimeout) — a never-terminated wait must run the full deadline", got.elapsed, c.MergePollTimeout)
	}
}

// A termination landing between a successful rebase force-push and
// rewaitAfterForcePush's CI poll must be reported as errAbandoned, not wrapped
// into errLandingNeverGreen, the gap issue #805 closes. Without the fix,
// gateToGreen's own errAbandoned result collapses into a generic "never went
// green" error, which selfHeal mis-routes to landingFailed, not errAbandoned.
func TestMergeImmediate_TerminatedDuringRewaitAfterPlainRebase(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 5
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	s := newTestSettle(c, fc, fc)
	fc.On("Rebase", func(int) { s.Registry().Mark("1") })

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
	s := newTestSettle(c, fc, fc)
	reg := s.Registry()
	d := dispatch.NewFake()
	d.On("ResolveConflict", func(int) { reg.Mark("1") })

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
	reg := s.Registry()
	d := dispatch.NewFake()
	d.On("Fix", func(int) { reg.Mark("1") })

	_, _, err := s.selfHeal(d, "1", 0, testPR, nil)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("selfHeal err = %v, want errAbandoned", err)
	}
	if len(d.FixCalls) != 1 {
		t.Errorf("want exactly 1 fix call (termination stops the retry loop), got %d: %+v", len(d.FixCalls), d.FixCalls)
	}
}

// For a termination landing during the post-force-push re-wait, selfHeal must
// report errAbandoned, not landingFailed, and take none of landingFailed's
// side effects (no agent-failed transition, no "landing failed" comment). That
// is the "no further action, Terminate already handled it" contract on
// errAbandoned.
func TestSelfHeal_TerminatedDuringRewaitAfterForcePush_ReportsAbandoned(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 5
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	s := newTestSettle(c, fc, fc)
	fc.On("Rebase", func(int) { s.Registry().Mark("1") })

	_, _, err := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("selfHeal err = %v, want errAbandoned", err)
	}
	assertNoTransitionOrComment(t, fc)
}

// A termination marked while a fix-pass Box is being reaped (its Fix call
// both marks the registry and returns a non-success result, since Reclaim's
// SIGKILL is what produced that exit) must abandon rather than commit
// agent-failed — the fix-failed branch had no terminated check at all before
// this (issue #3523).
func TestSelfHeal_TerminatedFixPassReaped_ReportsAbandonedNotFailed(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	fk := dispatch.NewFake()
	fk.FixResult = dispatch.Failed(dispatch.Result{})
	s := newTestSettle(c, fc, fc)
	reg := s.Registry()
	fk.On("Fix", func(int) { reg.Mark("1") })

	_, _, err := s.selfHeal(fk, "1", 0, testPR, nil)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("selfHeal err = %v, want errAbandoned", err)
	}
	assertNoPRWriteAfterTermination(t, fc)
}

// The fix-no-op sibling of the case above: Fix exits zero, the head SHA is
// unchanged on both the immediate and confirm reads, and the mark lands
// during the confirm-pause sleep — the window the fix-no-op branch drives
// through s.clock.Sleep. Must abandon rather than commit agent-failed
// (issue #3523).
func TestSelfHeal_TerminatedDuringFixNoOpConfirmSleep_ReportsAbandoned(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	fc.SetHeadCommitSHAs(testPR, []string{"sha-unchanged", "sha-unchanged", "sha-unchanged"})
	var s *Settle
	c.Clock = dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) { s.Registry().Mark("1") }}
	s = newTestSettle(c, fc, fc)

	_, _, err := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("selfHeal err = %v, want errAbandoned", err)
	}
	assertNoPRWriteAfterTermination(t, fc)
}

// A gate-terminal outcome observed after termination must abandon, not
// fail (issue #3523). MergePollTimeout=0 makes the first CheckState call
// also hit the deadline, so the outcome is gateTerminal, not an errAbandoned observation.
func TestSelfHeal_TerminatedGateTerminal_ReportsAbandonedNotFailed(t *testing.T) {
	c := baseConfig()
	c.MergePollTimeout = 0
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	s := newTestSettle(c, fc, fc)
	fc.On("CheckState", func(int) { s.Registry().Mark("1") })

	_, _, err := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("selfHeal err = %v, want errAbandoned", err)
	}
	assertNoPRWriteAfterTermination(t, fc)
}

// A red gate with the fix budget exhausted must abandon, not fail
// (issue #3523). The mark lands as CheckState's red-poll call returns, so
// the switch sees gateRedRetry — exercising the fix-exhausted guard itself.
func TestSelfHeal_TerminatedRedRetryFixExhausted_ReportsAbandonedNotFailed(t *testing.T) {
	c := fixConfig(0)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	s := newTestSettle(c, fc, fc)
	fc.On("CheckState", func(int) { s.Registry().Mark("1") })

	d := dispatch.NewFake()
	_, _, err := s.selfHeal(d, "1", 0, testPR, nil)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("selfHeal err = %v, want errAbandoned", err)
	}
	assertNoPRWriteAfterTermination(t, fc)
	// No FixCalls assertion: fixConfig(0) makes attempt(0) >= MaxFixAttempts
	// true unconditionally, so d.Fix is unreachable on this path whether or
	// not the termination guard above ran — a FixCalls check here would pass
	// even with that guard deleted.
}

// A fix pass that succeeds but whose issue was marked while it
// ran must abandon before relayBoxBundle runs — relaying pushes bundle work
// for an issue Reclaim already released (issue #3523).
func TestSelfHeal_TerminatedFixPassSucceeded_SkipsRelay(t *testing.T) {
	c := fixConfig(3)
	c.OutboxDir = func(num string) string { return "/outbox/" + num }
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	cf := fc.AsGithubReadOnly()
	s := newTestSettle(c, fc, cf)
	reg := s.Registry()
	d := dispatch.NewFake()
	d.On("Fix", func(int) { reg.Mark("1") })

	_, _, err := s.selfHeal(d, "1", 0, testPR, nil)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("selfHeal err = %v, want errAbandoned", err)
	}
	if len(fc.RelayBundleCalls) != 0 {
		t.Errorf("RelayBundle must not be called after termination; got %+v", fc.RelayBundleCalls)
	}
	assertNoPRWriteAfterTermination(t, fc)
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
	reg := s.Registry()

	oldGen := reg.Begin("1")
	reg.Mark("1")
	newGen := reg.Begin("1") // a re-pick's discover claims a fresh incarnation, mid-race

	if got, _ := s.gateToGreen("1", oldGen, testPR, false); !errors.Is(got.err, errAbandoned) {
		t.Errorf("gateToGreen(oldGen) = %v (err %v), want errAbandoned — a re-pick must not erase an in-flight settle's own mark", got.outcome, got.err)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionState must not be called for the abandoned generation; got %+v", fc.TransitionStateCalls)
	}
	if reg.Marked("1", newGen) {
		t.Error("Marked(1, newGen) = true, want false — the re-pick's own fresh generation was never terminated")
	}
}

// Settle's "ready" branch posts no usage comment when selfHeal reports
// errAbandoned: Terminate already recorded its own comment, and a second,
// unrelated one from the orphaned settle goroutine would be noise the operator
// never asked for.
func TestSettle_AbandonedSkipsUsageComment(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)
	reg := s.Registry()
	reg.Mark("1")

	d := dispatch.NewFake()
	s.Settle(d, "1", 0, dispatch.Result{
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

// Reclaim's SIGKILL reaps the conflict-resolve Box just like a fix-pass Box, so
// ResolveConflict returns the kill's non-zero exit while the mark is already
// set. resolveConflict must abandon instead of wrapping that exit as
// errLandingNeverGreen, which selfHeal would commit as agent-failed (#3523).
func TestMergeImmediate_ConflictResolveBoxReaped_ReportsAbandonedNotNeverGreen(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 5
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrMergeConflict
	fk := dispatch.NewFake()
	fk.ResolveConflictErr = errors.New("exit status 137")
	s := newTestSettle(c, fc, fc)
	reg := s.Registry()
	fk.On("ResolveConflict", func(int) { reg.Mark("1") })

	err := s.mergeImmediate("1", 0, testPR, fk)

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

// The end-to-end consequence of the case above: selfHeal must report
// errAbandoned and leave the issue alone, since Reclaim already moved it
// back to Dispatchable — a Failed commit here would fight that (#3523).
func TestSelfHeal_ConflictResolveBoxReaped_ReportsAbandonedNotFailed(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 5
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrMergeConflict
	fk := dispatch.NewFake()
	fk.ResolveConflictErr = errors.New("exit status 137")
	s := newTestSettle(c, fc, fc)
	reg := s.Registry()
	fk.On("ResolveConflict", func(int) { reg.Mark("1") })

	_, _, err := s.selfHeal(fk, "1", 0, testPR, nil)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("selfHeal err = %v, want errAbandoned", err)
	}
	assertNoTransitionOrComment(t, fc)
}

// resolveConflict's other caller, preflightStaleBase, reaches the same reaped
// Box through the proactive stale-base rebase (#3523).
func TestMergeImmediate_ConflictResolveBoxReapedDuringStaleBasePreflight_ReportsAbandoned(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetNeedsUpdate(testPR, true)
	fc.RebaseErr = forge.ErrMergeConflict
	fk := dispatch.NewFake()
	fk.ResolveConflictErr = errors.New("exit status 137")
	s := newTestSettle(c, fc, fc)
	reg := s.Registry()
	fk.On("ResolveConflict", func(int) { reg.Mark("1") })

	err := s.mergeImmediate("1", 0, testPR, fk)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("mergeImmediate err = %v, want errAbandoned", err)
	}
	if errors.Is(err, errLandingNeverGreen) {
		t.Errorf("mergeImmediate err = %v, must not also match errLandingNeverGreen", err)
	}
	// No fc.Merged assertion: preflightStaleBase's conflict branch returns
	// through resolveConflict's own errAbandoned before mergeImmediate's loop
	// — the loop that calls cf.Merge — ever starts, so Merge is unreachable
	// on this path whether or not the guard above ran.
}

// The reviewer's own repro (issue #3523, BLOCK finding): with the shipped
// MERGE_MODE=manual default, a mark landing during gateToGreen's
// SUCCESS-confirm sleep (watch.go, inside w.clock.Sleep) must abandon before
// the gateGreen arm ever touches MarkReady or commits Complete. Before the
// fix, only MergeMode=immediate's own applyMergeMode entry guard covered a
// mark landing in this arm, so manual mode's mark here fell straight through
// to a Complete commit on an issue Reclaim had already moved to
// Dispatchable. Control: TestSelfHeal_MarksReadyBeforeManualHandoff runs the
// identical manual-mode green path with nothing marked and reaches
// landingManual, so this same path does commit when untouched.
func TestSelfHeal_TerminatedDuringSuccessConfirmSleep_ManualMode_ReportsAbandoned(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "manual"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	var s *Settle
	c.Clock = dispatch.Clock{Now: time.Now, Sleep: func(time.Duration) { s.Registry().Mark("1") }}
	s = newTestSettle(c, fc, fc)

	_, _, err := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("selfHeal err = %v, want errAbandoned", err)
	}
	assertNoPRWriteAfterTermination(t, fc)
}

// Pins completeLanding: a mark landing inside MarkReady's own round-trip is
// invisible to the top-of-arm guard (already past by the time MarkReady
// returns), so only the per-commit-site check catches it before the final
// success exit's Complete commit.
func TestSelfHeal_TerminatedDuringMarkReady_ReportsAbandoned(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "manual"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)
	fc.On("MarkReady", func(int) { s.Registry().Mark("1") })

	_, _, err := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("selfHeal err = %v, want errAbandoned", err)
	}
	assertNoTransitionOrComment(t, fc)
}

// The merge-guard-hit exit is its own Complete commit site (ready.go's second
// of four), so a mark landing while mergeGuardHit's own ListPRFiles round-trip
// is in flight must still abandon rather than commit agent-complete. The mark
// lands ahead of the branch's own Comment call, which still fires (no
// checkpoint sits between the guard-hit detection and it) — the finding's
// hole is the state commit, not this comment. Control:
// TestSelfHeal_MergeGuardHit_DowngradesToManual runs the identical guard-hit
// path with nothing marked and asserts the agent-complete label.
func TestSelfHeal_TerminatedDuringMergeGuardHit_ReportsAbandoned(t *testing.T) {
	c := baseConfig()
	c.MergeMode = "manual"
	c.MergeGuardPaths = ".github/**"
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	fc.SetPRFiles(testPR, []string{".github/workflows/ci.yml"})
	s := newTestSettle(c, fc, fc)
	fc.On("ListPRFiles", func(int) { s.Registry().Mark("1") })

	_, _, err := s.selfHeal(dispatch.NewFake(), "1", 0, testPR, nil)

	if !errors.Is(err, errAbandoned) {
		t.Errorf("selfHeal err = %v, want errAbandoned", err)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionState must not be called after termination; got %+v", fc.TransitionStateCalls)
	}
}

// Registry is the Settle's own and never changes identity, so a mark written
// through the handle any caller took is the mark the settle checkpoints read
// (#3522): before this, a second caller could install a registry of its own
// and strand the first caller's marks where nothing checks them.
func TestRegistry_StableHandleIsWhatCheckpointsRead(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := newTestSettle(c, fc, fc)

	if s.Registry() != s.Registry() {
		t.Fatal("Registry: want the same registry on every call")
	}
	s.Registry().Mark("1")

	got, _ := s.gateToGreen("1", 0, testPR, false)

	if !errors.Is(got.err, errAbandoned) {
		t.Errorf("gateToGreen = %v (err %v), want errAbandoned", got.outcome, got.err)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionState must not be called after termination; got %+v", fc.TransitionStateCalls)
	}
}
