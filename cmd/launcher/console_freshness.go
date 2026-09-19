package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/freshness"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/waves"
)

// newConsoleFreshness wires the Console's freshness checker and rebuild action
// onto the freshness.Probe seam (issue #652). c.imageTag is baked into this
// process at nix-wrapper invocation time and cannot be recomputed in-process,
// so a bare Probe call reports the pre-rebuild verdict forever; the checker
// caches the rev rebuild landed on and treats a stale verdict there as fresh.
func newConsoleFreshness(c config, pwd string, eval freshness.Evaluator, pull func() (string, string, error), build func() (string, error)) (waves.FreshnessChecker, func() (string, string, error)) {
	probe := func() freshness.Result {
		// FlakeLauncherAttr and LoadedLauncherHash stay zero: issue #1364 scopes
		// host-launcher freshness to the headless wave path. The Console rebuilds
		// only the loaded artifact (issue #2667), never the host launcher binary,
		// so a launcher-stale verdict here would be one it can never resolve.
		return freshness.Probe(freshness.ProbeSpec{
			RunnerKind:     c.runnerKind,
			Pwd:            pwd,
			BaseBranch:     c.baseBranch,
			FlakeImageAttr: c.flakeImageAttr,
			ImageTag:       c.imageTag,
		}, eval)
	}
	return newConsoleFreshnessChecker(c.baseBranch, probe, pull, build)
}

// newConsoleFreshnessChecker holds the rev-caching logic, with the probe, pull
// and build seams as plain funcs so tests can script freshness.Result values
// instead of running git and nix.
func newConsoleFreshnessChecker(baseBranch string, probe func() freshness.Result, pull func() (string, string, error), build func() (string, error)) (waves.FreshnessChecker, func() (string, string, error)) {
	var mu sync.Mutex
	var builtRev string

	fresh := func() (bool, bool, string) {
		res := probe()
		mu.Lock()
		// res.Rev and builtRev are both un-abbreviated `git rev-parse` output, so
		// this string equality is a safe same-commit check. Adding --short or
		// --abbrev at either call site breaks the match silently.
		rebuiltThisTip := res.Rev != "" && res.Rev == builtRev
		mu.Unlock()
		if res.Applicable && !res.Fresh && rebuiltThisTip {
			return true, true, fmt.Sprintf("fresh (rebuilt at %s tip %s)", baseBranch, res.Rev)
		}
		return res.Applicable, res.Fresh, res.Message
	}

	rebuild := func() (string, string, error) {
		pulledRev, notice, err := pull()
		if err != nil {
			return "", "", err
		}
		output, err := build()
		if err != nil {
			return output, notice, err
		}
		mu.Lock()
		builtRev = pulledRev
		mu.Unlock()
		return output, notice, nil
	}

	return fresh, rebuild
}

// consoleGitSync checks pwd out on baseBranch and fast-forwards it from origin,
// returning the rev it landed on (issue #767) and a notice naming the branch it
// switched off of (issue #1141, empty when none). It refuses the checkout when
// pwd is on another branch with uncommitted changes (issue #769): git blocks a
// checkout only when a file conflicts, so it carries other edits over silently.
func consoleGitSync(pwd, baseBranch string) (string, string, error) {
	branch, err := checkCheckoutSafe(pwd, baseBranch)
	if err != nil {
		return "", "", err
	}
	if err := runGit(pwd, "checkout", baseBranch); err != nil {
		return "", "", err
	}
	if err := runGit(pwd, "pull", "--ff-only"); err != nil {
		return "", "", err
	}
	rev, err := headRev(pwd)
	if err != nil {
		return "", "", err
	}
	var notice string
	if branch != baseBranch {
		notice = fmt.Sprintf("switched off-branch tree from %s to %s", branch, baseBranch)
	}
	return rev, notice, nil
}

// headRev returns the rev pwd is checked out at as a full SHA. It passes no
// --short or --abbrev, so the format matches freshness.fetchBaseTip's, which
// newConsoleFreshnessChecker's res.Rev == builtRev comparison relies on.
func headRev(pwd string) (string, error) {
	return gitOutput(pwd, "rev-parse", "HEAD")
}

// checkCheckoutSafe refuses a checkout when pwd is on a branch other than
// baseBranch and has uncommitted changes.
func checkCheckoutSafe(pwd, baseBranch string) (string, error) {
	branch, err := gitOutput(pwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if branch == baseBranch {
		return branch, nil
	}
	status, err := gitOutput(pwd, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if status != "" {
		return "", fmt.Errorf("refusing to checkout %s: %s has uncommitted changes on %s", baseBranch, pwd, branch)
	}
	return branch, nil
}

// runGit runs `git -C pwd args...`, surfacing git's own stderr on failure.
func runGit(pwd string, args ...string) error {
	_, err := gitOutput(pwd, args...)
	return err
}

// gitOutput runs `git -C pwd args...` and returns its trimmed stdout,
// surfacing git's own stderr on failure.
func gitOutput(pwd string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", pwd}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// consoleNixBuild re-realizes the image from pwd's updated tree via
// runner.RunNixBuild, not this process's own build(), whose IMAGE_DRV and
// IMAGE_TAG are fixed at process start. It captures the output rather than
// streaming it (issue #765): a Bubble Tea alt-screen program owns stdout and
// stderr during a background rebuild, and a direct writer corrupts its renders.
func consoleNixBuild(pwd string) (string, error) {
	return runner.RunNixBuild(pwd)
}
