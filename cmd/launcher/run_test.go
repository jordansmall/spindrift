package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
	"spindrift.dev/launcher/internal/testutil"
	"spindrift.dev/launcher/internal/waves"
)

// Exercises run's orchestration logic, not the bootstrap prologue, against a
// fake-populated launchContext with no ISSUE_NUMBER and no dispatchable issues.
func TestRun_EmptyQueue_ReturnsErrQueueEmpty(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)
	fc := forge.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	err := run(lc)

	if !errors.Is(err, errQueueEmpty) {
		t.Fatalf("run(lc) = %v, want errQueueEmpty", err)
	}
}

// Pins the translation from errQueueEmpty to exit code 2 that main once did
// inline, against a fake-populated launchContext with no bootstrap.
func TestRunExitCode_EmptyQueue_ReturnsExitCode2(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)
	fc := forge.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 2 {
		t.Errorf("runExitCode(lc) = %d, want 2 (errQueueEmpty)", got)
	}
}

// Regression test for #522/#477: with MAX_JOBS unset (0, the uncapped drain
// default) the queue path no longer loops dispatchWaves waiting for a blocker.
// A batch with nothing currently dispatchable exits straight to code 3.
func TestRunExitCode_QueueMaxJobsZero_NoneDispatchable_ReturnsExitCode3(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{
		Number: "1",
		Body:   "## Blocked by\n- #2",
		Labels: []string{c.label},
	})
	fc.SetIssue(forge.Issue{Number: "2", State: "OPEN"}) // blocker, not yet complete
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 3 {
		t.Errorf("runExitCode(lc) = %d, want 3 (ErrOpenNoneDispatchable)", got)
	}
}

// Regression test for #524: zero selected with issues held (here everything is
// overlap-deferred) exits 3, matching the queue path's ErrOpenNoneDispatchable
// translation, instead of the generic exit 1 every other selective-dispatch
// error uses.
func TestSelectiveDispatchExitCode_ZeroSelected_ReturnsExitCode3(t *testing.T) {
	c := baseConfig()
	c.label = "agent-trigger"
	c.overlapGate = "defer"
	dir := tempLogDir(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{
		Number: "10",
		Body:   "## Touches\n- lib/env-schema.nix",
		Labels: []string{c.label},
	})
	fc.SetIssue(forge.Issue{
		Number: "20",
		Body:   "## Touches\n- lib/env-schema.nix",
		State:  "OPEN",
		Labels: []string{c.inProgressLabel},
	})

	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := selectiveDispatchExitCode(lc, []string{"10"}, true); got != 3 {
		t.Errorf("selectiveDispatchExitCode(lc, [10], true) = %d, want 3 (ErrOpenNoneDispatchable)", got)
	}
}

// CONTINUOUS_DISPATCH must preserve exit-2 semantics unchanged (#527): an empty
// queue exits the same way whether or not continuous mode is enabled.
func TestRunExitCode_ContinuousDispatch_EmptyQueue_ReturnsExitCode2(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 1
	dir := tempLogDir(t)
	fc := forge.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 2 {
		t.Errorf("runExitCode(lc) = %d, want 2 (errQueueEmpty)", got)
	}
}

// A tracker failure on continuous mode's one startup query must surface as a raw
// error (exit 1 via runExitCode's generic fallback), the way the removed
// standalone precheck surfaced a failing discoverIssues call. Continuous mode must
// not swallow it into ErrOpenNoneDispatchable/exit 3 the way refill tolerates a
// later, mid-run discover failure.
func TestRun_ContinuousDispatch_StartupQueryError_Propagates(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 1
	dir := tempLogDir(t)
	fc := forge.NewFake()
	fc.ListIssuesErr = boxErr
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	err := run(lc)
	if !errors.Is(err, boxErr) {
		t.Fatalf("run(lc) = %v, want the raw ListIssuesErr", err)
	}
	if errors.Is(err, waves.ErrOpenNoneDispatchable) {
		t.Errorf("run(lc) = %v, must not flatten into ErrOpenNoneDispatchable", err)
	}
}

// Regression test for #1645's other half of the exit-code split: open issues
// exist but the only one is blocked, so continuous mode must still exit 3
// (ErrOpenNoneDispatchable) rather than folding into the empty-queue exit 2 now
// that both cases route through the same waves.RunContinuous call.
func TestRunExitCode_ContinuousDispatch_AllBlocked_ReturnsExitCode3(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 1
	dir := tempLogDir(t)
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{
		Number: "1",
		Body:   "## Blocked by\n- #2",
		Labels: []string{c.label},
	})
	fc.SetIssue(forge.Issue{Number: "2", State: "OPEN"}) // blocker, not yet complete
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 3 {
		t.Errorf("runExitCode(lc) = %d, want 3 (ErrOpenNoneDispatchable)", got)
	}
}

