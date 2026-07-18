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

// TestGateToGreen_TerminatedAbandonsWithoutTransition verifies that a
// termination marked before gateToGreen's first poll makes it bail
// immediately, without ever confirming green or swapping agent-complete —
// ADR 0024's "abandons the settle wherever it stands" applied to the CI-watch
// phase.
func TestGateToGreen_TerminatedAbandonsWithoutTransition(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)
	reg.Mark("1")

	got := s.gateToGreen("1", 0, testPR)

	if got != gateAbandoned {
		t.Errorf("gateToGreen = %v, want gateAbandoned", got)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionState must not be called after termination; got %+v", fc.TransitionStateCalls)
	}
}

// TestMergeImmediate_TerminatedDuringRewaitAfterStaleBasePreflight is the
// preflightStaleBase counterpart to
// TestMergeImmediate_TerminatedDuringRewaitAfterPlainRebase: termination
// lands between the proactive stale-base rebase and rewaitAfterForcePush's
// CI poll (added by fda1a20, in preflightStaleBase), and must report
// errAbandoned — never reaching mergeImmediate's own Merge call.
func TestMergeImmediate_TerminatedDuringRewaitAfterStaleBasePreflight(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetNeedsUpdate(testPR, true)
	reg := terminate.NewRegistry()
	tf := terminatingForge{Fake: fc, reg: reg, num: "1"}
	s := New(c, tf, tf)
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

// TestMergeImmediate_TerminatedStopsRebaseRetry verifies a termination marked
// before mergeImmediate's first attempt stops it from ever calling Merge or
// Rebase — the merge-gate phase of "abandons the settle wherever it stands."
func TestMergeImmediate_TerminatedStopsRebaseRetry(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 5
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	s := New(c, fc, fc)
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

// TestMergeImmediate_TerminatedBeforeStaleBasePreflightSkipsRebase verifies a
// termination marked before mergeImmediate is even called stops
// preflightStaleBase from issuing its proactive rebase at all (issue #943) —
// not merely from retrying after one already force-pushed. Before the fix,
// mergeImmediate called preflightStaleBase ahead of its own first
// s.terminated check, so a terminated issue with a stale base still got one
// branch-mutating rebase pushed.
func TestMergeImmediate_TerminatedBeforeStaleBasePreflightSkipsRebase(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 3
	c.PreflightStaleBase = true
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetNeedsUpdate(testPR, true)
	s := New(c, fc, fc)
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

// terminatingForge wraps a forge.Fake so its Rebase call marks num
// terminated after returning — simulating Terminate reaping the settle
// goroutine while a force-push is in flight, mirroring terminatingDispatcher
// below but for the code-forge seam.
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

// TestMergeImmediate_TerminatedDuringRewaitAfterPlainRebase verifies a
// termination landing between a successful rebase force-push and
// rewaitAfterForcePush's CI poll is reported as errAbandoned, not wrapped
// into errLandingNeverGreen — the gap issue #805 closes. Without the fix,
// gateToGreen's own gateAbandoned result gets collapsed into a generic
// "never went green" error, which selfHeal then mis-routes to
// landingFailed/agent-failed instead of landingAbandoned.
func TestMergeImmediate_TerminatedDuringRewaitAfterPlainRebase(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 5
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	reg := terminate.NewRegistry()
	tf := terminatingForge{Fake: fc, reg: reg, num: "1"}
	s := New(c, tf, tf)
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

// terminatingConflictResolver wraps a dispatch.Fake so its ResolveConflict
// call marks num terminated after returning — simulating Terminate reaping
// the settle goroutine right after a successful agent-assisted conflict
// resolve, before the post-force-push CI re-wait runs.
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

// TestMergeImmediate_TerminatedDuringRewaitAfterConflictResolve is the
// conflict-resolve counterpart to
// TestMergeImmediate_TerminatedDuringRewaitAfterPlainRebase: termination
// lands between a successful agent conflict-resolve and
// rewaitAfterForcePush's CI poll (in mergeImmediate's conflict-retry
// branch), and must report errAbandoned rather than errLandingNeverGreen.
func TestMergeImmediate_TerminatedDuringRewaitAfterConflictResolve(t *testing.T) {
	c := baseConfig()
	c.MaxRebaseAttempts = 5
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.MergeErrs = []error{forge.ErrMergeConflict}
	fc.RebaseErr = forge.ErrMergeConflict
	reg := terminate.NewRegistry()
	s := New(c, fc, fc)
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
// terminated after returning — simulating Terminate reaping the fix-pass Box
// mid-flight (the caller observes Fix's own failure result, then notices the
// termination on its next loop iteration).
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

// TestSelfHeal_TerminatedDuringFixPass_StopsRetryLoop verifies that a
// termination landing while a fix-pass Box is running (observed here as Fix
// returning, then the registry being marked) stops selfHeal from dispatching
// a second fix pass or re-polling CI — it abandons on the very next
// checkpoint instead of continuing the attempt loop.
func TestSelfHeal_TerminatedDuringFixPass_StopsRetryLoop(t *testing.T) {
	c := fixConfig(3)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	// Genuine red on every poll if the loop were ever allowed to continue —
	// proves termination is what stops it, not exhausted fix attempts.
	fc.SetCheckStates(testPR, []forge.RollupState{
		forge.StateFailure, forge.StateFailure, forge.StateFailure, forge.StateFailure,
	})
	s := New(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)
	d := terminatingDispatcher{Fake: dispatch.NewFake(), reg: reg, num: "1"}

	landing := s.selfHeal(d, "1", 0, testPR)

	if landing != landingAbandoned {
		t.Errorf("selfHeal = %v, want landingAbandoned", landing)
	}
	if len(d.FixCalls) != 1 {
		t.Errorf("want exactly 1 fix call (termination stops the retry loop), got %d: %+v", len(d.FixCalls), d.FixCalls)
	}
}

// TestSelfHeal_TerminatedDuringRewaitAfterForcePush_ReportsAbandoned verifies
// selfHeal's end-to-end handling of a termination landing during the
// post-force-push re-wait: it must report landingAbandoned, not
// landingFailed, and take none of landingFailed's side effects (no
// agent-failed transition, no "landing failed" comment) — the
// "no further action, Terminate already handled it" contract on
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
	s := New(c, tf, tf)
	s.SetTerminated(reg)

	landing := s.selfHeal(dispatch.NewFake(), "1", 0, testPR)

	if landing != landingAbandoned {
		t.Errorf("selfHeal = %v, want landingAbandoned", landing)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionState must not be called after termination; got %+v", fc.TransitionStateCalls)
	}
	if len(fc.CommentCalls) != 0 {
		t.Errorf("Comment must not be called after termination; got %+v", fc.CommentCalls)
	}
}

// TestGateToGreen_RepickDoesNotClearAnAbandonedSettlesMark reproduces the
// issue #743 race directly at the settle seam: an old, still-in-flight
// settle goroutine (holding the generation its dispatch was launched under)
// must keep seeing itself as terminated even after a re-pick has begun a
// fresh generation for the same issue number — the old blind Unmark would
// have erased the mark out from under it here; Begin must not.
func TestGateToGreen_RepickDoesNotClearAnAbandonedSettlesMark(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)

	oldGen := reg.Begin("1") // the original dispatch's own claim
	reg.Mark("1")            // Terminate marks that generation dead
	newGen := reg.Begin("1") // a re-pick's discover claims a fresh incarnation, mid-race

	if got := s.gateToGreen("1", oldGen, testPR); got != gateAbandoned {
		t.Errorf("gateToGreen(oldGen) = %v, want gateAbandoned — a re-pick must not erase an in-flight settle's own mark", got)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("TransitionState must not be called for the abandoned generation; got %+v", fc.TransitionStateCalls)
	}
	if reg.Marked("1", newGen) {
		t.Error("Marked(1, newGen) = true, want false — the re-pick's own fresh generation was never terminated")
	}
}

// TestSettle_AbandonedSkipsUsageComment verifies Settle's "ready" branch
// posts no usage comment when selfHeal reports landingAbandoned — Terminate
// already recorded its own comment; a second, unrelated comment from the
// orphaned settle goroutine would be noise the operator never asked for.
func TestSettle_AbandonedSkipsUsageComment(t *testing.T) {
	c := baseConfig()
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	s := New(c, fc, fc)
	reg := terminate.NewRegistry()
	s.SetTerminated(reg)
	reg.Mark("1")

	d := dispatch.NewFake()
	s.Settle(d, "1", 0, dispatch.Result{
		Success:      true,
		OutcomeFound: true,
		Outcome:      outcome.Outcome{Issue: "1", Landing: testPR, Status: "ready", Note: "ok"},
	})

	if len(fc.CommentCalls) != 0 {
		t.Errorf("no comment expected after termination; got %+v", fc.CommentCalls)
	}
}

// TestDocComments_NoHardcodedLineNumbers guards against #1174 regressing:
// doc-comments here once pinned another file's exact line number, which
// rots the instant that file shifts. Symbol names (function/type/const)
// are stable across edits; line numbers are not.
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
