package main

import (
	"errors"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// sequencedEval varies its outPath across successive Eval calls against the
// image attr, which freshness.Fake cannot do. Calls against launcherAttr are a
// fixed side channel that never consume the sequence: the swap branch needs a
// genuinely evaluated, fresh launcher dimension (issue #2682 review finding).
type sequencedEval struct {
	// mu guards Eval; RunContinuous may call fresh() from more than one goroutine.
	mu              sync.Mutex
	outPaths        []string
	calls           int
	launcherAttr    string
	launcherOutPath string
}

func (e *sequencedEval) Eval(pwd, rev, attr string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if attr == e.launcherAttr {
		return e.launcherOutPath, nil
	}
	i := e.calls
	if i >= len(e.outPaths) {
		i = len(e.outPaths) - 1
	}
	e.calls++
	return e.outPaths[i], nil
}

// TestRunContinuousDispatch_BwrapImageOnlyStale_HotSwapsAndKeepsRefilling is
// the core regression test for issue #2682 / ADR 0043: under bwrap, a
// stale-image/fresh-launcher verdict must hot-swap the agent-closure
// generation in place and keep refilling, rather than draining and exiting
// with waves.ErrImageStale (exit 4) the way the OCI path does.
func TestRunContinuousDispatch_BwrapImageOnlyStale_HotSwapsAndKeepsRefilling(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded closure
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct from loadedHash

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = freshness.KindBwrap
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#packages.x86_64-linux.agent-closure"
	c.imageTag = "/nix/store/" + loadedHash + "-agent-closure"
	// The swap branch requires the launcher dimension to have been genuinely
	// probed, not merely defaulted true by an unconfigured attr (ADR 0043,
	// issue #2682 review finding). staleEval returns the same outpath for every
	// attr, so setting loadedLauncherHash to staleHash makes the launcher
	// dimension both evaluated and fresh, isolating image-only staleness.
	c.flakeLauncherAttr = ".#launcher-currency"
	c.loadedLauncherHash = staleHash

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	it.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleOutPath := "/nix/store/" + staleHash + "-agent-closure"
	staleEval := &freshness.Fake{OutPath: staleOutPath}
	realizeFake := freshness.NewRealizerFake()

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if err != nil {
		t.Fatalf("runContinuousDispatch = %v, want nil -- a bwrap image-only-stale verdict must hot-swap and keep refilling, never drain-exit", err)
	}

	calls := realizeFake.CallsCopy()
	if len(calls) != 1 {
		t.Fatalf("realizeFake.CallsCopy() = %v, want exactly one synchronous RealizeSync call", calls)
	}

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("fr.RunCalls = %v, want exactly one Run call for issue #1 -- the hot-swap must not suppress dispatch", fr.RunCalls)
	}
	box := fr.RunCalls[0]
	if box.ClosureGeneration == nil {
		t.Fatalf("box.ClosureGeneration = nil, want the swapped generation bound onto the launched Box")
	}
	// staleOutPath is the agent-closure linkFarm's own store path (what
	// freshness.Probe reports as res.TipTag under bwrap); AgentFiles/AgentEnv
	// must be its "files"/"env" linkFarm children, not the closure path
	// itself (issue #2682 review finding).
	if want := staleOutPath + "/files"; box.ClosureGeneration.AgentFiles != want {
		t.Errorf("box.ClosureGeneration.AgentFiles = %q, want %q (the closure's \"files\" child)", box.ClosureGeneration.AgentFiles, want)
	}
	if want := staleOutPath + "/env"; box.ClosureGeneration.AgentEnv != want {
		t.Errorf("box.ClosureGeneration.AgentEnv = %q, want %q (the closure's \"env\" child)", box.ClosureGeneration.AgentEnv, want)
	}
	if want := staleOutPath + "/prefetch"; box.ClosureGeneration.PrefetchFile != want {
		t.Errorf("box.ClosureGeneration.PrefetchFile = %q, want %q (the closure's \"prefetch\" child)", box.ClosureGeneration.PrefetchFile, want)
	}
}

