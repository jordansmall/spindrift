package freshness

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var errEvalBoom = errors.New("nix eval boom")

// TestProbe_BwrapNotApplicable verifies that the bwrap runtime — which keeps
// its store read-only and has no loaded image to compare — reports the probe
// as not applicable rather than attempting a fetch or eval.
func TestProbe_BwrapNotApplicable(t *testing.T) {
	res := Probe("bwrap", "/nonexistent", "main", ".#packages.x86_64-linux.agent-image", "/nix/store/abc.drv", nil)

	if res.Applicable {
		t.Errorf("Applicable = true, want false for bwrap")
	}
	if res.Message == "" {
		t.Error("Message is empty, want a not-applicable explanation")
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

// newCloneWithOrigin sets up a bare "origin" repo with a single commit on
// baseBranch and a local clone of it, matching the shape the launcher's own
// pwd has in production: a checkout with an "origin" remote. Returns the
// clone directory.
func newCloneWithOrigin(t *testing.T, baseBranch string) string {
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

func gitWriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestProbe_FreshWhenDrvPathMatches verifies that a drvPath evaluated at the
// fetched base tip equal to the baked imageDrv reports fresh.
func TestProbe_FreshWhenDrvPathMatches(t *testing.T) {
	pwd := newCloneWithOrigin(t, "main")
	eval := &Fake{DrvPath: "/nix/store/same.drv"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "/nix/store/same.drv", eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for podman runtime")
	}
	if !res.Fresh {
		t.Errorf("Fresh = false, want true when drvPath matches; message: %s", res.Message)
	}
	if len(eval.Calls) != 1 {
		t.Fatalf("Eval called %d times, want 1", len(eval.Calls))
	}
	if eval.Calls[0].Pwd != pwd {
		t.Errorf("Eval called with pwd %q, want %q", eval.Calls[0].Pwd, pwd)
	}
}

// TestProbe_EvalReceivesFetchedRev verifies that Probe passes the fetched
// base-tip sha (not the local clone's own checked-out HEAD) to Eval — the
// wiring that makes the eval hermetic against the fetched tip rather than
// whatever pwd happens to have checked out.
func TestProbe_EvalReceivesFetchedRev(t *testing.T) {
	pwd := newCloneWithOrigin(t, "main")
	localHead := gitOutput(t, pwd, "rev-parse", "HEAD")
	advancedSha, err := gitAdvanceOrigin(t, pwd, "main")
	if err != nil {
		t.Fatalf("gitAdvanceOrigin: %v", err)
	}
	eval := &Fake{DrvPath: "/nix/store/same.drv"}

	Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "/nix/store/same.drv", eval)

	if len(eval.Calls) != 1 {
		t.Fatalf("Eval called %d times, want 1", len(eval.Calls))
	}
	if eval.Calls[0].Rev != advancedSha {
		t.Errorf("Eval called with rev %q, want the fetched base tip %q", eval.Calls[0].Rev, advancedSha)
	}
	if eval.Calls[0].Rev == localHead {
		t.Errorf("Eval called with rev %q, the clone's own stale checked-out HEAD, not the fetched tip", eval.Calls[0].Rev)
	}
}

// TestProbe_RebuildNeededWhenDrvPathDiffers verifies that a base-tip commit
// which changed image inputs — a different evaluated drvPath — reports
// rebuild-needed, not fresh.
func TestProbe_RebuildNeededWhenDrvPathDiffers(t *testing.T) {
	pwd := newCloneWithOrigin(t, "main")
	eval := &Fake{DrvPath: "/nix/store/new.drv"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "/nix/store/old.drv", eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for podman runtime")
	}
	if res.Fresh {
		t.Errorf("Fresh = true, want false when drvPath differs; message: %s", res.Message)
	}
}

// TestProbe_EvalFailureFailsClosed verifies that an eval error reports
// rebuild-needed with a loud message rather than guessing fresh.
func TestProbe_EvalFailureFailsClosed(t *testing.T) {
	pwd := newCloneWithOrigin(t, "main")
	eval := &Fake{Err: errEvalBoom}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "/nix/store/old.drv", eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for podman runtime")
	}
	if res.Fresh {
		t.Errorf("Fresh = true, want false (fail closed) on eval error")
	}
	if !strings.Contains(res.Message, errEvalBoom.Error()) {
		t.Errorf("Message %q does not surface the eval error", res.Message)
	}
}

// TestProbe_FetchFailureFailsClosed verifies that a git fetch error (e.g. no
// "origin" remote reachable) reports rebuild-needed with a loud message,
// without ever calling the evaluator.
func TestProbe_FetchFailureFailsClosed(t *testing.T) {
	pwd := t.TempDir()
	gitRun(t, pwd, "init")
	eval := &Fake{DrvPath: "/nix/store/same.drv"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "/nix/store/same.drv", eval)

	if !res.Applicable {
		t.Fatalf("Applicable = false, want true for podman runtime")
	}
	if res.Fresh {
		t.Errorf("Fresh = true, want false (fail closed) on fetch error")
	}
	if len(eval.Calls) != 0 {
		t.Errorf("Eval called %d times, want 0 when fetch fails", len(eval.Calls))
	}
}

// TestProbe_FetchFailure_MessageIncludesGitStderr verifies that the loud
// fetch-failure message surfaces git's own diagnostic (its stderr), not just
// the bare exit status, so an operator reading `preview` output can see why.
func TestProbe_FetchFailure_MessageIncludesGitStderr(t *testing.T) {
	pwd := t.TempDir()
	gitRun(t, pwd, "init")
	gitRun(t, pwd, "remote", "add", "origin", filepath.Join(pwd, "does-not-exist.git"))
	eval := &Fake{DrvPath: "/nix/store/same.drv"}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "/nix/store/same.drv", eval)

	if strings.Contains(res.Message, "exit status") && !strings.Contains(res.Message, "does-not-exist") {
		t.Errorf("Message %q looks like a bare exit code, want git's stderr detail", res.Message)
	}
}

