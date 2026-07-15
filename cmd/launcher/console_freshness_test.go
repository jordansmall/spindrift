package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/freshness"
)

// consoleFreshnessGitRun runs git in dir, failing the test on error — a
// small local copy of the freshness package's own test fixture (unexported
// there, so not reachable from this package's tests).
func consoleFreshnessGitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// newConsoleFreshnessTestClone sets up a bare "origin" repo with a single
// commit on baseBranch and a local clone of it — the shape newConsoleFreshness's
// underlying freshness.Probe call expects a real pwd to have.
func newConsoleFreshnessTestClone(t *testing.T, baseBranch string) string {
	t.Helper()
	dir := t.TempDir()
	bare := filepath.Join(dir, "origin.git")
	clone := filepath.Join(dir, "clone")

	consoleFreshnessGitRun(t, "", "init", "--bare", bare)
	consoleFreshnessGitRun(t, "", "clone", bare, clone)
	consoleFreshnessGitRun(t, clone, "checkout", "-B", baseBranch)
	consoleFreshnessGitRun(t, clone, "config", "user.email", "test@example.com")
	consoleFreshnessGitRun(t, clone, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(clone, "flake.nix"), []byte("{ }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	consoleFreshnessGitRun(t, clone, "add", "flake.nix")
	consoleFreshnessGitRun(t, clone, "commit", "-m", "base")
	consoleFreshnessGitRun(t, clone, "push", "-u", "origin", baseBranch)

	return clone
}

// consoleFreshnessAdvanceOrigin commits a new file on baseBranch in a
// second clone of pwd's own origin and pushes it, simulating a merge
// landing on the base branch after pwd's own clone was made — without
// touching pwd itself.
func consoleFreshnessAdvanceOrigin(t *testing.T, pwd, baseBranch string) {
	t.Helper()
	origin := strings.TrimSpace(runGitOutput(t, pwd, "remote", "get-url", "origin"))
	second := t.TempDir()
	consoleFreshnessGitRun(t, "", "clone", origin, second)
	consoleFreshnessGitRun(t, second, "checkout", baseBranch)
	consoleFreshnessGitRun(t, second, "config", "user.email", "test@example.com")
	consoleFreshnessGitRun(t, second, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(second, "again.txt"), []byte("again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	consoleFreshnessGitRun(t, second, "add", "again.txt")
	consoleFreshnessGitRun(t, second, "commit", "-m", "advance again")
	consoleFreshnessGitRun(t, second, "push", "origin", baseBranch)
}

// runGitOutput runs git in dir and returns its stdout, failing the test on
// error.
func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// TestNewConsoleFreshness_RebuildThenCheck_ReportsFreshAtSameTip verifies
// the checker recognizes a tip it just rebuilt against as fresh — the fix
// for the static imageTag comparand baked at process start never updating
// in-process (issue #652). Without this, a successful rebuild would leave
// the very next freshness check reporting stale forever, since Probe's
// comparand (c.imageTag) can't be recomputed without a fresh process.
func TestNewConsoleFreshness_RebuildThenCheck_ReportsFreshAtSameTip(t *testing.T) {
	pwd := newConsoleFreshnessTestClone(t, "main")
	eval := &freshness.Fake{OutPath: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-agent-image"}
	c := config{
		runtime:        "podman",
		baseBranch:     "main",
		flakeImageAttr: ".#packages.x86_64-linux.agent-image",
		imageTag:       "spindrift:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
	}

	buildCalls := 0
	fresh, rebuild := newConsoleFreshness(c, pwd, eval, func() error { return nil }, func() error { buildCalls++; return nil })

	if applicable, isFresh, msg := fresh(); !applicable || isFresh {
		t.Fatalf("initial check: applicable=%v fresh=%v msg=%q, want stale", applicable, isFresh, msg)
	}

	if err := rebuild(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if buildCalls != 1 {
		t.Fatalf("buildCalls = %d, want 1", buildCalls)
	}

	if applicable, isFresh, msg := fresh(); !applicable || !isFresh {
		t.Errorf("after rebuild: applicable=%v fresh=%v msg=%q, want fresh", applicable, isFresh, msg)
	}
}

// TestNewConsoleFreshness_OriginAdvancesAfterRebuild_StaleAgain verifies the
// rev-based fresh cache doesn't paper over a genuine second staleness: once
// origin/baseBranch moves past the rev rebuild last rebuilt, the checker
// must report stale again rather than treating any prior rebuild as
// permanently sufficient.
func TestNewConsoleFreshness_OriginAdvancesAfterRebuild_StaleAgain(t *testing.T) {
	pwd := newConsoleFreshnessTestClone(t, "main")
	eval := &freshness.Fake{OutPath: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-agent-image"}
	c := config{
		runtime:        "podman",
		baseBranch:     "main",
		flakeImageAttr: ".#packages.x86_64-linux.agent-image",
		imageTag:       "spindrift:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
	}

	fresh, rebuild := newConsoleFreshness(c, pwd, eval, func() error { return nil }, func() error { return nil })
	if err := rebuild(); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if _, isFresh, _ := fresh(); !isFresh {
		t.Fatalf("fresh() after the first rebuild reported stale, want fresh")
	}

	// A second clone pushes a further commit to origin/main — the checker's
	// cached rev is now behind the tip again.
	consoleFreshnessAdvanceOrigin(t, pwd, "main")

	if applicable, isFresh, msg := fresh(); !applicable || isFresh {
		t.Errorf("after origin advanced past the rebuilt rev: applicable=%v fresh=%v msg=%q, want stale", applicable, isFresh, msg)
	}
}