// With CONTINUOUS_DISPATCH enabled and the freshness probe reporting
// not-applicable (RUNNER_KIND=bwrap, which never blocks a refill), a dispatchable
// issue launches and the run exits 0.
func TestRunExitCode_ContinuousDispatch_Fresh_DispatchesAndReturns0(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = "bwrap"
	dir := tempLogDir(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})

	fr := runner.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, fr),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 0 {
		t.Errorf("runExitCode(lc) = %d, want 0", got)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Errorf("RunCalls: got %v, want exactly issue 1", fr.RunCalls)
	}
}

// Regression test for #1645: runContinuousDispatch used to run its own standalone
// empty-queue precheck query and then, unconditionally, discover's first call
// inside RunContinuous's bootstrap refill, printing two "==> querying open" lines
// before the first Box launched. Removing the precheck leaves exactly one.
func TestRunExitCode_ContinuousDispatch_QueriesTrackerOnceBeforeFirstDispatch(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = "bwrap"
	dir := tempLogDir(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})

	fr := runner.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, fr),
		settle:       settle.NewFake(),
	}

	out := testutil.CaptureStdout(t, func() {
		if got := runExitCode(lc); got != 0 {
			t.Errorf("runExitCode(lc) = %d, want 0", got)
		}
	})

	firstDispatch := strings.Index(out, "    -> #")
	if firstDispatch == -1 {
		t.Fatalf("no dispatch line found in output:\n%s", out)
	}
	before := strings.Count(out[:firstDispatch], "==> querying open")
	if before != 1 {
		t.Errorf("\"==> querying open\" appeared %d time(s) before the first dispatch line, want exactly 1:\n%s", before, out)
	}
}

// Regression test for #1666. This drives the real runContinuousDispatch and
// RunContinuous refill loop rather than calling logDiscoveryPoll directly, so it
// covers that wiring too. #1's Box adds #2 to the tracker mid-run, so the
// refill that picks up the freed slot must log exactly one line naming #2 and
// must not repeat the baseline "querying open" line.
func TestRunExitCode_ContinuousDispatch_RefillAnnouncesOnlyNewIssue(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = "bwrap"
	dir := tempLogDir(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})

	fr := runner.NewFake()
	fr.RunFunc = func(box runner.Box) error {
		if box.Issue == "1" {
			fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.label}})
		}
		return nil
	}
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, fr),
		settle:       settle.NewFake(),
	}

	out := testutil.CaptureStdout(t, func() {
		if got := runExitCode(lc); got != 0 {
			t.Errorf("runExitCode(lc) = %d, want 0", got)
		}
	})

	// The bootstrap poll's baseline line and the refill's new-issue announcement
	// share the "==> querying open" prefix by design, so distinguish them by the
	// "new:" suffix rather than by that shared prefix alone.
	total := strings.Count(out, "==> querying open")
	named := strings.Count(out, "new: #2")
	if total != 2 || named != 1 {
		t.Errorf("got %d \"==> querying open\" line(s) and %d \"new: #2\" line(s), want exactly one baseline line and one line naming #2:\n%s", total, named, out)
	}
}

// Regression test for #600: a bare agent-in-progress issue with an open non-draft
// PR is what a live runner's in-flight work looks like from the outside, the same
// shape a crash-stranded issue has. The discovered-origin sweep could not tell
// them apart and force-pushed/merged over the live runner. Two "runners" share one
// fake forge here, and this run must leave that issue's PR and labels untouched.
func TestRun_DoesNotAdoptLiveRunnersInProgressIssue(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.branchPrefix = "agent/issue-"
	dir := tempLogDir(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = c.branchPrefix
	// Issue #5 is another runner's live work: agent-in-progress with an open PR
	// and no explicit recovery signal.
	fc.SetIssue(forge.Issue{Number: "5", Labels: []string{c.inProgressLabel}})
	fc.SetPR(fc.AgentBranch("5"), forge.PR{URL: "https://github.com/owner/repo/pull/5"})

	sf := settle.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, nil),
		settle:       sf,
	}

	if err := run(lc); !errors.Is(err, errQueueEmpty) {
		t.Fatalf("run(lc) = %v, want errQueueEmpty (no other issue dispatchable)", err)
	}
	if len(sf.SettleAdoptedCalls) != 0 {
		t.Errorf("expected no SettleAdopted calls on a bare in-progress issue; got %v", sf.SettleAdoptedCalls)
	}
	if len(fc.TransitionStateCalls) != 0 {
		t.Errorf("expected no label churn on issue #5; got %v", fc.TransitionStateCalls)
	}
	if fc.Merged != "" {
		t.Errorf("expected no merge; fc.Merged=%q", fc.Merged)
	}
}