// TestRunContinuousDispatch_BwrapBothStale_DrainsAsLauncherStale proves "when
// both moved, the launcher wins" (ADR 0043): a bwrap verdict where both
// dimensions are stale must drain and exit with waves.ErrImageStale (exit 4),
// never swap. It asserts no swap rather than zero Realizer calls, because the
// unrelated background realize (issue #2679) still fires on the drain path.
func TestRunContinuousDispatch_BwrapBothStale_DrainsAsLauncherStale(t *testing.T) {
	const loadedImageHash = "11111111111111111111111111111111"    // 32 chars, the loaded closure
	const staleImageHash = "22222222222222222222222222222222"     // 32 chars, distinct
	const loadedLauncherHash = "33333333333333333333333333333333" // 32 chars, the loaded launcher
	const staleLauncherHash = "44444444444444444444444444444444"  // 32 chars, distinct

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = freshness.KindBwrap
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#packages.x86_64-linux.agent-closure"
	c.imageTag = "/nix/store/" + loadedImageHash + "-agent-closure"
	c.flakeLauncherAttr = ".#launcher-currency"
	c.loadedLauncherHash = loadedLauncherHash

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	// No open issue: the stale verdict short-circuits the bootstrap refill
	// before any discover or dispatch, so the drain exit is observable without
	// one.
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleEval := &freshness.Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-closure": "/nix/store/" + staleImageHash + "-agent-closure",
			"launcher-currency":                   "/nix/store/" + staleLauncherHash + "-launcher",
		},
	}
	realizeFake := freshness.NewRealizerFake()

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- a both-stale bwrap verdict must drain like the pre-#2682 path, not swap", got)
	}
	if got := f.AgentGeneration(); got != nil {
		t.Errorf("f.AgentGeneration() = %+v, want nil -- a both-stale verdict must never bind a swapped generation", got)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %v, want 0 (no Box launches once the probe is stale)", fr.RunCalls)
	}
}

// TestRunContinuousDispatch_OCIImageOnlyStale_StillDrains proves the hot-swap
// is bwrap-only (ADR 0043): the same image-only-stale, launcher-fresh shape
// that hot-swaps under bwrap must still drain and exit 4 under an OCI
// runnerKind. It asserts the return code, not realizeFake's call count, since
// RealizeTip's background call may not have finished by the time this returns.
func TestRunContinuousDispatch_OCIImageOnlyStale_StillDrains(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded image
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runtime = "podman"
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#image"
	c.imageTag = "spindrift:" + loadedHash
	// c.runnerKind is left at its zero value (""), which reads as OCI, matching
	// TestRunContinuousDispatch_CleanSuccessPreservesHostTaintGuard_Halts.

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	it.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleEval := &freshness.Fake{OutPath: "/nix/store/" + staleHash + "-img"}
	realizeFake := freshness.NewRealizerFake()

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- the OCI path must keep draining, never swap", got)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %v, want 0 (no Box launches once the probe is stale)", fr.RunCalls)
	}
}

