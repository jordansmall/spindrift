package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
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

// TestRunContinuousDispatch_StoreClosureImageOnlyStale_HotSwapsAndKeepsRefilling is
// the core regression test for issue #2682 / ADR 0043: under bwrap, a
// stale-image/fresh-launcher verdict must hot-swap the agent-closure
// generation in place and keep refilling, rather than draining and exiting
// with waves.ErrImageStale (exit 4) the way the OCI path does.
func TestRunContinuousDispatch_StoreClosureImageOnlyStale_HotSwapsAndKeepsRefilling(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded closure
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct from loadedHash

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = runner.KindBwrap
	c.boxArtifact = "store-closure"
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

	var stdout bytes.Buffer
	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("runContinuousDispatch = %v, want nil -- a bwrap image-only-stale verdict must hot-swap and keep refilling, never drain-exit", err)
	}
	if want := "==> store-closure hot-swap: swapped in agent closure to main tip"; !strings.Contains(stdout.String(), want) {
		t.Errorf("stdout = %q, want it to contain %q", stdout.String(), want)
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

// TestRunContinuousDispatch_StoreClosureBothStale_DrainsAsLauncherStale proves "when
// both moved, the launcher wins" (ADR 0043): a bwrap verdict where both
// dimensions are stale must drain and exit with waves.ErrImageStale (exit 4),
// never swap. It asserts no swap rather than zero Realizer calls, because the
// unrelated background realize (issue #2679) still fires on the drain path.
func TestRunContinuousDispatch_StoreClosureBothStale_DrainsAsLauncherStale(t *testing.T) {
	const loadedImageHash = "11111111111111111111111111111111"    // 32 chars, the loaded closure
	const staleImageHash = "22222222222222222222222222222222"     // 32 chars, distinct
	const loadedLauncherHash = "33333333333333333333333333333333" // 32 chars, the loaded launcher
	const staleLauncherHash = "44444444444444444444444444444444"  // 32 chars, distinct

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = runner.KindBwrap
	c.boxArtifact = "store-closure"
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

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp, io.Discard, io.Discard)
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

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp, io.Discard, io.Discard)
	if got := exitCodeFor(err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- the OCI path must keep draining, never swap", got)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %v, want 0 (no Box launches once the probe is stale)", fr.RunCalls)
	}
}

// TestRunContinuousDispatch_BwrapRunnerOCIArtifact_StillDrains proves the
// hot-swap gate reads the Box artifact, never the runner family: the same
// image-only-stale, launcher-fresh shape that swaps under a store-closure
// artifact drains and exits 4 when the artifact is an OCI image, even with
// runnerKind bwrap.
func TestRunContinuousDispatch_BwrapRunnerOCIArtifact_StillDrains(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded closure
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = runner.KindBwrap
	c.boxArtifact = "oci-image"
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#packages.x86_64-linux.agent-closure"
	c.imageTag = "/nix/store/" + loadedHash + "-agent-closure"
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

	staleEval := &freshness.Fake{OutPath: "/nix/store/" + staleHash + "-agent-closure"}
	realizeFake := freshness.NewRealizerFake()

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp, io.Discard, io.Discard)
	if got := exitCodeFor(err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- the runner family must not enable the swap", got)
	}
	if got := f.AgentGeneration(); got != nil {
		t.Errorf("f.AgentGeneration() = %+v, want nil -- an OCI artifact must never bind a swapped generation", got)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %v, want 0 (no Box launches once the probe is stale)", fr.RunCalls)
	}
}

// TestRunContinuousDispatch_StoreClosureRealizeFails_FallsBackToDrain proves the
// hot-swap branch's own failure path: when the synchronous RealizeSync call
// fails, the run must fall back to the ordinary drain (exit 4) instead of
// crashing or hanging, and must never bind a swapped generation onto the
// Factory.
func TestRunContinuousDispatch_StoreClosureRealizeFails_FallsBackToDrain(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded closure
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = runner.KindBwrap
	c.boxArtifact = "store-closure"
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#packages.x86_64-linux.agent-closure"
	c.imageTag = "/nix/store/" + loadedHash + "-agent-closure"
	// flakeLauncherAttr is configured and genuinely fresh. See
	// TestRunContinuousDispatch_StoreClosureImageOnlyStale_HotSwapsAndKeepsRefilling
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

	var stderr bytes.Buffer
	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp, io.Discard, &stderr)
	if want := "==> store-closure hot-swap: realize failed, draining instead"; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
	}
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

