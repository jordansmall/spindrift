package main

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// TestRunContinuousDispatch_CleanSuccessPreservesHostTaintGuard_Halts pins the
// fix for #2128 (root cause diagnosed in #2127): runContinuousDispatch's
// success path used to clear the freshness guard unconditionally, so a clean
// run wiped the rev recorded by an earlier stale run and the next same-rev
// stale verdict became a rebuild (exit 4) instead of a host-taint halt (exit 5).
func TestRunContinuousDispatch_CleanSuccessPreservesHostTaintGuard_Halts(t *testing.T) {
	const freshHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" // 32 chars
	const staleHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" // 32 chars, distinct

	c := baseConfig()
	c.continuousDispatch = true
	c.maxParallel = 1
	c.runtime = "podman"
	c.baseBranch = "main"
	c.label = "ready-for-agent"
	c.issueTracker = "local"
	c.codeForge = "local"
	c.flakeImageAttr = ".#image"
	c.imageTag = "spindrift:" + freshHash // the loaded image

	// newStaleProbeRepo's dir/origin round-trip gives a real git rev that must
	// not move between the three calls below, so rev R stays the same guard key
	// across calls 1 and 3.
	dir, _ := newStaleProbeRepo(t)

	it := forge.NewFake(testDispatchLabels)
	it.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	cf := it // fully-local: the same fake stands in for both seams

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleEval := &freshness.Fake{OutPath: "/nix/store/" + staleHash + "-img"}
	freshEval := &freshness.Fake{OutPath: "/nix/store/" + freshHash + "-img"}
	// All three calls share one realize fake: this test asserts nothing about
	// realize, only about the host-taint guard.
	realizeFake := freshness.NewRealizerFake()

	// Call 1: a stale probe with no prior guard is content staleness, so it
	// arms the guard at rev R.
	err1 := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err1); got != 4 {
		t.Fatalf("call 1: exitCodeFor(err1) = %d, want 4 (waves.ErrImageStale)", got)
	}
	rev := freshness.NewGuard(dir).Prior()
	if rev == "" {
		t.Fatalf("call 1: Guard.Prior() = %q, want a non-empty recorded rev (guard armed)", rev)
	}

	// Call 2: a fresh probe dispatches and settles issue #1, so
	// runContinuousDispatch takes its success path. After #2128 that path no
	// longer clears the tracker, so the guard armed by call 1 survives any
	// clean success, not just the issue-close originally reported.
	err2 := runContinuousDispatch(c, it, cf, dir, f, s, freshEval, realizeFake, lp)
	if err2 != nil {
		t.Fatalf("call 2: runContinuousDispatch = %v, want nil (fresh probe + one dispatchable issue settles cleanly)", err2)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("call 2: fr.RunCalls = %v, want exactly one Run call for issue #1", fr.RunCalls)
	}
	if got := freshness.NewGuard(dir).Prior(); got != rev {
		t.Fatalf("call 2: Guard.Prior() = %q, want %q -- the fix under test: an unrelated clean success must PRESERVE the host-taint guard armed by call 1 (the success path no longer unconditionally clears the Guard's recorded rev)", got, rev)
	}

	// Call 3: stale again at the same rev R is the host-taint signature. The
	// guard survived call 2, so Guard.Classify's NonConverging check matches and
	// halts with exit 5 rather than reporting content staleness again.
	// TestGuard_Classify_NonConverging_HostTaintedAndClears drives the same
	// shape straight at Guard.Classify with no intervening success.
	err3 := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err3); got != 5 {
		t.Fatalf("call 3: exitCodeFor(err3) = %d, want 5 (errImageHostTainted — same-rev repeat after the guard survived call 2's clean success)", got)
	}
	if got := freshness.NewGuard(dir).Prior(); got != "" {
		t.Fatalf("call 3: Guard.Prior() = %q, want empty (Guard.Classify's host-taint-halt path clears the recorded rev after reporting the non-convergence)", got)
	}
}