// TestRunContinuousDispatch_BwrapRealizeFails_FallsBackToDrain proves the
// hot-swap branch's own failure path: when the synchronous RealizeSync call
// fails, the run must fall back to the ordinary drain (exit 4) instead of
// crashing or hanging, and must never bind a swapped generation onto the
// Factory.
func TestRunContinuousDispatch_BwrapRealizeFails_FallsBackToDrain(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded closure
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = freshness.KindBwrap
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#packages.x86_64-linux.agent-closure"
	c.imageTag = "/nix/store/" + loadedHash + "-agent-closure"
	// flakeLauncherAttr is configured and genuinely fresh. See
	// TestRunContinuousDispatch_BwrapImageOnlyStale_HotSwapsAndKeepsRefilling
	// for why the swap branch requires it.
	c.flakeLauncherAttr = ".#launcher-currency"
	c.loadedLauncherHash = staleHash

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	// A dispatchable issue is present so a successful swap, the bug this test
	// guards against, would reach dispatch. That makes "zero RunCalls" below a
	// meaningful assertion rather than a vacuous one from an empty queue.
	it.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleEval := &freshness.Fake{OutPath: "/nix/store/" + staleHash + "-agent-closure"}
	realizeFake := freshness.NewRealizerFake()
	realizeFake.Err = errors.New("boom: nix build failed")

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- a failed synchronous realize must fall back to the ordinary drain, not crash or hang", got)
	}
	if got := f.AgentGeneration(); got != nil {
		t.Errorf("f.AgentGeneration() = %+v, want nil -- a failed realize must never bind a swapped generation", got)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %v, want 0 (no Box launches once the probe is stale)", fr.RunCalls)
	}
}

// TestRunContinuousDispatch_BwrapNonConvergingSwap_HaltsHostTainted proves a
// bwrap hot-swap reaches errImageHostTainted (exit 5) through the same
// guard.Classify mechanism the OCI drain path uses (issue #2113, ADR 0043).
// Two issues with maxParallel=1 force two fresh() calls inside one run,
// because a swap's new tag lives only in that one process's own closure.
func TestRunContinuousDispatch_BwrapNonConvergingSwap_HaltsHostTainted(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111"         // 32 chars, the loaded closure
	const staleHash1 = "22222222222222222222222222222222"         // 32 chars, call 1's stale outpath
	const staleHash2 = "33333333333333333333333333333333"         // 32 chars, call 2's different stale outpath
	const loadedLauncherHash = "44444444444444444444444444444444" // 32 chars, the fixed, always-fresh launcher

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = freshness.KindBwrap
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#packages.x86_64-linux.agent-closure"
	c.imageTag = "/nix/store/" + loadedHash + "-agent-closure"
	// flakeLauncherAttr is configured and fresh on both fresh() calls. See
	// sequencedEval's doc comment for why launcherAttr and launcherOutPath are
	// a fixed side channel rather than part of the image's outPaths sequence.
	c.flakeLauncherAttr = ".#launcher-currency"
	c.loadedLauncherHash = loadedLauncherHash

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	it.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	it.SetIssue(forge.Issue{Number: "2", Labels: []string{c.label}})
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	eval := &sequencedEval{
		outPaths: []string{
			"/nix/store/" + staleHash1 + "-agent-closure",
			"/nix/store/" + staleHash2 + "-agent-closure",
		},
		launcherAttr:    "launcher-currency",
		launcherOutPath: "/nix/store/" + loadedLauncherHash + "-launcher",
	}
	realizeFake := freshness.NewRealizerFake()

	err := runContinuousDispatch(c, it, cf, dir, f, s, eval, realizeFake, lp)
	if got := exitCodeFor(err); got != 5 {
		t.Fatalf("exitCodeFor(err) = %d, want 5 (errImageHostTainted) -- an in-process bwrap swap that keeps re-diverging at the same rev must halt via the shared guard.Classify mechanism", got)
	}
	if got := freshness.NewGuard(dir).Prior(); got != "" {
		t.Errorf("freshness.NewGuard(dir).Prior() = %q, want empty -- Classify's own host-taint-halt path clears the recorded rev", got)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Errorf("fr.RunCalls = %v, want exactly one Run call for issue #1 -- issue #2 must never dispatch once the second fresh() call halts host-tainted", fr.RunCalls)
	}
}

