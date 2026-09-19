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

// The checker must recognize a tip it just rebuilt against as fresh: the
// imageTag comparand baked at process start never updated in-process (issue
// #652), so a successful rebuild left the next check reporting stale forever.
// probe is scripted directly because the checker's own caching logic is under
// test here, not freshness.Probe's git/eval plumbing.
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

// The rev-based fresh cache must not paper over a genuine second staleness:
// once the probe reports a different rev than the one rebuild last built, the
// checker reports stale again rather than treating any prior rebuild as
// permanently sufficient.
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

	// The base branch advanced past the rebuild, so the probe now reports a
	// rev the checker never built.
	res = freshness.Result{Applicable: true, Fresh: false, Rev: "def456", Message: "rebuild needed again"}

	if applicable, isFresh, msg := fresh(); !applicable || isFresh {
		t.Errorf("after the probe advanced past the rebuilt rev: applicable=%v fresh=%v msg=%q, want stale", applicable, isFresh, msg)
	}
}

// A probe result that is already fresh passes through unchanged, with no
// rev-cache override: the caching path only ever matters for a stale verdict.
func TestNewConsoleFreshnessChecker_AlreadyFresh_PassesThroughUnchanged(t *testing.T) {
	res := freshness.Result{Applicable: true, Fresh: true, Rev: "abc123", Message: "fresh"}
	probe := func() freshness.Result { return res }

	fresh, _ := newConsoleFreshnessChecker("main", probe, func() (string, string, error) { return "abc123", "", nil }, func() (string, error) { return "", nil })

	applicable, isFresh, msg := fresh()
	if !applicable || !isFresh || msg != "fresh" {
		t.Errorf("fresh() = (%v, %v, %q), want the probe's own fresh result unchanged", applicable, isFresh, msg)
	}
}

// This test pins the TOCTOU fix (issue #767): builtRev must be the rev pull()
// checked out and build() built, not whatever rev a post-build probe() sees.
// probe() re-fetches origin on every call, so if origin advances while build()
// runs, a builtRev derived from a trailing probe() would cache a rev nobody
// built and the next fresh() would false-positive there.
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

// rebuild returns pull's or build's error without probing again or updating
// the cached rev: a failed rebuild must never look like a successful one on
// the next check.
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

// rebuild returns pull's branch-switch notice alongside build's output instead
// of re-deriving it, so consoleGitSync's notice (issue #1141) reaches the
// console's rendered status.
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

// newConsoleFreshness must wire config.runnerKind, not config.runtime, into
// freshness.Probe (issue #2538 AC1/AC2). The bwrap arm compares outPath to
// imageTag byte-for-byte, while a runtime-name read takes the OCI arm and
// derives a "spindrift:<hash>" tag that can never equal this colon-less
// imageTag, so Fresh=true with no "spindrift:" tells the two reads apart.
func TestNewConsoleFreshness_UsesRunnerKindNotRuntime(t *testing.T) {
	const outPath = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-agent-closure"

	c := baseConfig()
	c.runnerKind = "bwrap"
	c.runtime = "podman"
	c.baseBranch = "main"
	c.imageTag = outPath // bare store path with no colon, bwrap's own tag shape
	eval := &freshness.Fake{OutPath: outPath}

	pwd := newConsoleGitRepo(t, "main")
	fresh, _ := newConsoleFreshness(c, pwd, eval, nil, nil)

	applicable, isFresh, msg := fresh()
	if !applicable || !isFresh {
		t.Fatalf("fresh() = applicable=%v fresh=%v msg=%q, want applicable=true fresh=true (the bwrap arm's direct outPath-vs-imageTag comparison; a wrong c.runtime read would take the OCI arm instead, which can never match a colon-less imageTag)", applicable, isFresh, msg)
	}
	if strings.Contains(msg, "spindrift:") {
		t.Errorf("fresh() message %q names an OCI-style %q tag, want the bwrap arm's bare-outPath comparison — proves runnerKind (not runtime) drove Probe", msg, "spindrift:")
	}
}

