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

// sequencedEval is a local freshness.Evaluator whose Eval call returns a
// different outPath on each successive call against the image attr.
// freshness.Fake has no built-in way to vary its response across calls (its
// OutPath field is a single, call-invariant value), and
// TestRunContinuousDispatch_BwrapNonConvergingSwap_HaltsHostTainted
// genuinely needs two DIFFERENT stale outpaths across fresh()'s two
// in-process calls to model a derivation that evaluates differently each
// time at the identical rev -- the host-taint signature ADR 0043 names.
// launcherAttr/launcherOutPath are a fixed side channel, not sequenced: the
// swap branch now requires the launcher dimension to be genuinely evaluated
// and fresh (issue #2682 review finding), so a call against launcherAttr
// must return the same outpath every time rather than consuming an entry
// from outPaths, which stays reserved for the image attr's own sequence.
// Defined locally rather than added to freshness.Fake since it's a narrow,
// single-test need. mu guards calls since RunContinuous may, in general,
// invoke fresh() from more than one goroutine over the run's life (even
// though every actual call here is serialized under RunContinuous's own
// mutex -- see fresh()'s own doc comment in main.go).
type sequencedEval struct {
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
// generation in place -- realize synchronously, bind the new generation via
// Factory.SetAgentGeneration, and keep refilling -- rather than draining and
// exiting with waves.ErrImageStale (exit 4) the way the OCI path (and the
// pre-#2682 bwrap path) does.
func TestRunContinuousDispatch_BwrapImageOnlyStale_HotSwapsAndKeepsRefilling(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded closure
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct -- never matches loadedHash

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
	// flakeLauncherAttr is configured and genuinely evaluated fresh: the
	// swap branch requires the launcher dimension to have actually been
	// probed (ADR 0043: "The launcher dimension of the probe is a
	// prerequisite for the swap, not a companion improvement to it" --
	// issue #2682 review finding), not merely defaulted true by an
	// unconfigured attr. staleEval below (freshness.Fake.OutPath) returns
	// the same outpath for every attr, so the launcher tip hash equals
	// staleHash too -- loadedLauncherHash is set to staleHash so the
	// launcher dimension is genuinely evaluated AND fresh, isolating
	// image-only staleness exactly like the previous unset-attr shape did.
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

// TestRunContinuousDispatch_BwrapBothStale_DrainsAsLauncherStale proves
// "when both moved, the launcher wins" (ADR 0043): a bwrap verdict where
// BOTH the image and the launcher dimensions are stale must still drain and
// exit with waves.ErrImageStale (exit 4), exactly like the pre-#2682 path --
// not attempt a swap. res.LauncherFresh false is what keeps this shape out
// of the hot-swap branch. It does NOT assert zero Realizer calls: the image
// dimension alone still genuinely diverges here (TipTag non-empty), so the
// pre-existing background realize (freshness.RealizeTip, issue #2679,
// unrelated to the swap) still fires on the drain path exactly as it always
// has -- this test only needs to prove no SWAP (no bound generation, no
// Box launch) was attempted.
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
	// No open issue at all: the stale verdict short-circuits the bootstrap
	// refill before any discover/dispatch happens, so this test needs no
	// dispatchable issue to observe the drain exit.
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

// TestRunContinuousDispatch_OCIImageOnlyStale_StillDrains proves "hot-swap is
// bwrap-only; the OCI path keeps the drain-exit unchanged" (ADR 0043): the
// exact image-only-stale/launcher-fresh shape that hot-swaps under bwrap
// (TestRunContinuousDispatch_BwrapImageOnlyStale_HotSwapsAndKeepsRefilling)
// must still drain and exit 4 under an OCI runnerKind. Asserted on the
// return code, not on realizeFake's call count: RealizeTip's own background
// (async, fire-and-forget) call may or may not have completed by the time
// runContinuousDispatch returns, so asserting a call count here would either
// race or duplicate TestRunContinuousDispatch_StaleRealizesTipInBackground's
// own coverage of that async path.
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
	// c.runnerKind left at its zero value ("") -- OCI-ish, per
	// TestRunContinuousDispatch_CleanSuccessPreservesHostTaintGuard_Halts's
	// own convention.

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
	// flakeLauncherAttr configured and genuinely fresh -- see
	// TestRunContinuousDispatch_BwrapImageOnlyStale_HotSwapsAndKeepsRefilling's
	// own comment for why this is required to reach the swap branch at all.
	c.flakeLauncherAttr = ".#launcher-currency"
	c.loadedLauncherHash = staleHash

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	// A dispatchable issue is present so a successful swap (the bug this
	// test guards against not happening) would actually reach dispatch --
	// making "zero RunCalls" a meaningful assertion below, not a vacuous one
	// from an empty queue.
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
// bwrap hot-swap can reach errImageHostTainted (exit 5) via the SAME
// guard.Classify mechanism the existing OCI drain path uses (issue #2113),
// not a second mechanism (ADR 0043: "It gets the same halt, not a second
// mechanism").
//
// Shape: one process, two in-process fresh() calls, not two separate
// runContinuousDispatch calls. Two separate processes can't reproduce a
// host-tainted SWAP -- a successful swap's currentImageTag only lives inside
// that one process's own closure, so a second process's Probe at the
// unchanged rev is a fresh divergence against the original baked tag, not
// specifically a repeat of the first process's own divergence. This test
// instead drives two dispatchable issues with maxParallel=1, so
// waves.RunContinuous's refill triggers fresh() twice within ONE
// runContinuousDispatch call: once for issue #1's bootstrap dispatch, and
// again once issue #1's Box completes and frees the slot for issue #2. A
// local sequencedEval (see its own doc comment above) returns a DIFFERENT
// stale outpath on the second Eval call than the first, modeling a
// host-realized derivation that evaluates differently each time at the
// identical rev -- so fresh()'s first call swaps successfully (Rebuild),
// and its second call, at the same rev with a new divergent outpath, hits
// guard.Classify's NonConverging case and halts HostTainted instead of
// swapping forever.
func TestRunContinuousDispatch_BwrapNonConvergingSwap_HaltsHostTainted(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111"         // 32 chars, the loaded closure
	const staleHash1 = "22222222222222222222222222222222"         // 32 chars, call 1's stale outpath
	const staleHash2 = "33333333333333333333333333333333"         // 32 chars, call 2's DIFFERENT stale outpath
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
	// flakeLauncherAttr configured and genuinely fresh on both fresh() calls
	// -- see sequencedEval's own doc comment for why launcherAttr/
	// launcherOutPath are a fixed side channel rather than part of the
	// image's own outPaths sequence.
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
// proves slice 2's fix for issue #2682's blocking bug: under a nixInBox
// Consumer (c.nixConfigFile set -- the same gate bwrapAdapter.IsReady/Run
// use to decide the /nix/var overlay is in play), a successful hot-swap must
// call the snapshotGeneration seam (main.go's own package-level seam over
// runner.SnapshotGeneration, mirroring bwrap.go's execCommand/statHostNixDB
// seam convention) with the swap's own pwd/closure BEFORE binding the
// generation onto the Factory -- otherwise every subsequent Box launch would
// fail bwrapAdapter.Run's "nix-var snapshot ... no longer exists" stat guard
// against a directory nothing ever wrote (ADR 0043: "A swap therefore adds a
// generation named for the closure it was taken against").
func TestRunContinuousDispatch_BwrapNixInBoxSwap_SnapshotsGenerationBeforeBinding(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded closure
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct -- never matches loadedHash

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
	// flakeLauncherAttr configured and genuinely fresh -- see
	// TestRunContinuousDispatch_BwrapImageOnlyStale_HotSwapsAndKeepsRefilling's
	// own comment for why this is required to reach the swap branch at all.
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
// proves the snapshotGeneration seam's failure path mirrors RealizeSync's
// own: a failed snapshot must fall back to the ordinary drain (exit 4)
// instead of binding a generation whose on-disk snapshot dir doesn't exist,
// which would only surface later as every subsequent Box launch's own
// "no longer exists" stat-guard failure instead of failing cleanly here
// (the same "a failed realize falls back to draining" acceptance criterion
// issue #2682 already established for RealizeSync, extended to this new
// failure mode).
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
	// flakeLauncherAttr configured and genuinely fresh -- see
	// TestRunContinuousDispatch_BwrapImageOnlyStale_HotSwapsAndKeepsRefilling's
	// own comment for why this is required to reach the swap branch at all.
	c.flakeLauncherAttr = ".#launcher-currency"
	c.loadedLauncherHash = staleHash

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	// A dispatchable issue is present so a successful swap (the bug this
	// test guards against not happening) would actually reach dispatch --
	// making "zero RunCalls" a meaningful assertion below, not a vacuous one
	// from an empty queue.
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

// TestRunContinuousDispatch_BwrapLauncherUnconfigured_FallsBackToDrain pins
// the swap branch's launcher-dimension prerequisite (issue #2682 review
// finding): Probe hard-codes LauncherFresh true whenever flakeLauncherAttr
// is unconfigured (freshness's own "not configured is not stale" contract),
// so without an explicit c.flakeLauncherAttr != "" gate an unconfigured
// launcher dimension would hot-swap forever with no incidental restart ever
// catching a launcher-side change (ADR 0043: "The launcher dimension of the
// probe is a prerequisite for the swap, not a companion improvement to
// it"). c.flakeLauncherAttr is left at its zero value here -- an otherwise
// textbook Box-only-stale shape must still drain like the pre-#2682 path.
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
	// c.flakeLauncherAttr left unset (baseConfig's zero value).

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	// A dispatchable issue is present so a successful swap (the bug this
	// test guards against) would actually reach dispatch -- making "zero
	// RunCalls" a meaningful assertion below, not a vacuous one from an
	// empty queue.
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
// strings.HasPrefix(res.TipTag, "/nix/store/") guard in fresh()'s swap
// branch (issue #2682 review finding): reachable whenever Probe's image eval
// yields an empty out-path (Applicable true, ImageFresh false, TipTag ""),
// which makes RealizeSync a genuine no-op (startRealize's own
// res.TipTag == "" guard, freshness/realize.go) rather than a failure -- so
// without this guard an empty TipTag would sail past RealizeSync's error
// check and get bound as a live generation with no real store path. The
// guard must instead fall back to the ordinary drain.
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