// TestRunContinuousDispatch_BwrapNixInBoxSwap_SnapshotsGenerationBeforeBinding
// pins slice 2's fix for issue #2682's blocking bug: under a nixInBox Consumer
// (c.nixConfigFile set), a successful hot-swap must call snapshotGeneration
// with the swap's own pwd and closure before binding the generation, or every
// later Box launch fails bwrapAdapter.Run's "no longer exists" stat guard.
func TestRunContinuousDispatch_BwrapNixInBoxSwap_SnapshotsGenerationBeforeBinding(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded closure
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct from loadedHash

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = freshness.KindBwrap
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#packages.x86_64-linux.agent-closure"
	c.imageTag = "/nix/store/" + loadedHash + "-agent-closure"
	c.nixConfigFile = "/fake/nix.conf" // nixInBox on
	// flakeLauncherAttr is configured and genuinely fresh. See
	// TestRunContinuousDispatch_BwrapImageOnlyStale_HotSwapsAndKeepsRefilling
	// for why the swap branch requires it.
	c.flakeLauncherAttr = ".#launcher-currency"
	c.loadedLauncherHash = staleHash

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	it.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleOutPath := "/nix/store/" + staleHash + "-agent-closure"
	staleEval := &freshness.Fake{OutPath: staleOutPath}
	realizeFake := freshness.NewRealizerFake()

	origSnapshot := snapshotGeneration
	t.Cleanup(func() { snapshotGeneration = origSnapshot })
	var calls []struct{ pwd, closure string }
	snapshotGeneration = func(pwd, closure string) error {
		calls = append(calls, struct{ pwd, closure string }{pwd, closure})
		return nil
	}

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if err != nil {
		t.Fatalf("runContinuousDispatch = %v, want nil", err)
	}

	if len(calls) != 1 {
		t.Fatalf("snapshotGeneration calls = %v, want exactly one call", calls)
	}
	if calls[0].pwd != dir {
		t.Errorf("snapshotGeneration pwd = %q, want %q", calls[0].pwd, dir)
	}
	if calls[0].closure != staleOutPath {
		t.Errorf("snapshotGeneration closure = %q, want %q (res.TipTag)", calls[0].closure, staleOutPath)
	}

	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("fr.RunCalls = %v, want exactly one Run call for issue #1 -- the swap must still dispatch once the snapshot succeeds", fr.RunCalls)
	}
	box := fr.RunCalls[0]
	if box.ClosureGeneration == nil {
		t.Fatalf("box.ClosureGeneration = nil, want the swapped generation bound onto the launched Box")
	}
	if box.ClosureGeneration.Generation == "" {
		t.Errorf("box.ClosureGeneration.Generation = %q, want non-empty", box.ClosureGeneration.Generation)
	}
}

// TestRunContinuousDispatch_BwrapNixInBoxSwap_SnapshotGenerationFails_FallsBackToDrain
// proves the snapshotGeneration seam's failure path mirrors RealizeSync's: a
// failed snapshot must drain (exit 4) rather than bind a generation whose
// snapshot dir does not exist, which would otherwise appear only as every
// later Box launch's "no longer exists" stat-guard failure (issue #2682).
func TestRunContinuousDispatch_BwrapNixInBoxSwap_SnapshotGenerationFails_FallsBackToDrain(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded closure
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = freshness.KindBwrap
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#packages.x86_64-linux.agent-closure"
	c.imageTag = "/nix/store/" + loadedHash + "-agent-closure"
	c.nixConfigFile = "/fake/nix.conf" // nixInBox on
	// flakeLauncherAttr is configured and genuinely fresh. See
	// TestRunContinuousDispatch_BwrapImageOnlyStale_HotSwapsAndKeepsRefilling
	// for why the swap branch requires it.
	c.flakeLauncherAttr = ".#launcher-currency"
	c.loadedLauncherHash = staleHash

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	// A dispatchable issue is present so a successful swap, the bug this test
	// guards against, would reach dispatch. That makes "zero RunCalls" below a
	// meaningful assertion rather than a vacuous one from an empty queue.
	it.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleEval := &freshness.Fake{OutPath: "/nix/store/" + staleHash + "-agent-closure"}
	realizeFake := freshness.NewRealizerFake()

	origSnapshot := snapshotGeneration
	t.Cleanup(func() { snapshotGeneration = origSnapshot })
	snapshotGeneration = func(pwd, closure string) error {
		return errors.New("boom: vacuum into failed")
	}

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- a failed snapshotGeneration must fall back to the ordinary drain, not crash or hang", got)
	}
	if got := f.AgentGeneration(); got != nil {
		t.Errorf("f.AgentGeneration() = %+v, want nil -- a failed snapshotGeneration must never bind a swapped generation", got)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %v, want 0 (no Box launches once the probe is stale)", fr.RunCalls)
	}
}

