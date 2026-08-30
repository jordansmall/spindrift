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

// shaPattern matches a full 40-char git SHA-1 hex string — what
// fetchBaseTip's `git rev-parse FETCH_HEAD` resolves to.
var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// newStaleProbeRepo sets up the pair of real local git repos that every
// runContinuousDispatch stale-probe test needs: dir (the checkout
// runContinuousDispatch's probe fetches from, i.e. pwd) with an "origin"
// remote pointing at a second temp repo (origin), each carrying one
// base.txt commit -- so freshness.Probe's real fetchBaseTip resolves a
// genuine non-empty Rev instead of a canned one.
func newStaleProbeRepo(t *testing.T) (dir, origin string) {
	t.Helper()

	dir = tempLogDir(t) // pwd: the checkout runContinuousDispatch's probe fetches from

	// origin: a second repo dir's worth of history for pwd's "origin" remote
	// to fetch -- fetchBaseTip needs a real git round-trip to produce a
	// genuine Rev.
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

// TestRunContinuousDispatch_StaleRealizesTipInBackground is the regression
// test for issue #2679: a stale probe verdict must kick off a background
// `nix build` of the base-tip image (freshness.RealizeTip) without changing
// runContinuousDispatch's existing exit-code behavior, and without waiting
// for that build to finish before returning.
//
// The setup mirrors
// TestRunContinuousDispatch_CleanSuccessPreservesHostTaintGuard_Halts: a
// real local git repo (dir) with a real "origin" remote pointing at a
// second temp repo, so freshness.Probe's real fetchBaseTip resolves a
// genuine non-empty Rev, combined with a freshness.Fake evaluator whose
// OutPath hash never matches c.imageTag's — a genuine content-staleness
// verdict, not a canned one.
func TestRunContinuousDispatch_StaleRealizesTipInBackground(t *testing.T) {
	const loadedHash = "cccccccccccccccccccccccccccccccc" // 32 chars, the loaded image
	const staleHash = "dddddddddddddddddddddddddddddddd"  // 32 chars, distinct -- never matches loadedHash

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
	// refill before any discover/dispatch happens (see
	// waves.RunContinuous's refill: fresh() is checked before discover()),
	// so this test needs no dispatchable issue to observe the stale exit
	// and the realize call it triggers.
	cf := it

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleEval := &freshness.Fake{OutPath: "/nix/store/" + staleHash + "-img"}

	realizeFake := freshness.NewRealizerFake()
	// Block gates Realize's return until the test explicitly unblocks it
	// below (after the CallsCopy assertion), which lets this test confirm
	// the recorded call is the very one its own goroutine later
	// completes/unblocks, and then explicitly synchronize on Done. The
	// actual proof that Start returns without waiting for the build --
	// freshness.RealizeTip's fire-and-forget guarantee -- lives in the unit
	// test freshness.TestRealizeTip_ReturnsBeforeRealizeCompletes
	// (cmd/launcher/internal/freshness/realize_test.go), which explicitly
	// checks that <-rf.Done has NOT fired before it unblocks the call.
	realizeFake.Block = make(chan struct{})

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- the realize wiring must not change the existing exit behavior", got)
	}

	// RealizeTip calls Start synchronously, in the same goroutine as its
	// caller, precisely so the call is durably recorded by the time
	// RealizeTip (and hence runContinuousDispatch, and hence the calling
	// process, up to and including os.Exit) returns -- see the Realizer doc
	// comment for why. Asserting CallsCopy() synchronously here, with no
	// channel-wait beforehand, is the actual proof of that guarantee: it
	// demonstrates the call can't be lost to a process exit racing a
	// goroutine that hasn't run yet.
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

	// Now unblock the still-in-flight call and confirm it completes --
	// proving it really was still running, not already finished before the
	// CallsCopy check above.
	close(realizeFake.Block)
	select {
	case <-realizeFake.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("realizeFake.Done: timed out waiting for the blocked Realize call to complete after unblocking")
	}
}

// TestRunContinuousDispatch_FailedRealizeDoesNotChangeOutcome is the other
// half of the regression test for issue #2679: a background realize that
// FAILS -- not just one that's slow -- must still never change
// runContinuousDispatch's own exit code or behavior. RealizeTip only logs
// the error to stderr (see freshness.RealizeTip); it must never propagate
// it to the caller. The setup mirrors
// TestRunContinuousDispatch_StaleRealizesTipInBackground, but sets
// realizeFake.Err instead of gating on Block, since this test cares about
// the outcome of a completed-but-failed call, not about proving the call is
// still in flight when runContinuousDispatch returns.
func TestRunContinuousDispatch_FailedRealizeDoesNotChangeOutcome(t *testing.T) {
	const loadedHash = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" // 32 chars, the loaded image
	const staleHash = "ffffffffffffffffffffffffffffffff"  // 32 chars, distinct -- never matches loadedHash

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
	// refill before any discover/dispatch happens, so this test needs no
	// dispatchable issue to observe the stale exit and the realize call it
	// triggers.
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

	// Wait for the background Realize call to actually complete (and
	// therefore for RealizeTip's error branch to have run) before the test
	// ends, so this test genuinely exercises the FAILED half of #2679's
	// acceptance criterion rather than racing past it.
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

// TestRunContinuousDispatch_Bwrap_StaleClosure_ReturnsExitCode4 is issue
// #2667 AC2's own test: a stale bwrap agent-closure must produce the same
// exit code (4, waves.ErrImageStale) a stale OCI image does. Every other
// exit-code-4 test in this file drives the shared path with an OCI-style
// runnerKind/imageTag; this one is the bwrap twin, proving the shared
// mapping is actually exercised for bwrap rather than only argued from code
// sharing between the two runnerKinds. Mirrors
// TestRunContinuousDispatch_StaleRealizesTipInBackground's setup, but with a
// bwrap runnerKind and a bare-store-path imageTag (no "repo:hash" tag shape).
func TestRunContinuousDispatch_Bwrap_StaleClosure_ReturnsExitCode4(t *testing.T) {
	const loadedHash = "11111111111111111111111111111111" // 32 chars, the loaded closure
	const staleHash = "22222222222222222222222222222222"  // 32 chars, distinct -- never matches loadedHash

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

	staleEval := &freshness.Fake{OutPath: "/nix/store/" + staleHash + "-agent-closure"}
	realizeFake := freshness.NewRealizerFake()

	err := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err); got != 4 {
		t.Fatalf("exitCodeFor(err) = %d, want 4 (waves.ErrImageStale) -- a stale bwrap agent-closure must exit the same way a stale OCI image does", got)
	}
	if len(fr.RunCalls) != 0 {
		t.Errorf("RunCalls: got %d, want 0 (no Box launches once the probe is stale)", len(fr.RunCalls))
	}
}
