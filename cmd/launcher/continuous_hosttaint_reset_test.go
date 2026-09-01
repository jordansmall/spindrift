package main

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// TestRunContinuousDispatch_CleanSuccessPreservesHostTaintGuard_Halts is the
// regression test for issue #2128 (root cause #2127).
//
// runContinuousDispatch's success path used to contain, at
// cmd/launcher/main.go,
//
//	_ = tracker.clear()
//
// which ran on EVERY nil-error return from waves.RunContinuous,
// unconditionally. It was NOT scoped to "the divergence this guard
// recorded actually resolved," so the host-taint non-convergence guard
// (issue #2113's logic, now sealed in freshness.Guard) armed by a prior
// stale run was wiped by the very next successful run, whatever that run
// did. That wipe forced the following same-rev stale verdict to be
// reclassified as fresh content staleness (exit 4 → rebuild) instead of
// non-converging host-taint (exit 5 → halt + diagnostic) — the observed
// perpetual rebuild loop.
//
// #2128's fix removes that unconditional success-path clear entirely (the
// other two Guard clear points — the ErrOpenNoneDispatchable reset and the
// Guard's own host-taint-halt clear inside freshness.Guard.Classify — are
// untouched and still fire on their own paths). With the clear gone, a clean
// continuous success no longer disturbs a guard armed by an earlier stale
// run, so the next same-rev stale repeat is correctly classified as
// non-converging host-taint.
//
// The three calls below inject a freshness.Fake evaluator, which
// short-circuits Probe's real fetchBaseTip/tag-comparison path entirely, and
// share one pwd and one persisted tracker file:
//
//  1. A stale probe (staleEval's tag never matches the loaded image) with no
//     prior recorded rev is content staleness by definition (issue #2113):
//     freshness.Guard.Classify returns Rebuild — mapped to waves.ErrImageStale
//     (exit 4) — and records the fetched base rev R as the guard. In a real
//     dogfood loop, exit 4 is
//     exactly the signal that drives a rebuild attempt against R.
//  2. A FRESH probe on the same pwd lets refill dispatch and settle the one
//     open issue; RunContinuous returns nil, so runContinuousDispatch takes
//     its success path. With #2128's fix, that success path no longer clears
//     the tracker, so the guard armed in step 1 SURVIVES this unrelated clean
//     success — no rebuild ever having happened.
//  3. A stale probe again, at the SAME rev R (the origin repo never moved) —
//     the signature a genuinely host-tainted image produces after a rebuild
//     attempt fails to converge. Because step 2 preserved the guard,
//     Guard.Prior() is still R here, so freshness.Guard.Classify's
//     NonConverging(R, R) check is true and it correctly reports this
//     same-rev repeat as HostTainted — mapped to errImageHostTainted (exit 5),
//     matching TestGuard_Classify_NonConverging_HostTaintedAndClears, which
//     drives the identical same-rev-repeat shape directly against Guard.Classify
//     (no intervening success) and gets the same HostTainted disposition.
//     Guard.Classify's own host-taint-halt path then clears the recorded rev,
//     so Guard.Prior() is empty again afterward.
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

	// newStaleProbeRepo's dir/origin round-trip gives a real, stable git rev
	// that must not move between the three calls below, so R stays the same
	// guard key across steps 1 and 3.
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
	// realizeFake is shared across all three calls below: this test's own
	// purpose (host-taint guard behavior) is unrelated to realize, so a
	// single fake standing in for `nix build` across every call is enough --
	// no per-call assertions on it are needed here.
	realizeFake := freshness.NewRealizerFake()

	// --- call 1: stale, no prior guard -- content staleness, arm the guard at R ---
	err1 := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err1); got != 4 {
		t.Fatalf("call 1: exitCodeFor(err1) = %d, want 4 (waves.ErrImageStale)", got)
	}
	rev := freshness.NewGuard(dir).Prior()
	if rev == "" {
		t.Fatalf("call 1: Guard.Prior() = %q, want a non-empty recorded rev (guard armed)", rev)
	}

	// --- call 2: fresh -- refill dispatches and settles issue #1; RunContinuous
	// returns nil, so runContinuousDispatch takes its success path. With
	// #2128's fix, that success path no longer clears the tracker
	// unconditionally, so the guard armed by call 1 survives this unrelated
	// clean success -- ANY clean success now leaves it intact, the
	// originally reported issue-close being just one such success. ---
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

	// --- call 3: stale again, at the SAME rev R -- a real host-taint
	// signature. The guard survived (call 2 preserved it), so this correctly
	// halts (exit 5) instead of misclassifying as content staleness. ---
	err3 := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, realizeFake, lp)
	if got := exitCodeFor(err3); got != 5 {
		t.Fatalf("call 3: exitCodeFor(err3) = %d, want 5 (errImageHostTainted — same-rev repeat after the guard survived call 2's clean success)", got)
	}
	if got := freshness.NewGuard(dir).Prior(); got != "" {
		t.Fatalf("call 3: Guard.Prior() = %q, want empty (Guard.Classify's host-taint-halt path clears the recorded rev after reporting the non-convergence)", got)
	}
}
