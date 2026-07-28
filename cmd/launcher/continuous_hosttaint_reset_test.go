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

// TestRunContinuousDispatch_IssueCloseResetsHostTaintGuard_ForcesRebuild is a
// CHARACTERIZATION test AND the written verdict for issue #2127 — adjudicate
// why a fully-local (ISSUE_TRACKER=local, CODE_FORGE=local) issue-close forces
// a macOS image rebuild on the next continuous-dispatch run.
//
// # VERDICT: Theory A is the root cause; Theory B is ruled out
//
// Theory A — guard reset on success — CONFIRMED, and it is the exact code
// path this test pins. runContinuousDispatch's success path at
// cmd/launcher/main.go:1291,
//
//	_ = tracker.clear()
//
// runs on EVERY nil-error return from waves.RunContinuous, unconditionally.
// It is NOT scoped to "the divergence this tracker recorded actually
// resolved," so the host-taint non-convergence guard (issue #2113's
// classifyStaleOutcome/staleRevTracker) armed by a prior stale run is wiped
// by the very next successful run, whatever that run did. That wipe forces
// the following same-rev stale verdict to be reclassified as fresh content
// staleness (exit 4 → rebuild) instead of non-converging host-taint
// (exit 5 → halt + diagnostic) — the observed perpetual rebuild loop.
//
// Precision on the trigger (do not overclaim "close"): the guard is cleared
// at main.go:1291 BEFORE reconcileAfterDispatch (main.go:1293) runs, on ANY
// successful RunContinuous return. The issue-close of the reported scenario is
// merely one instance of such a success — this run genuinely prints
// "reconcile: no issues closed," yet the guard is already gone, because the
// clearing fires on the dispatch success, not on a close per se. The defect is
// therefore broader and more precise than "closing an issue resets it": any
// clean continuous run resets it.
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
// the guard-reset mechanism itself — that a clean success at main.go:1291
// wipes the staleRevTracker and thereby downgrades a subsequent same-rev
// host-taint signature (exit 5) to a rebuild verdict (exit 4).
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
// the unchanged rev R. This test pins the guard's in-process reset, not that
// production divergence.
//
// # How the three calls demonstrate Theory A
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
//     its success path and unconditionally clears the tracker (main.go:1291)
//     before reconcileAfterDispatch even runs. The guard armed in step 1 is
//     gone — with no rebuild ever having happened.
//  3. A stale probe again, at the SAME rev R (the origin repo never moved) —
//     the signature a genuinely host-tainted image produces after a rebuild
//     attempt fails to converge. Because step 2 wiped the guard,
//     tracker.prior() is "" here, so classifyStaleOutcome's NonConverging(R,
//     "") check is false and it misclassifies this same-rev repeat as content
//     staleness (again exit 4) instead of the host-taint halt (exit 5).
//     Contrast TestClassifyStaleOutcome_NonConverging_DiagsAndHalts, which
//     drives the identical same-rev-repeat shape directly against
//     classifyStaleOutcome (no intervening success) and correctly gets
//     errImageHostTainted.
//
// Call 3 asserting exit 4 (rather than 5) is the CURRENT, WRONG behavior this
// test pins. Per acceptance criterion 4 no fix is applied here, so the
// assertion is written green against today's defective behavior rather than
// red-failing (a red test would break CI); #2127's fix must scope
// tracker.clear() away from an unrelated success path and then flip this
// assertion to 5.
func TestRunContinuousDispatch_IssueCloseResetsHostTaintGuard_ForcesRebuild(t *testing.T) {
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
	// returns nil, so the success path's unconditional tracker.clear()
	// (main.go:1291) wipes the guard armed above BEFORE reconcile even runs, for
	// a reason wholly unrelated to freshness -- ANY clean success clears it, the
	// reported issue-close being just one such success. ---
	err2 := runContinuousDispatch(c, it, cf, dir, f, s, freshEval, lp)
	if err2 != nil {
		t.Fatalf("call 2: runContinuousDispatch = %v, want nil (fresh probe + one dispatchable issue settles cleanly)", err2)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("call 2: fr.RunCalls = %v, want exactly one Run call for issue #1", fr.RunCalls)
	}
	if got := newStaleRevTracker(dir).prior(); got != "" {
		t.Fatalf("call 2: tracker.prior() = %q, want empty -- THE BUG under test: an unrelated clean success reset the host-taint guard armed by call 1 (main.go:1291's tracker.clear() is unconditional on the success path, not scoped to a resolved divergence)", got)
	}

	// --- call 3: stale again, at the SAME rev R -- a real host-taint
	// signature. The guard is gone (call 2 wiped it), so this misclassifies
	// as content staleness (exit 4) instead of halting (exit 5). ---
	err3 := runContinuousDispatch(c, it, cf, dir, f, s, staleEval, lp)
	// THIS IS THE DEFECT #2127 EXISTS TO FIX: a same-rev repeat after a
	// rebuild attempt (call 1's exit 4 is exactly the dogfood loop's rebuild
	// trigger) should be reported as errImageHostTainted (exit 5) — see
	// TestClassifyStaleOutcome_NonConverging_DiagsAndHalts, which drives the
	// identical same-rev-repeat shape straight through classifyStaleOutcome
	// with no intervening issue-close and correctly gets exit 5. Here the
	// guard-reset in call 2 makes tracker.prior() == "" again, so
	// classifyStaleOutcome's NonConverging check sees no prior rev to match
	// against and falls back to the rebuild-and-retry verdict. Once #2127's
	// fix scopes tracker.clear() away from an unrelated issue-close success,
	// flip this assertion to 5 -- and it must then pass.
	if got := exitCodeFor(err3); got != 4 {
		t.Fatalf("call 3: exitCodeFor(err3) = %d, want 4 (the CURRENT WRONG behavior pinned by this characterization test — should become 5/errImageHostTainted once #2127 is fixed)", got)
	}
	if got := newStaleRevTracker(dir).prior(); got != rev {
		t.Fatalf("call 3: tracker.prior() = %q, want %q (same rev R re-recorded as content staleness)", got, rev)
	}
}
