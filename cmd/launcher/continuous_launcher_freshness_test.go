package main

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// TestRunContinuousDispatch_LauncherStaleTriggersImageStale is the
// regression test for issue #1364: it proves runContinuousDispatch's
// freshness.Probe call actually forwards c.flakeLauncherAttr and
// c.loadedLauncherHash (main.go's `fresh` closure), not just the image
// dimension. The fixture makes the IMAGE dimension fresh (the tip image
// outpath's hash matches c.imageTag) and the LAUNCHER dimension stale (the
// tip launcher outpath's hash differs from c.loadedLauncherHash), so the
// only way this probe can come back stale is if the launcher attr/hash
// wiring at main.go:1592 is actually live -- reverting those two trailing
// args to "", "" would make the launcher dimension look unconfigured
// (launcherConfigured = false in probe.go), leaving only the fresh image
// dimension driving the verdict, and this test would then fail to observe
// exit code 4.
func TestRunContinuousDispatch_LauncherStaleTriggersImageStale(t *testing.T) {
	const loadedImageHash = "11111111111111111111111111111111"    // 32 chars, the loaded image
	const loadedLauncherHash = "22222222222222222222222222222222" // 32 chars, the loaded launcher
	const tipLauncherHash = "33333333333333333333333333333333"    // 32 chars, distinct -- never matches loadedLauncherHash

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runtime = "podman"
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#image"
	c.imageTag = "spindrift:" + loadedImageHash
	c.flakeLauncherAttr = ".#launcher-currency"
	c.loadedLauncherHash = loadedLauncherHash

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	// No open issue at all: the stale verdict short-circuits the bootstrap
	// refill before any discover/dispatch happens, so this test needs no
	// dispatchable issue to observe the stale exit.
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleEval := &freshness.Fake{
		OutPathForAttr: map[string]string{
			// image attr trims to "image" -- outpath hash matches
			// c.imageTag's hash, so the image dimension alone is fresh.
			"image": "/nix/store/" + loadedImageHash + "-img",
			// launcher attr trims to "launcher-currency" -- outpath hash
			// differs from c.loadedLauncherHash, so the launcher dimension
			// is stale.
			"launcher-currency": "/nix/store/" + tipLauncherHash + "-launcher",
		},
	}

	realizeFake := freshness.NewRealizerFake()

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- the launcher attr/hash wiring must reach freshness.Probe so a launcher-only staleness verdict still stops the wave", got)
	}

	if len(fr.RunCalls) != 0 {
		t.Fatalf("fr.RunCalls = %v, want no Box launches -- a stale probe verdict must short-circuit the wave before any dispatch", fr.RunCalls)
	}
}

// TestRunContinuousDispatch_LauncherHashMatchAllowsDispatch pins the SECOND
// trailing arg at main.go:1592 specifically -- c.loadedLauncherHash -- a gap
// the test above does not close. That test makes the tip launcher hash
// differ from c.loadedLauncherHash, so mutating c.loadedLauncherHash to ""
// at the call site is invisible there: tipLauncherHash ("333...") still
// differs from "" exactly as it differed from the real loaded hash, and the
// launcher dimension is stale either way -- the suite would stay green even
// with that mutation live.
//
// Here the fixture instead makes the tip launcher hash MATCH
// c.loadedLauncherHash (launcher dimension fresh) and the tip image hash
// match c.imageTag (image dimension fresh too), so the correct wiring
// produces an overall-fresh verdict: RunContinuous proceeds to dispatch the
// one open issue and returns nil. If main.go:1592's second arg were reverted
// to "", Probe would compare tipLauncherHash ("444...") against "" instead
// of the real loaded hash, they would never match, and the launcher
// dimension would look stale -- flipping the verdict to rebuild-needed
// (exit 4, waves.ErrImageStale) and suppressing the dispatch entirely. That
// is exactly what this test asserts against.
func TestRunContinuousDispatch_LauncherHashMatchAllowsDispatch(t *testing.T) {
	const loadedImageHash = "11111111111111111111111111111111"    // 32 chars, the loaded image
	const loadedLauncherHash = "44444444444444444444444444444444" // 32 chars, the loaded launcher

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runtime = "podman"
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#image"
	c.imageTag = "spindrift:" + loadedImageHash
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

	freshEval := &freshness.Fake{
		OutPathForAttr: map[string]string{
			// image attr trims to "image" -- outpath hash matches
			// c.imageTag's hash, so the image dimension is fresh.
			"image": "/nix/store/" + loadedImageHash + "-img",
			// launcher attr trims to "launcher-currency" -- outpath hash
			// matches c.loadedLauncherHash, so the launcher dimension is
			// fresh too, ONLY if the loaded-hash arg actually reaches
			// Probe.
			"launcher-currency": "/nix/store/" + loadedLauncherHash + "-launcher",
		},
	}

	realizeFake := freshness.NewRealizerFake()

	err := runContinuousDispatch(c, it, cf, dir, f, s, freshEval, realizeFake, lp)
	if err != nil {
		t.Fatalf("runContinuousDispatch = %v, want nil -- image and launcher both fresh should let the wave dispatch and settle the one open issue", err)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("fr.RunCalls = %v, want exactly one Run call for issue #1 -- an overall-fresh verdict must not suppress dispatch", fr.RunCalls)
	}
}