// TestRunContinuousDispatch_StoreClosureNonConvergingSwap_HaltsHostTainted proves a
// store-closure hot-swap reaches errImageHostTainted (exit 5) through the same
// guard.Classify mechanism the OCI drain path uses (issue #2113, ADR 0043).
// Two issues with maxParallel=1 force two fresh() calls inside one run,
// because a swap's new tag lives only in that one process's own closure.
func TestRunContinuousDispatch_StoreClosureNonConvergingSwap_HaltsHostTainted(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111"         // 32 chars, the loaded closure
	const staleHash1 = "22222222222222222222222222222222"         // 32 chars, call 1's stale outpath
	const staleHash2 = "33333333333333333333333333333333"         // 32 chars, call 2's different stale outpath
	const loadedLauncherHash = "44444444444444444444444444444444" // 32 chars, the fixed, always-fresh launcher

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = runner.KindBwrap
	c.boxArtifact = "store-closure"
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

	err := runContinuousDispatch(c, it, cf, dir, f, s, eval, realizeFake, lp, io.Discard, io.Discard)
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

// nixInBoxSwapResult is what runNixInBoxSwap observed.
type nixInBoxSwapResult struct {
	err    error
	f      *dispatch.Factory
	fr     *runner.Fake
	stdout string
	stderr string
	stale  string // the swap's target closure (res.TipTag)
	dir    string
}

// strategyWithHook answers lookupRunnerStrategy with a bare Strategy carrying
// only hook as its PrepareHotSwap, the one field hot-swap reads.
func strategyWithHook(hook func(cfg runner.Config, pwd, closure string) error) func(string) (runner.Strategy, error) {
	return func(string) (runner.Strategy, error) {
		return runner.Strategy{PrepareHotSwap: hook}, nil
	}
}

// runNixInBoxSwap drives one continuous dispatch whose Box-only-stale probe
// hot-swaps a store-closure, with nixInBox (c.nixConfigFile set) on or off and
// lookup answering the runner family.
func runNixInBoxSwap(t *testing.T, nixInBox bool, lookup func(string) (runner.Strategy, error)) nixInBoxSwapResult {
	t.Helper()
	return runNixInBoxSwapTo(t, nixInBox, lookup, false)
}

// runNixInBoxSwapTo is runNixInBoxSwap with emptyTipTag making the image eval
// yield an empty out-path, the one non-store tip tag Probe can hand back.
func runNixInBoxSwapTo(t *testing.T, nixInBox bool, lookup func(string) (runner.Strategy, error), emptyTipTag bool) nixInBoxSwapResult {
	t.Helper()
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded closure
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct from loadedHash

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = runner.KindBwrap
	c.boxArtifact = "store-closure"
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#packages.x86_64-linux.agent-closure"
	c.imageTag = "/nix/store/" + loadedHash + "-agent-closure"
	c.passwdFile = "/nix/store/passwd-fixture"
	if nixInBox {
		c.nixConfigFile = "/fake/nix.conf"
	}
	// flakeLauncherAttr is configured and genuinely fresh. See
	// TestRunContinuousDispatch_StoreClosureImageOnlyStale_HotSwapsAndKeepsRefilling
	// for why the swap branch requires it.
	c.flakeLauncherAttr = ".#launcher-currency"
	c.loadedLauncherHash = staleHash

	orig := lookupRunnerStrategy
	t.Cleanup(func() { lookupRunnerStrategy = orig })
	lookupRunnerStrategy = lookup

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	// A dispatchable issue is present so a swap that wrongly binds would reach
	// dispatch, making "zero RunCalls" meaningful rather than vacuous.
	it.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	staleOutPath := "/nix/store/" + staleHash + "-agent-closure"
	staleEval := &freshness.Fake{OutPath: staleOutPath}
	if emptyTipTag {
		staleEval.OutPathForAttr = map[string]string{"packages.x86_64-linux.agent-closure": ""}
	}

	var stdout, stderr bytes.Buffer
	err := runContinuousDispatch(c, it, it, dir, f, settle.NewFake(), staleEval, freshness.NewRealizerFake(), fakeLiveness{}, &stdout, &stderr)
	return nixInBoxSwapResult{err: err, f: f, fr: fr, stdout: stdout.String(), stderr: stderr.String(), stale: staleOutPath, dir: dir}
}

// TestRunContinuousDispatch_StoreClosureNixInBoxSwap_PreparesClosureBeforeBinding
// pins slice 2's fix for issue #2682's blocking bug: under a nixInBox Consumer
// a successful hot-swap must call the selected family's PrepareHotSwap with the
// swap's own pwd and closure before binding the generation, or every later Box
// launch fails bwrapAdapter.Run's "no longer exists" stat guard.
func TestRunContinuousDispatch_StoreClosureNixInBoxSwap_PreparesClosureBeforeBinding(t *testing.T) {
	var calls []struct{ pwd, closure string }
	var gotCfg runner.Config
	r := runNixInBoxSwap(t, true, strategyWithHook(func(cfg runner.Config, pwd, closure string) error {
		gotCfg = cfg
		calls = append(calls, struct{ pwd, closure string }{pwd, closure})
		return nil
	}))
	if r.err != nil {
		t.Fatalf("runContinuousDispatch = %v, want nil", r.err)
	}

	if len(calls) != 1 {
		t.Fatalf("PrepareHotSwap calls = %v, want exactly one call", calls)
	}
	if calls[0].pwd != r.dir {
		t.Errorf("PrepareHotSwap pwd = %q, want %q", calls[0].pwd, r.dir)
	}
	if gotCfg.PasswdFile != "/nix/store/passwd-fixture" {
		t.Errorf("PrepareHotSwap cfg.PasswdFile = %q, want the startup config's passwd", gotCfg.PasswdFile)
	}
	if calls[0].closure != r.stale {
		t.Errorf("PrepareHotSwap closure = %q, want %q (res.TipTag)", calls[0].closure, r.stale)
	}

	if len(r.fr.RunCalls) != 1 || r.fr.RunCalls[0].Issue != "1" {
		t.Fatalf("fr.RunCalls = %v, want exactly one Run call for issue #1 -- the swap must still dispatch once preparation succeeds", r.fr.RunCalls)
	}
	box := r.fr.RunCalls[0]
	if box.ClosureGeneration == nil {
		t.Fatalf("box.ClosureGeneration = nil, want the swapped generation bound onto the launched Box")
	}
	if box.ClosureGeneration.Generation == "" {
		t.Errorf("box.ClosureGeneration.Generation = %q, want non-empty", box.ClosureGeneration.Generation)
	}
}

// TestRunContinuousDispatch_StoreClosureNixInBoxSwap_NilHook_SwapsWithoutPreparation
// pins ADR 0069 decision 2: a family with no PrepareHotSwap needs no
// preparation, so a nixInBox hot-swap binds the generation without one.
func TestRunContinuousDispatch_StoreClosureNixInBoxSwap_NilHook_SwapsWithoutPreparation(t *testing.T) {
	r := runNixInBoxSwap(t, true, strategyWithHook(nil))
	if r.err != nil {
		t.Fatalf("runContinuousDispatch = %v, want nil", r.err)
	}
	if !strings.Contains(r.stdout, "hot-swap: swapped in agent") {
		t.Errorf("stdout = %q, want the swapped-in message", r.stdout)
	}
	if len(r.fr.RunCalls) != 1 || r.fr.RunCalls[0].ClosureGeneration == nil {
		t.Fatalf("fr.RunCalls = %v, want one Run call carrying the swapped generation", r.fr.RunCalls)
	}
}

// TestRunContinuousDispatch_StoreClosureSwap_NotNixInBox_SkipsPreparation pins
// that the nixInBox gate stays in core: with it off, a family's hook is never
// called.
func TestRunContinuousDispatch_StoreClosureSwap_NotNixInBox_SkipsPreparation(t *testing.T) {
	called := false
	r := runNixInBoxSwap(t, false, strategyWithHook(func(runner.Config, string, string) error {
		called = true
		return nil
	}))
	if r.err != nil {
		t.Fatalf("runContinuousDispatch = %v, want nil", r.err)
	}
	if called {
		t.Error("PrepareHotSwap was called with nixInBox off")
	}
	if len(r.fr.RunCalls) != 1 || r.fr.RunCalls[0].ClosureGeneration == nil {
		t.Fatalf("fr.RunCalls = %v, want one Run call carrying the swapped generation", r.fr.RunCalls)
	}
}

// TestRunContinuousDispatch_StoreClosureNixInBoxSwap_PrepareFails_FallsBackToDrain
// proves the hook's failure path mirrors RealizeSync's: a failed preparation
// must drain (exit 4) rather than bind a generation whose snapshot dir does
// not exist, which would otherwise appear only as every later Box launch's
// "no longer exists" stat-guard failure (issue #2682).
func TestRunContinuousDispatch_StoreClosureNixInBoxSwap_PrepareFails_FallsBackToDrain(t *testing.T) {
	r := runNixInBoxSwap(t, true, strategyWithHook(func(runner.Config, string, string) error {
		return errors.New("boom: vacuum into failed")
	}))
	if got := exitCodeFor(r.err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- a failed PrepareHotSwap must fall back to the ordinary drain, not crash or hang", got)
	}
	if !strings.Contains(r.stderr, "snapshot generation failed, draining instead: boom: vacuum into failed") {
		t.Errorf("stderr = %q, want the snapshot-failed drain message", r.stderr)
	}
	if got := r.f.AgentGeneration(); got != nil {
		t.Errorf("f.AgentGeneration() = %+v, want nil -- a failed PrepareHotSwap must never bind a swapped generation", got)
	}
	if len(r.fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %v, want 0 (no Box launches once the probe is stale)", r.fr.RunCalls)
	}
}

// TestRunContinuousDispatch_StoreClosureNixInBoxSwap_UnknownFamily_FallsBackToDrain
// pins that an unresolvable runner family drains rather than binding an
// unprepared generation.
func TestRunContinuousDispatch_StoreClosureNixInBoxSwap_UnknownFamily_FallsBackToDrain(t *testing.T) {
	r := runNixInBoxSwap(t, true, func(string) (runner.Strategy, error) {
		return runner.Strategy{}, errors.New("no such family")
	})
	if got := exitCodeFor(r.err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4", got)
	}
	if !strings.Contains(r.stderr, "runner family lookup failed, draining instead: no such family") {
		t.Errorf("stderr = %q, want the family-lookup drain message", r.stderr)
	}
	if got := r.f.AgentGeneration(); got != nil {
		t.Errorf("f.AgentGeneration() = %+v, want nil", got)
	}
	if len(r.fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %v, want 0 (no Box launches once the probe is stale)", r.fr.RunCalls)
	}
}

// TestRunContinuousDispatch_StoreClosureNixInBoxSwap_EmptyTipTag_SkipsPreparation
// pins that the store-path rejection runs before the hook: an empty tip tag
// drains without ever reaching the runner family's preparation.
func TestRunContinuousDispatch_StoreClosureNixInBoxSwap_EmptyTipTag_SkipsPreparation(t *testing.T) {
	called := false
	r := runNixInBoxSwapTo(t, true, strategyWithHook(func(runner.Config, string, string) error {
		called = true
		return nil
	}), true)
	if got := exitCodeFor(r.err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- an empty tip tag must drain", got)
	}
	if !strings.Contains(r.stderr, "is not a nix store path, draining instead") {
		t.Errorf("stderr = %q, want the non-store-path drain message", r.stderr)
	}
	if called {
		t.Error("PrepareHotSwap was called for a tip tag that is not a store path")
	}
	if got := r.f.AgentGeneration(); got != nil {
		t.Errorf("f.AgentGeneration() = %+v, want nil", got)
	}
	if len(r.fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %v, want 0 (no Box launches once the probe is stale)", r.fr.RunCalls)
	}
}

// TestRunContinuousDispatch_StoreClosureLauncherUnconfigured_FallsBackToDrain pins the
// swap branch's launcher-dimension prerequisite (issue #2682 review finding):
// Probe hard-codes LauncherFresh true when flakeLauncherAttr is unconfigured,
// so without an explicit gate an otherwise textbook Box-only-stale shape would
// hot-swap forever and never catch a launcher-side change (ADR 0043).
func TestRunContinuousDispatch_StoreClosureLauncherUnconfigured_FallsBackToDrain(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded closure
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = runner.KindBwrap
	c.boxArtifact = "store-closure"
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

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp, io.Discard, io.Discard)
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

// TestRunContinuousDispatch_StoreClosureEmptyTipTag_FallsBackToDrain pins the
// strings.HasPrefix(res.TipTag, "/nix/store/") guard in fresh()'s swap branch
// (issue #2682 review finding): an empty image out-path makes RealizeSync a
// no-op rather than a failure, so without the guard the swap would bind an
// empty TipTag as a live generation with no store path instead of draining.
func TestRunContinuousDispatch_StoreClosureEmptyTipTag_FallsBackToDrain(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111"         // 32 chars, the loaded closure
	const loadedLauncherHash = "44444444444444444444444444444444" // 32 chars, the loaded launcher

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = runner.KindBwrap
	c.boxArtifact = "store-closure"
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

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp, io.Discard, io.Discard)
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

// An unknown BOX_ARTIFACT must stop continuous dispatch before any Box
// launches, rather than fall back to the OCI comparison.
func TestRunContinuousDispatch_UnknownBoxArtifactIsAnError(t *testing.T) {
	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.boxArtifact = "bogus"
	c.issueTracker = "local"
	c.codeForge = "local"

	dir, _ := newStaleProbeRepo(t)
	it := forge.NewFake(testDispatchLabels)
	fr := runner.NewFake()
	f := testFactory(t, dir, fr)

	err := runContinuousDispatch(c, it, it, dir, f, settle.NewFake(), &freshness.Fake{}, freshness.NewRealizerFake(), fakeLiveness{}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("runContinuousDispatch err = %v, want an error naming the unknown artifact", err)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("fr.RunCalls = %v, want 0 (no Box launches on a bad artifact)", fr.RunCalls)
	}
}