// Pins the exit code 4 case (#527): with the freshness probe reporting
// rebuild-needed, no Box launches. The test forces that verdict with a base-branch
// fetch that fails from a real repo whose configured origin is unreachable. #1579
// carves the not-a-repo case and #2034 the no-origin case out of this same
// fetch-failure path, so the fixture must be a genuine repo to still land here.
func TestRunExitCode_ContinuousDispatch_ImageStale_ReturnsExitCode4(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runtime = "podman"
	c.baseBranch = "main"
	dir := tempLogDir(t)
	if err := runGit(dir, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := runGit(dir, "remote", "add", "origin", "https://example.invalid/nope.git"); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})

	fr := runner.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, fr),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 4 {
		t.Errorf("runExitCode(lc) = %d, want 4 (waves.ErrImageStale)", got)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0 (no Box launches once the probe is stale)", len(fr.RunCalls))
	}
}

// Regression test for #2777: the stale-drain report's heldBack count comes from a
// reporting-only queue.Pending() call in main.go, never from the CLI's discover()
// closure. Forcing the tracker query to fail makes that call error, and the error
// must stay inert: no "==> querying open" line (that line belongs to a real poll),
// still exit 4, still no Box. Precedence itself is pinned by the tests below.
func TestRunExitCode_ContinuousDispatch_ImageStaleOnFirstRefillWithTransientDiscoverError_ReturnsExitCode4(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runtime = "podman"
	c.baseBranch = "main"
	dir := tempLogDir(t)
	if err := runGit(dir, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := runGit(dir, "remote", "add", "origin", "https://example.invalid/nope.git"); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	fc.ListIssuesErr = boxErr

	fr := runner.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, fr),
		settle:       settle.NewFake(),
	}

	out := testutil.CaptureStdout(t, func() {
		if got := runExitCode(lc); got != 4 {
			t.Errorf("runExitCode(lc) = %d, want 4 (waves.ErrImageStale)", got)
		}
	})
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0 (no Box launches once the probe is stale)", len(fr.RunCalls))
	}
	if strings.Contains(out, "==> querying open") {
		t.Errorf("stdout must not contain \"==> querying open\" (heldBack discover() call is reporting-only, not a real poll):\n%s", out)
	}
}

// Regression test for the #2939 review finding: main.go's headless pending closure
// reported a raw len(queryOpenIssues(...)) with no readiness filtering, so a
// candidate blocked by an unresolved edge inflated the heldBack count. #1 has no
// blocker and #2 is blocked by still-open #9, with the probe stale from the first
// refill. The pre-fix bug reported heldBack=2; this pins the fixed value, 1.
func TestRunExitCode_ContinuousDispatch_ImageStaleHeldBackExcludesBlockedIssue(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runtime = "podman"
	c.baseBranch = "main"
	dir := tempLogDir(t)
	if err := runGit(dir, "init"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	if err := runGit(dir, "remote", "add", "origin", "https://example.invalid/nope.git"); err != nil {
		t.Fatalf("git remote add: %v", err)
	}

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{
		Number: "2",
		Body:   "## Blocked by\n- #9",
		Labels: []string{c.label},
	})
	fc.SetIssue(forge.Issue{Number: "9", State: "OPEN"}) // #2's blocker, unmet

	fr := runner.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: fc,
		codeForge:    fc,
		factory:      testFactory(t, dir, fr),
		settle:       settle.NewFake(),
	}

	out := testutil.CaptureStdout(t, func() {
		if got := runExitCode(lc); got != 4 {
			t.Errorf("runExitCode(lc) = %d, want 4 (waves.ErrImageStale)", got)
		}
	})
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0 (no Box launches once the probe is stale)", len(fr.RunCalls))
	}
	if !strings.Contains(out, "1 issue(s) held back") {
		t.Errorf("stdout: got %q, want a drain report line reporting exactly 1 issue held back (issue #2 excluded by its unresolved blocker)", out)
	}
	if strings.Contains(out, "2 issue(s) held back") {
		t.Errorf("stdout: got %q, must not count issue #2 as held back while it's still blocked by open #9", out)
	}
}