// TestRunContinuousDispatch_GenuineFirstDiscoverErrorNeverReachesLaterStaleness
// is the regression test for the "scenario 2" masking worry raised in a
// research comment on issue #2780: could a genuine (non-reporting-only)
// first-ever discover() error ever coexist with staleness that's detected
// independently later in the same run, so that runContinuousDispatch's
// `errors.Is(err, waves.ErrImageStale)` check masks that real error behind
// exit 4 instead of surfacing it as exit 1? This test proves that scenario
// is structurally unreachable, not just untested -- see the priority
// comment above that check in main.go for the full unreachability proof;
// this doc only summarizes the setup, to avoid keeping two prose copies of
// that proof in sync.
//
// The fixture makes fresh() report NOT stale (the fake outpath hash matches
// c.imageTag's hash) and makes every discover() call fail. refill calls
// fresh() first; since it's not stale, refill falls through to the genuine
// discover() call, which fails here, so refill logs to stderr and returns
// false without ever reaching the dispatch/launch code -- zero Boxes
// launch, and RunContinuous's bootstrap never releases its mutex to a
// second refill (see the main.go comment for why). So a genuine
// first-discover error always ends the run (via ErrOpenNoneDispatchable,
// since stale and dispatchedAny both stay false) before fresh() is ever
// evaluated a second time, and the raw discover error must surface as exit
// 1, never flattened into ErrImageStale/exit 4 or
// ErrOpenNoneDispatchable/exit 3.
func TestRunContinuousDispatch_GenuineFirstDiscoverErrorNeverReachesLaterStaleness(t *testing.T) {
	const loadedImageHash = "11111111111111111111111111111111" // 32 chars

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 2 // >1, to also rule out a multi-slot bootstrap burst reaching a second refill
	c.runtime = "podman"
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#image"
	c.imageTag = "spindrift:" + loadedImageHash

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	// Every discover() call fails -- no issue is ever reached, so SetIssue is
	// never called.
	it.ListIssuesErr = boxErr
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	freshEval := &freshness.Fake{
		OutPathForAttr: map[string]string{
			// image attr trims to "image" -- outpath hash matches
			// c.imageTag's hash exactly, so fresh() reports NOT stale on
			// every call it's given (there should only ever be one call).
			"image": "/nix/store/" + loadedImageHash + "-img",
		},
	}

	realizeFake := freshness.NewRealizerFake()

	err := runContinuousDispatch(c, it, cf, dir, f, s, freshEval, realizeFake, lp)
	if !errors.Is(err, boxErr) {
		t.Fatalf("runContinuousDispatch = %v, want the raw ListIssuesErr surfaced (errors.Is boxErr), never flattened into ErrImageStale or ErrOpenNoneDispatchable", err)
	}
	if got := exitCodeFor(err); got != 1 {
		t.Fatalf("exitCodeFor(err) = %d, want 1 -- a genuine first-discover error must surface as a raw error, not exit 3 or exit 4", got)
	}
	// freshEval.Calls counts Eval() calls, not fresh() calls; the two match
	// 1:1 here only because baseConfig leaves flakeLauncherAttr and
	// loadedLauncherHash empty, so Probe's single fresh() call makes only
	// the one image-attr Eval and skips its second, launcher-attr Eval.
	if len(freshEval.Calls) != 1 {
		t.Fatalf("freshEval.Calls = %d, want exactly 1 Eval call -- the bootstrap's single refill calls fresh() exactly once; a failed genuine discover aborts the run before any later refill has a chance to call fresh() again", len(freshEval.Calls))
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("fr.RunCalls = %v, want no Box launches -- a failing genuine first discover must abort before any dispatch", fr.RunCalls)
	}
}
