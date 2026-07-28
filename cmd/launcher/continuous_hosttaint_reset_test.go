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
// CHARACTERIZATION test for issue #2127: it pins the exact mechanism by which
// a fully-local (ISSUE_TRACKER=local, CODE_FORGE=local) continuous-dispatch
// run that closes an issue mid-stream silently discards the host-taint
// non-convergence guard (issue #2113's classifyStaleOutcome/staleRevTracker),
// forcing every subsequent same-rev stale verdict to re-enter the
// rebuild-and-retry path (exit 4) instead of ever reaching the halt path
// (exit 5) — even though, from the freshness probe's point of view, nothing
// about the divergence changed.
//
// The mechanism (Theory A) is exactly one line: runContinuousDispatch's
// success path at cmd/launcher/main.go:1291,
//
//	_ = tracker.clear()
//
// runs on EVERY nil-error return from waves.RunContinuous — including the
// completely unrelated case where a run dispatched and settled an issue
// (closing it) with no freshness involvement at all — because tracker.clear()
// is unconditional on the success path, not scoped to "the divergence this
// tracker recorded actually resolved." A prior run's recorded stale rev (the
// guard armed by classifyStaleOutcome's tracker.record call, guarding against
// exactly this rev recurring) is wiped by the next run for the unrelated
// reason that an issue happened to close, resetting the host-taint detector's
// only state.
//
// This test proves that mechanism deterministically, in-process, with three
// direct runContinuousDispatch calls sharing one pwd and one persisted
// tracker file, no macOS host and no real image build:
//
//  1. A stale probe (staleEval's tag never matches the loaded image) with no
//     prior recorded rev is content staleness by definition (issue #2113):
//     classifyStaleOutcome returns waves.ErrImageStale (exit 4) and records
//     the fetched base rev R as the guard. In a real dogfood loop, exit 4 is
//     exactly the signal that drives a rebuild attempt against R.
//  2. A FRESH probe on the same pwd lets refill dispatch and settle the one
//     open issue; RunContinuous returns nil, so runContinuousDispatch takes
//     its success path and unconditionally clears the tracker
//     (main.go:1291) before reconcileAfterDispatch even runs. The guard
//     armed in step 1 is gone — with no rebuild ever having happened.
//  3. A stale probe again, at the SAME rev R (the origin repo never moved
//     between calls) — the same signature a genuinely host-tainted image
//     produces after a real rebuild attempt failed to converge. But because
//     step 2 wiped the guard, tracker.prior() is "" again here, so
//     classifyStaleOutcome's NonConverging(R, "") check is false: it
//     misclassifies this same-rev repeat as fresh content staleness (again
//     exit 4) instead of the host-taint halt (exit 5) it would have reported
//     had the guard survived. Contrast
//     TestClassifyStaleOutcome_NonConverging_DiagsAndHalts, which drives the
//     same same-rev-repeat shape directly against classifyStaleOutcome (no
//     intervening issue-close) and correctly gets errImageHostTainted.
//
// Call 3 asserting exit 4 (rather than 5) is the CURRENT, WRONG behavior this
// test pins — issue #2127's fix must flip that assertion to 5 once the
// guard-reset is scoped away from an unrelated issue-close success path
// (e.g. tracker.clear() only firing when the run's own freshness check, not
// merely its dispatch outcome, was clean). Until then this test must stay
// green as the CI-safe reproduction; whether the real macOS dogfood image is
// genuinely host-tainted in exactly this same-rev-perpetual-staleness way
// still needs a real macOS run to confirm — this test only pins the guard's
// in-process reset mechanism, not the production divergence itself.
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

	// --- call 2: fresh -- refill dispatches and settles issue #1, closing it;
	// the success path's unconditional tracker.clear() (main.go:1291) wipes
	// the guard armed above, for a reason wholly unrelated to freshness. ---
	err2 := runContinuousDispatch(c, it, cf, dir, f, s, freshEval, lp)
	if err2 != nil {
		t.Fatalf("call 2: runContinuousDispatch = %v, want nil (fresh probe + one dispatchable issue settles cleanly)", err2)
	}
	if len(fr.RunCalls) != 1 || fr.RunCalls[0].Issue != "1" {
		t.Fatalf("call 2: fr.RunCalls = %v, want exactly one Run call for issue #1", fr.RunCalls)
	}
	if got := newStaleRevTracker(dir).prior(); got != "" {
		t.Fatalf("call 2: tracker.prior() = %q, want empty -- THE BUG under test: an unrelated issue-close reset the host-taint guard armed by call 1 (main.go:1291's tracker.clear() is unconditional on the success path, not scoped to a resolved divergence)", got)
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
