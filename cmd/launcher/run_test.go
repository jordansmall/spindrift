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

// Exercises run's orchestration logic, not the bootstrap prologue: the
// launchContext is fake-populated, with no ISSUE_NUMBER set.
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

// Pins the errQueueEmpty -> 2 translation against a fake-populated
// launchContext -- no bootstrap, no real config or runtime.
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

// Regression for #522/#477: with MAX_JOBS unset (0, the uncapped drain
// default) the queue path must not loop dispatchWaves waiting for a blocker —
// a batch with nothing currently dispatchable exits straight to code 3.
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

// Regression for #524: zero selected with issues held (here, everything
// overlap-deferred) exits 3, matching the queue path's
// ErrOpenNoneDispatchable translation, not the generic exit 1 every other
// selective-dispatch error uses.
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

// An empty queue exits the same way whether or not continuous mode is
// enabled (#527).
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

// A tracker failure on continuous mode's one startup query surfaces as a raw
// error (exit 1), not swallowed into ErrOpenNoneDispatchable/exit 3 the way
// refill tolerates a later, mid-run discover failure.
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

// #1645's other half of the exit-code split: open issues exist but the only
// one is blocked, so continuous mode must still exit 3
// (ErrOpenNoneDispatchable) rather than folding into the empty-queue exit 2
// now that both cases route through the same waves.RunContinuous call.
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

// RUNNER_KIND=bwrap makes the freshness probe report not-applicable, which
// never blocks a refill — so this exercises continuous mode end-to-end
// through run/runExitCode with nothing held back.
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

// Regression for #1645: a standalone empty-queue precheck plus RunContinuous's
// own bootstrap refill produced two "==> querying open" lines before the first
// Box ever launched. Exactly one is correct.
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

// Regression for #1666, driven through the real runContinuousDispatch/
// RunContinuous refill loop rather than calling logDiscoveryPoll directly: the
// bootstrap poll sees only #1 and logs the baseline line; #1's Box adds #2 to
// the tracker mid-run (simulating an issue appearing between polls) before
// finishing, so the refill that picks up the freed slot sees #2 for the first
// time. That refill must log exactly one line naming #2, and must not repeat
// the baseline "querying open" line for a poll that isn't the first.
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

	// The bootstrap poll's baseline line and the refill's new-issue
	// announcement share the "==> querying open" prefix by design (both are
	// discovery-query lines), so distinguish them by the "new:" suffix
	// rather than by that shared prefix alone.
	total := strings.Count(out, "==> querying open")
	named := strings.Count(out, "new: #2")
	if total != 2 || named != 1 {
		t.Errorf("got %d \"==> querying open\" line(s) and %d \"new: #2\" line(s), want exactly one baseline line and one line naming #2:\n%s", total, named, out)
	}
}

// Regression for #600: a bare agent-in-progress issue with an open non-draft
// PR is what a live runner's in-flight work looks like from the outside (a
// second local dogfood run, or an overlapping agent-dispatch box) — the same
// shape a crash-stranded issue has. The discovered-origin sweep could not tell
// the two apart and force-pushed/merged over the live runner. Two "runners"
// share one fake forge here; the live one's PR and labels must stay untouched.
func TestRun_DoesNotAdoptLiveRunnersInProgressIssue(t *testing.T) {
	c := baseConfig()
	c.label = "ready-for-agent"
	c.branchPrefix = "agent/issue-"
	dir := tempLogDir(t)

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = c.branchPrefix
	// Issue #5: another runner's live work — agent-in-progress with an open
	// PR, no explicit recovery signal.
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

// Staleness (#527) is forced by a base-branch fetch that fails. The fixture
// must be a genuine git repo with a configured-but-unreachable "origin": issue
// #1579 carves the not-a-git-repo case, and issue #2034 the no-origin-remote
// case, out of this same fetch-failure path into a not-applicable verdict, so
// only a genuinely transient failure still lands on rebuild-needed.
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

// Regression for #2777: the stale-drain report's heldBack count comes from a
// separate, reporting-only queue.Pending() call (main.go's pending closure),
// never the CLI's discover() closure. fc.ListIssuesErr makes that heldBack
// call — fired the first time staleness is detected — itself error, and that
// error must be fully inert: no "==> querying open" line (that belongs to a
// real poll), and no change to the exit code, which stays 4.
//
// Because heldBack no longer runs through discover(), it can no longer set
// firstQueryErr, so no production path leaves ErrImageStale and firstQueryErr
// both non-nil. The precedence between them is kept as documented intent (see
// continuousDispatchErr in main.go) and pinned in isolation by
// TestContinuousDispatchErr_ImageStaleWinsOverFirstQueryErr below.
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

// Regression for the #2939 review finding: the headless pending closure
// (main.go) reported a raw len(queryOpenIssues(...)) with no readiness
// filtering, so a candidate blocked by an unresolved edge inflated the
// stale-drain report's heldBack count as if it were dispatchable. #1 has no
// blocker, #2 is blocked by still-open #9, and the freshness probe is stale
// from the first refill (same runtime=podman/no-reachable-origin setup as
// TestRunExitCode_ContinuousDispatch_ImageStale_ReturnsExitCode4), so the
// heldBack query fires before any Box launches. Pre-fix it reported 2.
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

// continuousDispatchErr's top priority. No end-to-end test can reach this
// combination any more, now that the stale-drain report's heldBack query never
// touches firstQueryErr (issue #2777), so it is pinned directly here.
func TestContinuousDispatchErr_ImageStaleWinsOverFirstQueryErr(t *testing.T) {
	err := fmt.Errorf("refill: %w", waves.ErrImageStale)
	firstQueryErr := errors.New("transient: tracker hiccup")

	got := continuousDispatchErr(err, firstQueryErr)

	if !errors.Is(got, waves.ErrImageStale) {
		t.Errorf("continuousDispatchErr(err, firstQueryErr) = %v, want errors.Is(got, waves.ErrImageStale)", got)
	}
}

// continuousDispatchErr's second priority: the startup-query-failure surfacing
// runContinuousDispatch's doc comment describes.
func TestContinuousDispatchErr_FirstQueryErrWinsWhenNotStale(t *testing.T) {
	firstQueryErr := errors.New("first query: distinct sentinel")
	err := errors.New("some other refill error")

	got := continuousDispatchErr(err, firstQueryErr)

	if got != firstQueryErr {
		t.Errorf("continuousDispatchErr(err, firstQueryErr) = %v, want firstQueryErr (%v)", got, firstQueryErr)
	}
}

func TestContinuousDispatchErr_FallsBackToRawErr(t *testing.T) {
	err := errors.New("raw refill error")

	got := continuousDispatchErr(err, nil)

	if got != err {
		t.Errorf("continuousDispatchErr(err, nil) = %v, want err (%v)", got, err)
	}
}

// The batch dispatch path threads NewReadiness's failed set (#1103) through to
// the wave engine: an issue whose DepsOf call errored is held for retry, not
// dispatched and not cascade-failed, while an unaffected sibling still goes.
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

// CONTINUOUS_DISPATCH's discover closure must thread NewReadiness's failed set
// (#1103) through to nextReady exactly as the batch path does.
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
