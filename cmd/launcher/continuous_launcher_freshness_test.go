package main

import (
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
