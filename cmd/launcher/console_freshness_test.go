package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	fresh, rebuild := newConsoleFreshnessChecker("main", probe, func() error { return nil }, func() (string, error) { buildCalls++; return "", nil })

	if applicable, isFresh, msg := fresh(); !applicable || isFresh {
		t.Fatalf("initial check: applicable=%v fresh=%v msg=%q, want stale", applicable, isFresh, msg)
	}

	if _, err := rebuild(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if buildCalls != 1 {
		t.Fatalf("buildCalls = %d, want 1", buildCalls)
	}
	if probeCalls != 2 {
		t.Fatalf("probeCalls = %d, want 2 (one for the initial check, one for rebuild's own re-probe)", probeCalls)
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

	fresh, rebuild := newConsoleFreshnessChecker("main", probe, func() error { return nil }, func() (string, error) { return "", nil })
	if _, err := rebuild(); err != nil {
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

	fresh, _ := newConsoleFreshnessChecker("main", probe, func() error { return nil }, func() (string, error) { return "", nil })

	applicable, isFresh, msg := fresh()
	if !applicable || !isFresh || msg != "fresh" {
		t.Errorf("fresh() = (%v, %v, %q), want the probe's own fresh result unchanged", applicable, isFresh, msg)
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

	_, rebuild := newConsoleFreshnessChecker("main", probe, func() error { return errBoomFreshness }, func() (string, error) {
		t.Fatal("build called after pull failed")
		return "", nil
	})
	if _, err := rebuild(); err != errBoomFreshness {
		t.Errorf("rebuild() = %v, want the pull error", err)
	}
	if probeCalls != 0 {
		t.Errorf("probeCalls = %d, want 0 when pull fails before any probe", probeCalls)
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

	if err := consoleGitSync(pwd, "main"); err == nil {
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

	if err := consoleGitSync(pwd, "main"); err != nil {
		t.Fatalf("consoleGitSync() = %v, want nil for a dirty tree already on baseBranch", err)
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