// TestRunContinuousDispatch_BwrapLauncherUnconfigured_FallsBackToDrain pins the
// swap branch's launcher-dimension prerequisite (issue #2682 review finding):
// Probe hard-codes LauncherFresh true when flakeLauncherAttr is unconfigured,
// so without an explicit gate an otherwise textbook Box-only-stale shape would
// hot-swap forever and never catch a launcher-side change (ADR 0043).
func TestRunContinuousDispatch_BwrapLauncherUnconfigured_FallsBackToDrain(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded closure
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = freshness.KindBwrap
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#packages.x86_64-linux.agent-closure"
	c.imageTag = "/nix/store/" + loadedHash + "-agent-closure"
	// c.flakeLauncherAttr is deliberately left unset (baseConfig's zero value).

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	// A dispatchable issue is present so a successful swap, the bug this test
	// guards against, would reach dispatch. That makes "zero RunCalls" below a
	// meaningful assertion rather than a vacuous one from an empty queue.
	it.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleEval := &freshness.Fake{OutPath: "/nix/store/" + staleHash + "-agent-closure"}
	realizeFake := freshness.NewRealizerFake()

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- an unconfigured launcher dimension must never hot-swap, since Probe can't tell whether the launcher itself has moved", got)
	}
	if got := f.AgentGeneration(); got != nil {
		t.Errorf("f.AgentGeneration() = %+v, want nil -- an unconfigured launcher dimension must never bind a swapped generation", got)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %v, want 0 (no Box launches once the probe is stale)", fr.RunCalls)
	}
}

// TestRunContinuousDispatch_BwrapEmptyTipTag_FallsBackToDrain pins the
// strings.HasPrefix(res.TipTag, "/nix/store/") guard in fresh()'s swap branch
// (issue #2682 review finding): an empty image out-path makes RealizeSync a
// no-op rather than a failure, so without the guard the swap would bind an
// empty TipTag as a live generation with no store path instead of draining.
func TestRunContinuousDispatch_BwrapEmptyTipTag_FallsBackToDrain(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111"         // 32 chars, the loaded closure
	const loadedLauncherHash = "44444444444444444444444444444444" // 32 chars, the loaded launcher

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = freshness.KindBwrap
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#packages.x86_64-linux.agent-closure"
	c.imageTag = "/nix/store/" + loadedHash + "-agent-closure"
	c.flakeLauncherAttr = ".#launcher-currency"
	c.loadedLauncherHash = loadedLauncherHash

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	it.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleEval := &freshness.Fake{OutPathForAttr: map[string]string{
		"packages.x86_64-linux.agent-closure": "", // image eval yields an empty out-path
		"launcher-currency":                   "/nix/store/" + loadedLauncherHash + "-launcher",
	}}
	realizeFake := freshness.NewRealizerFake()

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- an empty realized tip tag must fall back to the ordinary drain, not bind a generation with no store path", got)
	}
	if got := f.AgentGeneration(); got != nil {
		t.Errorf("f.AgentGeneration() = %+v, want nil -- an empty tip tag must never bind a swapped generation", got)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %v, want 0 (no Box launches once the probe is stale)", fr.RunCalls)
	}
	if calls := realizeFake.CallsCopy(); len(calls) != 0 {
		t.Errorf("realizeFake.CallsCopy() = %v, want none -- startRealize's own res.TipTag == \"\" guard makes RealizeSync a no-op", calls)
	}
}