// newConsoleFreshness must omit the launcher-freshness dimension from its
// freshness.Probe call (blocking review finding on issue #1364): the Console's
// Rebuild only pulls and rebuilds the image, never the host launcher binary,
// so a launcher-stale verdict could never be resolved here. flakeLauncherAttr
// and loadedLauncherHash are set to values that would report launcher-stale.
func TestNewConsoleFreshness_NeverWiresLauncherDimension(t *testing.T) {
	const imageHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const launcherHashOnDisk = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" // mismatches loadedLauncherHash below

	c := baseConfig()
	c.runnerKind = "podman"
	c.baseBranch = "main"
	c.flakeImageAttr = ".#packages.x86_64-linux.agent-image"
	c.imageTag = "spindrift:" + imageHash
	c.flakeLauncherAttr = ".#packages.x86_64-linux.launcher"
	c.loadedLauncherHash = "cccccccccccccccccccccccccccccccc"

	eval := &freshness.Fake{
		OutPathForAttr: map[string]string{
			"packages.x86_64-linux.agent-image": "/nix/store/" + imageHash + "-agent-image",
			"packages.x86_64-linux.launcher":    "/nix/store/" + launcherHashOnDisk + "-launcher",
		},
	}

	pwd := newConsoleGitRepo(t, "main")
	fresh, _ := newConsoleFreshness(c, pwd, eval, nil, nil)

	applicable, isFresh, msg := fresh()
	if !applicable || !isFresh {
		t.Fatalf("fresh() = applicable=%v fresh=%v msg=%q, want applicable=true fresh=true (the launcher dimension must never be wired into the Console's Probe call)", applicable, isFresh, msg)
	}
	if strings.Contains(msg, "launcher") {
		t.Errorf("fresh() message %q names the launcher, want the Console's probe to never report a launcher-driven verdict", msg)
	}
	for _, call := range eval.Calls {
		if call.Attr == "packages.x86_64-linux.launcher" {
			t.Errorf("Eval called for the launcher attr %q, want the Console's Probe call to never evaluate the launcher dimension at all", call.Attr)
		}
	}
}

// consoleGitSync must refuse the checkout when pwd is off baseBranch and
// dirty, rather than carry those changes onto baseBranch (issue #769). git
// itself only blocks a checkout that would overwrite a conflicting file, so it
// carries a non-conflicting dirty change across silently.
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

// A dirty tree already on baseBranch must still sync: the refusal keys off
// "off baseBranch AND dirty", not "dirty" alone, or every routine rebuild
// with scratch files present would wrongly refuse.
func TestConsoleGitSync_DirtyOnBaseBranch_StillSyncs(t *testing.T) {
	pwd := newConsoleGitRepo(t, "main")
	gitWriteFile(t, filepath.Join(pwd, "scratch.txt"), "untracked\n")

	if _, _, err := consoleGitSync(pwd, "main"); err != nil {
		t.Fatalf("consoleGitSync() = %v, want nil for a dirty tree already on baseBranch", err)
	}
}

// headRev and freshness.Probe's Result.Rev must return identically formatted
// full SHAs for the same commit. newConsoleFreshnessChecker's res.Rev ==
// builtRev comparison relies on that format, so a --short added to either call
// site would silently break the match.
func TestHeadRevAndProbeRev_SameCommit_IdenticalFormat(t *testing.T) {
	pwd := newConsoleGitRepo(t, "main")

	head, err := headRev(pwd)
	if err != nil {
		t.Fatalf("headRev: %v", err)
	}

	eval := &freshness.Fake{OutPath: "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-agent-image"}
	res := freshness.Probe(freshness.ProbeSpec{
		RunnerKind:     "podman",
		Pwd:            pwd,
		BaseBranch:     "main",
		FlakeImageAttr: ".#packages.x86_64-linux.agent-image",
		ImageTag:       "spindrift:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}, eval)

	if res.Rev != head {
		t.Errorf("Probe Rev = %q, headRev = %q, want identical for the same commit", res.Rev, head)
	}
	if len(head) != 40 && len(head) != 64 {
		t.Errorf("headRev length = %d, want 40 (SHA-1) or 64 (SHA-256), no --short", len(head))
	}
}

// A clean tree off baseBranch gets a notice naming both branches, which closes
// the silent-switch gap of issue #1141: checkCheckoutSafe lets the checkout
// proceed because there is nothing dirty to carry across, but before the notice
// nothing told the operator their pwd had moved off the branch.
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

// Syncing while already on baseBranch returns no notice, since no switch
// happened.
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

// headRev reports the same commit hash git itself reports for pwd's HEAD. This
// pins the seam headRev shares with gitOutput once headRev delegates to it
// (issue #1133).
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

// runGit runs the command it is given and includes git's own stderr in its
// error on failure. This pins the seam runGit shares with gitOutput once runGit
// delegates to it (issue #1133).
func TestRunGit_ExecutesCommandAndSurfacesError(t *testing.T) {
	pwd := newConsoleGitRepo(t, "main")

	if err := runGit(pwd, "checkout", "-b", "feature"); err != nil {
		t.Fatalf("runGit(checkout -b feature) = %v", err)
	}
	branch, err := exec.Command("git", "-C", pwd, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("git rev-parse --abbrev-ref HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(branch)); got != "feature" {
		t.Errorf("branch after runGit(checkout) = %q, want %q", got, "feature")
	}

	err = runGit(pwd, "checkout", "does-not-exist")
	if err == nil {
		t.Fatal("runGit(checkout does-not-exist) = nil, want an error")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("runGit() error = %q, want it to surface git's stderr mentioning %q", err, "does-not-exist")
	}
}

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

// newConsoleGitRepo builds a bare "origin" plus a local clone of it, matching
// the shape the launcher's own pwd has in production: a checkout with an
// "origin" remote.
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
