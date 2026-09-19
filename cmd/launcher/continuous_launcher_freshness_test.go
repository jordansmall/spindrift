package main

import (
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// TestRunContinuousDispatch_LauncherStaleTriggersImageStale is the regression
// test for issue #1364: it proves runContinuousDispatch forwards
// c.flakeLauncherAttr and c.loadedLauncherHash to freshness.Probe, not just the
// image dimension. The fixture makes the image dimension fresh and the launcher
// dimension stale, so only live launcher wiring can produce exit code 4.
func TestRunContinuousDispatch_LauncherStaleTriggersImageStale(t *testing.T) {
	const loadedImageHash = "11111111111111111111111111111111"    // 32 chars, the width Probe expects of a store hash
	const loadedLauncherHash = "22222222222222222222222222222222" // 32 chars
	const tipLauncherHash = "33333333333333333333333333333333"    // 32 chars, deliberately never equal to loadedLauncherHash

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
	// refill before any discover or dispatch happens.
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleEval := &freshness.Fake{
		OutPathForAttr: map[string]string{
			// The attr trims to "image" and the outpath hash matches
			// c.imageTag, so the image dimension alone is fresh.
			"image": "/nix/store/" + loadedImageHash + "-img",
			// The attr trims to "launcher-currency" and the outpath hash
			// differs from c.loadedLauncherHash, so the launcher dimension is
			// stale.
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

// TestRunContinuousDispatch_LauncherHashMatchAllowsDispatch pins the second
// argument of the same #1364 wiring, c.loadedLauncherHash, which the test above
// cannot: there the tip hash differs from both the real loaded hash and "", so
// blanking the argument stays invisible. Here the tip launcher hash matches it,
// so only correct wiring yields a fresh verdict and dispatches the open issue.
func TestRunContinuousDispatch_LauncherHashMatchAllowsDispatch(t *testing.T) {
	const loadedImageHash = "11111111111111111111111111111111"    // 32 chars, the width Probe expects of a store hash
	const loadedLauncherHash = "44444444444444444444444444444444" // 32 chars

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
			// The attr trims to "image" and the outpath hash matches
			// c.imageTag, so the image dimension is fresh.
			"image": "/nix/store/" + loadedImageHash + "-img",
			// The attr trims to "launcher-currency" and the outpath hash
			// matches c.loadedLauncherHash, so the launcher dimension is fresh
			// too, but only if the loaded-hash argument reaches Probe.
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
// pins the masking worry from a research comment on issue #2780: a genuine
// first discover() error must surface as exit 1, never be flattened into exit 4
// by the errors.Is(err, waves.ErrImageStale) check. main.go's comment above that
// check carries the proof that the two cannot coexist.
func TestRunContinuousDispatch_GenuineFirstDiscoverErrorNeverReachesLaterStaleness(t *testing.T) {
	const loadedImageHash = "11111111111111111111111111111111" // 32 chars

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 2 // Above 1, to rule out a multi-slot bootstrap burst reaching a second refill.
	c.runtime = "podman"
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#image"
	c.imageTag = "spindrift:" + loadedImageHash

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	// Every discover() call fails, so the test never reaches an issue and never
	// calls SetIssue.
	it.ListIssuesErr = boxErr
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	freshEval := &freshness.Fake{
		OutPathForAttr: map[string]string{
			// The attr trims to "image" and the outpath hash matches
			// c.imageTag exactly, so fresh() reports not stale on every call,
			// and it should get exactly one.
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
	// freshEval.Calls counts Eval() calls, not fresh() calls. They match 1:1
	// here only because baseConfig leaves flakeLauncherAttr and
	// loadedLauncherHash empty, so Probe's single fresh() call makes the
	// image-attr Eval and skips the launcher-attr one.
	if len(freshEval.Calls) != 1 {
		t.Fatalf("freshEval.Calls = %d, want exactly 1 Eval call -- the bootstrap's single refill calls fresh() exactly once; a failed genuine discover aborts the run before any later refill has a chance to call fresh() again", len(freshEval.Calls))
	}
	if len(fr.RunCalls) != 0 {
		t.Fatalf("fr.RunCalls = %v, want no Box launches -- a failing genuine first discover must abort before any dispatch", fr.RunCalls)
	}
}
