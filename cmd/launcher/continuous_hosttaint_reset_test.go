package main

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/settle"
)

// TestRunContinuousDispatch_CleanSuccessPreservesHostTaintGuard_Halts is the
// regression test for issue #2128, the fix for the root cause #2127
// adjudicated as Theory A.
//
// # VERDICT (unchanged from #2127): Theory A is the root cause; Theory B is ruled out
//
// Theory A — guard reset on success — was CONFIRMED by #2127, and it is the
// exact code path this test pins. runContinuousDispatch's success path used
// to contain, at cmd/launcher/main.go:1291,
//
//	_ = tracker.clear()
//
// which ran on EVERY nil-error return from waves.RunContinuous,
// unconditionally. It was NOT scoped to "the divergence this tracker
// recorded actually resolved," so the host-taint non-convergence guard
// (issue #2113's classifyStaleOutcome/staleRevTracker) armed by a prior
// stale run was wiped by the very next successful run, whatever that run
// did. That wipe forced the following same-rev stale verdict to be
// reclassified as fresh content staleness (exit 4 → rebuild) instead of
// non-converging host-taint (exit 5 → halt + diagnostic) — the observed
// perpetual rebuild loop.
//
// #2128's fix removes that unconditional success-path clear entirely (the
// other two tracker.clear() call sites — the ErrOpenNoneDispatchable path at
// main.go:1281 and classifyStaleOutcome's own host-taint-halt clear at
// continuous_freshness.go:43 — are untouched and still fire on their own
// paths). With the clear gone, a clean continuous success no longer disturbs
// a guard armed by an earlier stale run, so the next same-rev stale repeat is
// correctly classified as non-converging host-taint.
//
// Precision on the trigger (do not overclaim "close"): the guard used to be
// cleared at main.go:1291 BEFORE reconcileAfterDispatch (main.go:1293) ran,
// on ANY successful RunContinuous return — not specifically on an issue
// close. The issue-close of #2127's originally reported scenario was merely
// one instance of such a success. The defect was therefore broader and more
// precise than "closing an issue resets it": any clean continuous run reset
// it, and the fix restores the guard's survival across any clean run too.
//
// Theory B — probe oblivious to local forge — RULED OUT as a cause, and it
// cannot even partially contribute ("both contribute" is excluded). The
// freshness probe's signature is
//
//	freshness.Probe(runtime, pwd, baseBranch, flakeImageAttr, imageTag string, eval Evaluator)
//
// at cmd/launcher/internal/freshness/probe.go:91 — it takes NO codeForge
// argument, so it behaves identically under CODE_FORGE=local and
// CODE_FORGE=github; there is no local-forge-specific branch that could emit a
// spurious "rebuild needed." Worse for Theory B, its only local-specific
// effect runs the WRONG direction: in a fully-local checkout with no reachable
// origin, fetchBaseTip fails isNoOriginRemote and Probe returns
// Applicable=false (probe.go:114-119), which SUPPRESSES the rebuild rather
// than causing it. That suppression is pinned deterministically by the
// existing freshness.TestProbe_NoOriginRemoteNotApplicable. A rebuild verdict
// only ever arises when origin IS reachable and the evaluator's tag diverges —
// i.e. content-staleness / host-taint, which is Theory A's domain, forge-
// agnostic. So Theory B cannot produce the loop.
//
// # What this test proves deterministically vs. what needs macOS hardware
//
// Deterministic (this test, in-process, no macOS host, no real image build):
// the fixed guard-preservation mechanism — that a clean success no longer
// wipes the staleRevTracker, so a subsequent same-rev host-taint signature
// correctly halts with exit 5 instead of being downgraded to a rebuild
// verdict (exit 4).
//
// LIMITATION — why Theory B is adjudicated statically, not by this test: the
// three calls below inject a freshness.Fake evaluator, which short-circuits
// Probe's real fetchBaseTip/tag-comparison path entirely. This test therefore
// does NOT and CANNOT exercise probe.go's forge-oblivious code path, so it
// cannot by itself observe or refute Theory B. Theory B is ruled out above by
// static signature analysis (no codeForge param) plus the separately-pinning
// TestProbe_NoOriginRemoteNotApplicable — not by anything asserted here.
//
// Still needs a real macOS run to confirm: that the production dogfood image
// graph is genuinely host-tainted in exactly this same-rev-perpetual-staleness
// way — i.e. that after a real rebuild the evaluator's tag still diverges at
// the unchanged rev R. This test pins the guard's in-process preservation,
// not that production divergence.
//
// # How the three calls demonstrate the fix
//
// Three direct runContinuousDispatch calls share one pwd and one persisted
// tracker file:
//
//  1. A stale probe (staleEval's tag never matches the loaded image) with no
//     prior recorded rev is content staleness by definition (issue #2113):
//     classifyStaleOutcome returns waves.ErrImageStale (exit 4) and records
//     the fetched base rev R as the guard. In a real dogfood loop, exit 4 is
//     exactly the signal that drives a rebuild attempt against R.
//  2. A FRESH probe on the same pwd lets refill dispatch and settle the one
//     open issue; RunContinuous returns nil, so runContinuousDispatch takes
//     its success path. With #2128's fix, that success path no longer clears
//     the tracker, so the guard armed in step 1 SURVIVES this unrelated clean
//     success — no rebuild ever having happened.
//  3. A stale probe again, at the SAME rev R (the origin repo never moved) —
//     the signature a genuinely host-tainted image produces after a rebuild
//     attempt fails to converge. Because step 2 preserved the guard,
//     tracker.prior() is still R here, so classifyStaleOutcome's
//     NonConverging(R, R) check is true and it correctly reports this
//     same-rev repeat as non-converging host-taint (exit 5), matching
//     TestClassifyStaleOutcome_NonConverging_DiagsAndHalts, which drives the
//     identical same-rev-repeat shape directly against classifyStaleOutcome
//     (no intervening success) and gets the same errImageHostTainted result.
//     classifyStaleOutcome's own host-taint-halt path (continuous_freshness.go:43)
//     then clears the tracker, so tracker.prior() is empty again afterward.
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

	dir := tempLogDir(t) // pwd: the checkout runContinuousDispatch's probe fetches from

	// origin: a second repo dir's worth of history for pwd's "origin" remote
	// to fetch — fetchBaseTip needs a real, stable git round-trip, and the
	// rev it resolves must not move between the three calls below, so R stays
	// the same guard key across steps 1 and 3.
	origin := t.TempDir()
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

	it := forge.NewFake(testDispatchLabels)
	it.SetIssue(forge.Issue{Number: "1", Labels: []string{c.label}})
	cf := it // fully-local: the same fake stands in for both seams

	fr := runner.NewFake()
	f := testFactory(t, dir, fr)
	s := settle.NewFake()
	lp := fakeLiveness{}

	staleEval := &freshness.Fake{OutPath: "/nix/store/" + staleHash + "-img"}
	freshEval := &freshness.Fake{OutPath: "/nix/store/" + freshHash + "-img"}

	// --- call 1: stale, no prior guard -- content staleness, arm the guard at R ---
	err1 := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, lp)
	if got := exitCodeFor(err1); got != 4 {
		t.Fatalf("call 1: exitCodeFor(err1) = %d, want 4 (waves.ErrImageStale)", got)
	}
	rev := newStaleRevTracker(dir).prior()
	if rev == "" {
		t.Fatalf("call 1: tracker.prior() = %q, want a non-empty recorded rev (guard armed)", rev)
	}

	// --- call 2: fresh -- refill dispatches and settles issue #1; RunContinuous
	// returns nil, so runContinuousDispatch takes its success path. With
	// #2128's fix, that success path no longer clears the tracker
	// unconditionally, so the guard armed by call 1 survives this unrelated
	// clean success -- ANY clean success now leaves it intact, the
	// originally reported issue-close being just one such success. ---
	err2 := runContinuousDispatch(c, it, cf, dir, f, s, freshEval, lp)
	if err2 != nil {
		t.Fatalf("call 2: runContinuousDispatch = %v, want nil (fresh probe + one dispatchable issue settles cleanly)", err2)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("call 2: fr.RunCalls = %v, want exactly one Run call for issue #1", fr.RunCalls)
	}
	if got := newStaleRevTracker(dir).prior(); got != rev {
		t.Fatalf("call 2: tracker.prior() = %q, want %q -- the fix under test: an unrelated clean success must PRESERVE the host-taint guard armed by call 1 (main.go's success path no longer unconditionally clears the tracker)", got, rev)
	}

	// --- call 3: stale again, at the SAME rev R -- a real host-taint
	// signature. The guard survived (call 2 preserved it), so this correctly
	// halts (exit 5) instead of misclassifying as content staleness. ---
	err3 := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, lp)
	// THIS IS THE REGRESSION #2128 GUARDS AGAINST: a same-rev repeat after a
	// rebuild attempt (call 1's exit 4 is exactly the dogfood loop's rebuild
	// trigger) must be reported as errImageHostTainted (exit 5) — see
	// TestClassifyStaleOutcome_NonConverging_DiagsAndHalts, which drives the
	// identical same-rev-repeat shape straight through classifyStaleOutcome
	// with no intervening issue-close and gets the same exit 5. Here the
	// guard preserved by call 2 makes tracker.prior() == rev, so
	// classifyStaleOutcome's NonConverging check finds a matching prior rev
	// and correctly halts instead of falling back to the rebuild-and-retry
	// verdict.
	if got := exitCodeFor(err3); got != 5 {
		t.Fatalf("call 3: exitCodeFor(err3) = %d, want 5 (errImageHostTainted — same-rev repeat after the guard survived call 2's clean success)", got)
	}
	if got := newStaleRevTracker(dir).prior(); got != "" {
		t.Fatalf("call 3: tracker.prior() = %q, want empty (classifyStaleOutcome's host-taint-halt path clears the tracker after reporting the non-convergence)", got)
	}
}