// Proves continuousDispatchErr's top priority directly: a wrapped ErrImageStale
// wins even when firstQueryErr is a distinct, non-nil error. No end-to-end test
// can exercise this now that the stale-drain report's heldBack query never
// touches firstQueryErr (#2777).
func TestContinuousDispatchErr_ImageStaleWinsOverFirstQueryErr(t *testing.T) {
	err := fmt.Errorf("refill: %w", waves.ErrImageStale)
	firstQueryErr := errors.New("transient: tracker hiccup")

	got := continuousDispatchErr(err, firstQueryErr)

	if !errors.Is(got, waves.ErrImageStale) {
		t.Errorf("continuousDispatchErr(err, firstQueryErr) = %v, want errors.Is(got, waves.ErrImageStale)", got)
	}
}

// Proves continuousDispatchErr's second priority: when err is not ErrImageStale,
// a non-nil firstQueryErr wins over err itself.
func TestContinuousDispatchErr_FirstQueryErrWinsWhenNotStale(t *testing.T) {
	firstQueryErr := errors.New("first query: distinct sentinel")
	err := errors.New("some other refill error")

	got := continuousDispatchErr(err, firstQueryErr)

	if got != firstQueryErr {
		t.Errorf("continuousDispatchErr(err, firstQueryErr) = %v, want firstQueryErr (%v)", got, firstQueryErr)
	}
}

// Proves continuousDispatchErr's fallback: with neither ErrImageStale nor a
// firstQueryErr in play, the raw err passes through unchanged.
func TestContinuousDispatchErr_FallsBackToRawErr(t *testing.T) {
	err := errors.New("raw refill error")

	got := continuousDispatchErr(err, nil)

	if got != err {
		t.Errorf("continuousDispatchErr(err, nil) = %v, want err (%v)", got, err)
	}
}

// The batch dispatch path (`run`) threads NewReadiness's failed set (#1103)
// through to the wave engine: an issue whose own DepsOf call errored is held for
// retry, not dispatched and not cascade-failed, while an unaffected sibling in the
// same batch still dispatches.
func TestRun_DepsOfCheckFailure_HoldsIssueNotDispatched(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	dir := tempLogDir(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.label}})

	fr := runner.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: failDepsOf{Fake: fc, num: "1"},
		codeForge:    fc,
		factory:      testFactory(t, dir, fr),
		settle:       settle.NewFake(),
	}

	if err := run(lc); err != nil {
		t.Fatalf("run(lc): %v", err)
	}

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "2" {
		t.Fatalf("RunCalls: got %v, want exactly issue 2", fr.RunCalls)
	}

	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if containsLabel(iss1.Labels, c.failedLabel) {
		t.Errorf("issue 1 must NOT be cascade-failed on a DepsOf check failure; labels=%v", iss1.Labels)
	}
}

// CONTINUOUS_DISPATCH's discover closure threads NewReadiness's failed set (#1103)
// through to nextReady exactly as the batch path does: an issue whose own DepsOf
// call errored is held for retry rather than dispatched, while an unaffected
// sibling still dispatches.
func TestRunExitCode_ContinuousDispatch_DepsOfCheckFailure_HoldsIssueNotDispatched(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.continuousDispatch = true
	c.maxParallel = 2
	c.runnerKind = "bwrap"
	dir := tempLogDir(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	fc.SetIssue(forge.Issue{Number: "2", Labels: []string{c.label}})

	fr := runner.NewFake()
	lc := &launchContext{
		config:       c,
		pwd:          dir,
		issueTracker: failDepsOf{Fake: fc, num: "1"},
		codeForge:    fc,
		factory:      testFactory(t, dir, fr),
		settle:       settle.NewFake(),
	}

	if got := runExitCode(lc); got != 0 {
		t.Errorf("runExitCode(lc) = %d, want 0", got)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "2" {
		t.Fatalf("RunCalls: got %v, want exactly issue 2", fr.RunCalls)
	}

	iss1, err := fc.Issue("1")
	if err != nil {
		t.Fatalf("Issue(1): %v", err)
	}
	if containsLabel(iss1.Labels, c.failedLabel) {
		t.Errorf("issue 1 must NOT be cascade-failed on a DepsOf check failure; labels=%v", iss1.Labels)
	}
}
