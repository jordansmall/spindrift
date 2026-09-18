package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// shaPattern matches the full 40-char SHA-1 hex string that fetchBaseTip's
// `git rev-parse FETCH_HEAD` resolves to.
var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// newStaleProbeRepo builds two real local git repos, dir (the checkout the
// probe fetches from) with an "origin" remote pointing at a second temp repo,
// each carrying one base.txt commit. fetchBaseTip needs that real git
// round-trip to resolve a genuine non-empty Rev instead of a canned one.
func newStaleProbeRepo(t *testing.T) (dir, origin string) {
	t.Helper()

	dir = tempLogDir(t)

	origin = t.TempDir()
	mustRunGit(t, origin, "init", "-b", "main")
	mustRunGit(t, origin, "config", "user.email", "origin@example.com")
	mustRunGit(t, origin, "config", "user.name", "Origin")
	if err := os.WriteFile(filepath.Join(origin, "base.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, origin, "add", "base.txt")
	mustRunGit(t, origin, "commit", "-m", "base")

	mustRunGit(t, dir, "init", "-b", "main")
	mustRunGit(t, dir, "config", "user.email", "pwd@example.com")
	mustRunGit(t, dir, "config", "user.name", "Pwd")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRunGit(t, dir, "add", "base.txt")
	mustRunGit(t, dir, "commit", "-m", "base")
	mustRunGit(t, dir, "remote", "add", "origin", origin)

	return dir, origin
}

// Regression test for issue #2679: a stale probe verdict must start a
// background `nix build` of the base-tip image (freshness.RealizeTip) without
// changing runContinuousDispatch's exit code and without waiting for that
// build to finish. The freshness.Fake OutPath hash never matches c.imageTag's,
// so the staleness verdict is genuine rather than canned.
func TestRunContinuousDispatch_StaleRealizesTipInBackground(t *testing.T) {
	const loadedHash = "cccccccccccccccccccccccccccccccc" // 32 chars, the loaded image
	const staleHash = "dddddddddddddddddddddddddddddddd"  // 32 chars, distinct, so it never matches loadedHash

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

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	// No open issue at all: waves.RunContinuous's refill checks fresh() before
	// discover(), so the stale verdict short-circuits the bootstrap refill and
	// this test needs no dispatchable issue.
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleEval := &freshness.Fake{OutPath: "/nix/store/" + staleHash + "-img"}

	realizeFake := freshness.NewRealizerFake()
	// Block holds Realize open until the CallsCopy assertion below has run, so
	// the recorded call is provably the one still in flight. The proof that
	// RealizeTip returns before the build finishes lives in the unit test
	// freshness.TestRealizeTip_ReturnsBeforeRealizeCompletes.
	realizeFake.Block = make(chan struct{})

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- the realize wiring must not change the existing exit behavior", got)
	}

	// Asserting CallsCopy() with no channel-wait first is the point: RealizeTip
	// calls Start in its caller's goroutine so the call is recorded by the time
	// runContinuousDispatch returns, and a process exit cannot race past a
	// goroutine that has not run yet.
	calls := realizeFake.CallsCopy()
	if len(calls) != 1 {
		t.Fatalf("realizeFake.CallsCopy() = %v, want exactly one Realize call recorded by the time runContinuousDispatch returns", calls)
	}
	got := calls[0]
	if got.Pwd != dir {
		t.Errorf("realize call Pwd = %q, want %q", got.Pwd, dir)
	}
	if !shaPattern.MatchString(got.Rev) {
		t.Errorf("realize call Rev = %q, want a 40-char git SHA", got.Rev)
	}
	if got.Attr != "image" {
		t.Errorf("realize call Attr = %q, want %q (c.flakeImageAttr %q with its .# prefix trimmed)", got.Attr, "image", c.flakeImageAttr)
	}

	// Unblock the call and confirm it completes: that proves it was still in
	// flight during the CallsCopy check above.
	close(realizeFake.Block)
	select {
	case <-realizeFake.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("realizeFake.Done: timed out waiting for the blocked Realize call to complete after unblocking")
	}
}

// The other half of the regression test for issue #2679: a background realize
// that fails, not just one that is slow, must still leave
// runContinuousDispatch's exit code alone. RealizeTip logs the error to stderr
// and never propagates it. Setting realizeFake.Err instead of gating on Block
// tests a completed-but-failed call rather than an in-flight one.
func TestRunContinuousDispatch_FailedRealizeDoesNotChangeOutcome(t *testing.T) {
	const loadedHash = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" // 32 chars, the loaded image
	const staleHash = "ffffffffffffffffffffffffffffffff"  // 32 chars, distinct, so it never matches loadedHash

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

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	// No open issue at all: the stale verdict short-circuits the bootstrap
	// refill before any discover or dispatch happens.
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleEval := &freshness.Fake{OutPath: "/nix/store/" + staleHash + "-img"}

	realizeFake := freshness.NewRealizerFake()
	realizeFake.Err = errors.New("boom: nix build failed")

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- a failed background realize must not change the existing exit behavior", got)
	}

	// Wait for the background call to finish so RealizeTip's error branch runs
	// before the test ends. Without this the test races past the failed half of
	// #2679 instead of exercising it.
	select {
	case <-realizeFake.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("realizeFake.Done: timed out waiting for the failed background Realize call to complete")
	}

	calls := realizeFake.CallsCopy()
	if len(calls) != 1 {
		t.Fatalf("realizeFake.CallsCopy() = %v, want exactly one Realize call recorded", calls)
	}
}

// This test pinned exit code 4 (waves.ErrImageStale) for a stale bwrap
// agent-closure under issue #2667 AC2. ADR 0043 (issue #2682) superseded that:
// an image-only-stale verdict under bwrap now hot-swaps in place instead of
// draining, so with no open issue the run falls through to the ordinary
// empty-queue exit (2, errQueueEmpty) once the swap succeeds.
func TestRunContinuousDispatch_Bwrap_StaleClosure_HotSwapsThenReachesEmptyQueue(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded closure
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct, so it never matches loadedHash

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runnerKind = "bwrap"
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#packages.x86_64-linux.agent-closure"
	c.imageTag = "/nix/store/" + loadedHash + "-agent-closure"
	// The swap branch requires the launcher dimension to have really been
	// probed (ADR 0043, issue #2682 review finding), not defaulted true by an
	// unconfigured attr. freshness.Fake returns the same outpath for every
	// attr, so loadedLauncherHash must be staleHash to leave that dimension
	// both evaluated and fresh.
	c.flakeLauncherAttr = ".#launcher-currency"
	c.loadedLauncherHash = staleHash

	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	// No open issue at all: the swap succeeds against an empty queue, which is
	// what makes the run reach the ordinary empty-queue exit.
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleEval := &freshness.Fake{OutPath: "/nix/store/" + staleHash + "-agent-closure"}
	realizeFake := freshness.NewRealizerFake()

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err); got != 2 {
		t.Fatalf("exitCodeFor(err) = %d, want 2 (errQueueEmpty) -- ADR 0043: an image-only-stale bwrap verdict hot-swaps instead of draining, so with no open issue at all the run falls through to the ordinary empty-queue exit", got)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0 (no open issue to dispatch)", len(fr.RunCalls))
	}
	if calls := realizeFake.CallsCopy(); len(calls) != 1 {
		t.Errorf("realizeFake.CallsCopy() = %v, want exactly one synchronous RealizeSync call (the hot-swap's own realize)", calls)
	}
}