// TestProbe_NeverMutatesWorkingCopy verifies that Probe fetches the base tip
// without checking it out — the local clone's checked-out commit and dirty
// files are unchanged after the call.
func TestProbe_NeverMutatesWorkingCopy(t *testing.T) {
	pwd := newCloneWithOrigin(t, "main")
	before := gitOutput(t, pwd, "rev-parse", "HEAD")
	eval := &Fake{DrvPath: "/nix/store/same.drv"}

	if _, err := gitAdvanceOrigin(t, pwd, "main"); err != nil {
		t.Fatalf("gitAdvanceOrigin: %v", err)
	}

	res := Probe("podman", pwd, "main", ".#packages.x86_64-linux.agent-image", "/nix/store/same.drv", eval)
	if !res.Applicable {
		t.Fatalf("Applicable = false, want true")
	}

	after := gitOutput(t, pwd, "rev-parse", "HEAD")
	if before != after {
		t.Errorf("checked-out HEAD changed: %q -> %q; Probe must never check out", before, after)
	}
	status := gitOutput(t, pwd, "status", "--porcelain")
	if status != "" {
		t.Errorf("working copy dirtied by Probe: %q", status)
	}
}

// gitOutput runs git in dir and returns trimmed stdout, failing the test on
// error.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// gitAdvanceOrigin commits a new file on baseBranch in a second clone of the
// same origin as pwd and pushes it, simulating a merge landing on the base
// branch after pwd's own clone was made — without touching pwd itself.
func gitAdvanceOrigin(t *testing.T, pwd, baseBranch string) (string, error) {
	t.Helper()
	origin := gitOutput(t, pwd, "remote", "get-url", "origin")
	second := t.TempDir()
	gitRun(t, "", "clone", origin, second)
	gitRun(t, second, "checkout", baseBranch)
	gitRun(t, second, "config", "user.email", "test@example.com")
	gitRun(t, second, "config", "user.name", "Test")
	gitWriteFile(t, filepath.Join(second, "new.txt"), "new\n")
	gitRun(t, second, "add", "new.txt")
	gitRun(t, second, "commit", "-m", "advance")
	gitRun(t, second, "push", "origin", baseBranch)
	return gitOutput(t, second, "rev-parse", "HEAD"), nil
}
