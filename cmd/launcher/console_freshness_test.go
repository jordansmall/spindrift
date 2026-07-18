package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/freshness"
)

var errBoomFreshness = errors.New("pull failed")

// TestNewConsoleFreshnessChecker_RebuildThenCheck_ReportsFreshAtSameTip
// verifies the checker recognizes a tip it just rebuilt against as fresh —
// the fix for the static imageTag comparand baked at process start never
// updating in-process (issue #652). Without this, a successful rebuild
// would leave the very next freshness check reporting stale forever, since
// Probe's comparand (c.imageTag) can't be recomputed without a fresh
// process. probe is scripted directly (no real git/nix) since the
// checker's own caching logic, not freshness.Probe's plumbing, is under
// test here — Probe's own git/eval seam is exercised by internal/freshness's
// tests instead.
func TestNewConsoleFreshnessChecker_RebuildThenCheck_ReportsFreshAtSameTip(t *testing.T) {
	rev := "abc123"
	stale := freshness.Result{Applicable: true, Fresh: false, Rev: rev, Message: "rebuild needed"}
	probeCalls := 0
	probe := func() freshness.Result { probeCalls++; return stale }

	buildCalls := 0
	fresh, rebuild := newConsoleFreshnessChecker("main", probe, func() (string, string, error) { return rev, "", nil }, func() (string, error) { buildCalls++; return "", nil })

	if applicable, isFresh, msg := fresh(); !applicable || isFresh {
		t.Fatalf("initial check: applicable=%v fresh=%v msg=%q, want stale", applicable, isFresh, msg)
	}

	if _, _, err := rebuild(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if buildCalls != 1 {
		t.Fatalf("buildCalls = %d, want 1", buildCalls)
	}
	if probeCalls != 1 {
		t.Fatalf("probeCalls = %d, want 1 (the initial check only — rebuild now derives builtRev from pull's return value, not its own re-probe)", probeCalls)
	}

	if applicable, isFresh, msg := fresh(); !applicable || !isFresh {
		t.Errorf("after rebuild: applicable=%v fresh=%v msg=%q, want fresh", applicable, isFresh, msg)
	}
}

// TestNewConsoleFreshnessChecker_OriginAdvancesAfterRebuild_StaleAgain
// verifies the rev-based fresh cache doesn't paper over a genuine second
// staleness: once the underlying probe reports a different rev than the one
// rebuild last rebuilt, the checker must report stale again rather than
// treating any prior rebuild as permanently sufficient.
func TestNewConsoleFreshnessChecker_OriginAdvancesAfterRebuild_StaleAgain(t *testing.T) {
	res := freshness.Result{Applicable: true, Fresh: false, Rev: "abc123", Message: "rebuild needed"}
	probe := func() freshness.Result { return res }

	fresh, rebuild := newConsoleFreshnessChecker("main", probe, func() (string, string, error) { return "abc123", "", nil }, func() (string, error) { return "", nil })
	if _, _, err := rebuild(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if _, isFresh, _ := fresh(); !isFresh {
		t.Fatalf("fresh() after the first rebuild reported stale, want fresh")
	}

	// The base branch advanced further after the rebuild — the probe now
	// reports a newer rev the checker never rebuilt against.
	res = freshness.Result{Applicable: true, Fresh: false, Rev: "def456", Message: "rebuild needed again"}

	if applicable, isFresh, msg := fresh(); !applicable || isFresh {
		t.Errorf("after the probe advanced past the rebuilt rev: applicable=%v fresh=%v msg=%q, want stale", applicable, isFresh, msg)
	}
}

// TestNewConsoleFreshnessChecker_AlreadyFresh_PassesThroughUnchanged
// verifies a probe result that is already fresh is returned as-is, with no
// rev-cache override applied — the caching path only ever matters for a
// stale verdict.
func TestNewConsoleFreshnessChecker_AlreadyFresh_PassesThroughUnchanged(t *testing.T) {
	res := freshness.Result{Applicable: true, Fresh: true, Rev: "abc123", Message: "fresh"}
	probe := func() freshness.Result { return res }

	fresh, _ := newConsoleFreshnessChecker("main", probe, func() (string, string, error) { return "abc123", "", nil }, func() (string, error) { return "", nil })

	applicable, isFresh, msg := fresh()
	if !applicable || !isFresh || msg != "fresh" {
		t.Errorf("fresh() = (%v, %v, %q), want the probe's own fresh result unchanged", applicable, isFresh, msg)
	}
}

// TestNewConsoleFreshnessChecker_OriginAdvancesDuringRebuild_BuiltRevIsPulledRev
// verifies the TOCTOU fix (issue #767): builtRev must be the rev pull()
// actually checked out (and build() actually built), not whatever rev a
// post-build probe() happens to see. probe() re-fetches origin
// independently on every call, so if origin advances while build() is
// running, a rebuild() that derived builtRev from its own trailing probe()
// call would cache the advanced rev — one nobody ever built — and the next
// fresh() would false-positive at that rev.
func TestNewConsoleFreshnessChecker_OriginAdvancesDuringRebuild_BuiltRevIsPulledRev(t *testing.T) {
	const pulledRev = "abc123"   // what pull() checked out and build() built
	const advancedRev = "def456" // origin's tip by the time probe() next runs

	pull := func() (string, string, error) { return pulledRev, "", nil }
	build := func() (string, error) { return "", nil }
	probe := func() freshness.Result {
		return freshness.Result{Applicable: true, Fresh: false, Rev: advancedRev, Message: "rebuild needed"}
	}

	fresh, rebuild := newConsoleFreshnessChecker("main", probe, pull, build)

	if _, _, err := rebuild(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if applicable, isFresh, msg := fresh(); applicable && isFresh {
		t.Errorf("fresh() = (applicable=%v, isFresh=%v, msg=%q), want stale: builtRev must be the pulled rev %q, not the advanced rev %q the checker never built", applicable, isFresh, msg, pulledRev, advancedRev)
	}
}

// TestNewConsoleFreshnessChecker_RebuildPropagatesPullAndBuildErrors
// verifies rebuild returns pull's or build's error without probing again or
// updating the cached rev — a failed rebuild must never look like a
// successful one on the next check.
func TestNewConsoleFreshnessChecker_RebuildPropagatesPullAndBuildErrors(t *testing.T) {
	probeCalls := 0
	probe := func() freshness.Result {
		probeCalls++
		return freshness.Result{Applicable: true, Fresh: false, Rev: "abc123"}
	}

	_, rebuild := newConsoleFreshnessChecker("main", probe, func() (string, string, error) { return "", "", errBoomFreshness }, func() (string, error) {
		t.Fatal("build called after pull failed")
		return "", nil
	})
	if _, _, err := rebuild(); err != errBoomFreshness {
		t.Errorf("rebuild() = %v, want the pull error", err)
	}
	if probeCalls != 0 {
		t.Errorf("probeCalls = %d, want 0 when pull fails before any probe", probeCalls)
	}
}

// TestNewConsoleFreshnessChecker_Rebuild_PropagatesPullNotice verifies
// rebuild's return threads pull's branch-switch notice through alongside
// build's output — the seam consoleGitSync's notice (issue #1141) needs to
// reach the console's rendered status through, without rebuild re-deriving
// it itself.
func TestNewConsoleFreshnessChecker_Rebuild_PropagatesPullNotice(t *testing.T) {
	const wantNotice = "switched off-branch tree from feature to main"
	probe := func() freshness.Result { return freshness.Result{} }
	pull := func() (string, string, error) { return "abc123", wantNotice, nil }
	build := func() (string, error) { return "", nil }

	_, rebuild := newConsoleFreshnessChecker("main", probe, pull, build)

	_, notice, err := rebuild()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if notice != wantNotice {
		t.Errorf("rebuild() notice = %q, want %q", notice, wantNotice)
	}
}

// TestConsoleGitSync_DirtyOffBranch_RefusesCheckout verifies that when pwd is
// on a branch other than baseBranch and has uncommitted changes,
// consoleGitSync refuses the checkout instead of silently carrying those
// changes onto baseBranch (issue #769) — the exact "unexpected branch
// switch" the README's "non-destructive" claim glossed over: git itself
// only blocks a checkout that would overwrite a *conflicting* file, so a
// non-conflicting dirty change rides along in silence.
func TestConsoleGitSync_DirtyOffBranch_RefusesCheckout(t *testing.T) {
	pwd := newConsoleGitRepo(t, "main")
	gitRun(t, pwd, "checkout", "-b", "feature")
	gitWriteFile(t, filepath.Join(pwd, "flake.nix"), "{ dirty = true; }\n")

	if _, _, err := consoleGitSync(pwd, "main"); err == nil {
		t.Fatal("consoleGitSync() = nil, want an error refusing the checkout")
	}

	branch, err := gitOutput(pwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		t.Fatalf("gitOutput: %v", err)
	}
	if branch != "feature" {
		t.Errorf("branch = %q after refused checkout, want to stay on %q", branch, "feature")
	}
}

// TestConsoleGitSync_DirtyOnBaseBranch_StillSyncs verifies that a dirty tree
// already on baseBranch is not blocked — checking out the branch pwd is
// already on carries nothing across, so the precondition in
// TestConsoleGitSync_DirtyOffBranch_RefusesCheckout must key off "off
// baseBranch AND dirty", not "dirty" alone, or every routine rebuild with
// scratch files present would wrongly refuse to sync.
func TestConsoleGitSync_DirtyOnBaseBranch_StillSyncs(t *testing.T) {
	pwd := newConsoleGitRepo(t, "main")
	gitWriteFile(t, filepath.Join(pwd, "scratch.txt"), "untracked\n")

	if _, _, err := consoleGitSync(pwd, "main"); err != nil {
		t.Fatalf("consoleGitSync() = %v, want nil for a dirty tree already on baseBranch", err)
	}
}

// TestHeadRevAndProbeRev_SameCommit_IdenticalFormat verifies headRev and
// freshness.Probe's Result.Rev — both backed by a plain `git rev-parse` with
// no --short/--abbrev flag — return identically formatted full SHAs for the
// same commit, not just equal-looking strings. This is the format guarantee
// newConsoleFreshnessChecker's res.Rev == builtRev comparison relies on: a
// future --short added to either call site would silently break that match,
// and this test would catch it since a shortened Rev wouldn't equal a
// full-length headRev output.
func TestHeadRevAndProbeRev_SameCommit_IdenticalFormat(t *testing.T) {
	pwd := newConsoleGitRepo(t, "main")

	head, err := headRev(pwd)
	if err != nil {
		t.Fatalf("headRev: %v", err)
	}

	eval := &freshness.Fake{OutPath: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-agent-image"}
	res := freshness.Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "spindrift:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", eval)

	if res.Rev != head {
		t.Errorf("Probe Rev = %q, headRev = %q, want identical for the same commit", res.Rev, head)
	}
	if len(head) != 40 && len(head) != 64 {
		t.Errorf("headRev length = %d, want 40 (SHA-1) or 64 (SHA-256), no --short", len(head))
	}
}

// TestConsoleGitSync_CleanOffBranch_ReturnsSwitchNotice verifies that a
// clean tree on a branch other than baseBranch gets a notice describing the
// switch, naming both branches — the silent-switch gap issue #1141 closes:
// checkCheckoutSafe lets this checkout proceed (nothing dirty to carry
// across), but until now nothing told the operator their pwd moved off the
// branch they had checked out.
func TestConsoleGitSync_CleanOffBranch_ReturnsSwitchNotice(t *testing.T) {
	pwd := newConsoleGitRepo(t, "main")
	gitRun(t, pwd, "checkout", "-b", "feature")

	_, notice, err := consoleGitSync(pwd, "main")
	if err != nil {
		t.Fatalf("consoleGitSync() = %v, want nil for a clean off-branch tree", err)
	}
	if !strings.Contains(notice, "feature") || !strings.Contains(notice, "main") {
		t.Errorf("notice = %q, want it to name both the old branch %q and baseBranch %q", notice, "feature", "main")
	}
}

// TestConsoleGitSync_AlreadyOnBaseBranch_NoSwitchNotice verifies that
// syncing while already on baseBranch returns no notice — no real switch
// occurred, so there's nothing to tell the operator.
func TestConsoleGitSync_AlreadyOnBaseBranch_NoSwitchNotice(t *testing.T) {
	pwd := newConsoleGitRepo(t, "main")

	_, notice, err := consoleGitSync(pwd, "main")
	if err != nil {
		t.Fatalf("consoleGitSync() = %v, want nil", err)
	}
	if notice != "" {
		t.Errorf("notice = %q, want empty when already on baseBranch", notice)
	}
}

// TestHeadRev_ReturnsReposCurrentCommit verifies headRev reports the same
// commit hash git itself reports for pwd's checked-out HEAD — the seam
// headRev shares with gitOutput once headRev delegates to it (issue #1133).
func TestHeadRev_ReturnsReposCurrentCommit(t *testing.T) {
	pwd := newConsoleGitRepo(t, "main")

	want, err := exec.Command("git", "-C", pwd, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}

	got, err := headRev(pwd)
	if err != nil {
		t.Fatalf("headRev() = %v", err)
	}
	if got != strings.TrimSpace(string(want)) {
		t.Errorf("headRev() = %q, want %q", got, strings.TrimSpace(string(want)))
	}
}

// gitRun runs git in dir, failing the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func gitWriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newConsoleGitRepo sets up a bare "origin" repo with a single commit on
// baseBranch and a local clone of it, matching the shape the launcher's own
// pwd has in production: a checkout with an "origin" remote.
func newConsoleGitRepo(t *testing.T, baseBranch string) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	clone := filepath.Join(dir, "clone")

	gitRun(t, "", "init", "--bare", bare)
	gitRun(t, "", "clone", bare, clone)
	gitRun(t, clone, "checkout", "-B", baseBranch)
	gitRun(t, clone, "config", "user.email", "test@example.com")
	gitRun(t, clone, "config", "user.name", "Test")
	gitWriteFile(t, filepath.Join(clone, "flake.nix"), "{ }\n")
	gitRun(t, clone, "add", "flake.nix")
	gitRun(t, clone, "commit", "-m", "base")
	gitRun(t, clone, "push", "-u", "origin", baseBranch)

	return clone
}
