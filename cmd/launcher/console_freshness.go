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

// newConsoleFreshness builds the Console's freshness checker and rebuild
// action around the same freshness.Probe seam runContinuousDispatch already
// uses for the headless exit-4 path (issue #652). c.imageTag — the loaded
// image's tag — is baked into this process at nix-wrapper invocation time
// and can never be recomputed in-process, so a bare Probe call would keep
// reporting the pre-rebuild verdict forever even after rebuild has
// genuinely re-baked the image. The checker works around that by
// remembering the rev rebuild last rebuilt against (via Result.Rev) and
// treating a stale verdict at that exact rev as fresh — a real rebuild is
// still required whenever the base branch advances past it. pull and build
// are injected so tests can substitute fakes instead of shelling out to
// git/nix; production wiring is consoleGitSync and consoleNixBuild. pull
// returns the rev it landed on (issue #767) so rebuild can cache it as
// builtRev directly, rather than re-deriving it from a post-build probe.
// build returns its captured nix output (issue #765) alongside its error, so
// a background rebuild never writes directly to the Console's own
// stdout/stderr.
func newConsoleFreshness(c config, pwd string, eval freshness.Evaluator, pull func() (string, error), build func() (string, error)) (waves.FreshnessChecker, func() (string, error)) {
	probe := func() freshness.Result {
		return freshness.Probe(c.runtime, pwd, c.baseBranch, c.flakeImageAttr, c.imageTag, eval)
	}
	return newConsoleFreshnessChecker(c.baseBranch, probe, pull, build)
}

// newConsoleFreshnessChecker holds the rev-caching logic itself, with the
// probe seam factored out as a plain func so it can be unit-tested with
// scripted freshness.Result values instead of a real git/nix round-trip —
// freshness.Probe's own git plumbing is exercised by internal/freshness's
// own tests. See newConsoleFreshness for the production wiring.
func newConsoleFreshnessChecker(baseBranch string, probe func() freshness.Result, pull func() (string, error), build func() (string, error)) (waves.FreshnessChecker, func() (string, error)) {
	var mu sync.Mutex
	var builtRev string

	fresh := func() (bool, bool, string) {
		res := probe()
		mu.Lock()
		// res.Rev (from freshness.fetchBaseTip) and builtRev (from headRev via
		// pull) are both plain, un-abbreviated `git rev-parse` output — full
		// 40-char SHAs — so this string equality is a safe same-commit check.
		// Adding --short/--abbrev to either call site would silently break
		// this match.
		rebuiltThisTip := res.Rev != "" && res.Rev == builtRev
		mu.Unlock()
		if res.Applicable && !res.Fresh && rebuiltThisTip {
			return true, true, fmt.Sprintf("fresh (rebuilt at %s tip %s)", baseBranch, res.Rev)
		}
		return res.Applicable, res.Fresh, res.Message
	}

	rebuild := func() (string, error) {
		pulledRev, err := pull()
		if err != nil {
			return "", err
		}
		output, err := build()
		if err != nil {
			return output, err
		}
		mu.Lock()
		builtRev = pulledRev
		mu.Unlock()
		return output, nil
	}

	return fresh, rebuild
}

// consoleGitSync resets pwd to baseBranch and fast-forwards it from origin
// — the same two-step pull dogfood.sh performs before every rebuild, since
// `nix run .# -- build` reads from $PWD, not a fetched ref. It refuses the
// checkout outright when pwd is on some other branch with uncommitted
// changes (issue #769): git's own conflict check only blocks a checkout
// that would overwrite a *conflicting* file, so a non-conflicting dirty
// change would otherwise ride along onto baseBranch in total silence —
// already on baseBranch, or a clean tree on any branch, are both safe
// because there's nothing for the checkout to carry across silently. It
// returns the rev pwd landed on so the caller can record exactly what
// build() is about to build, rather than re-deriving it from a probe that
// may see origin advance mid-build (issue #767).
func consoleGitSync(pwd, baseBranch string) (string, error) {
	if err := checkCheckoutSafe(pwd, baseBranch); err != nil {
		return "", err
	}
	if err := runGit(pwd, "checkout", baseBranch); err != nil {
		return "", err
	}
	if err := runGit(pwd, "pull", "--ff-only"); err != nil {
		return "", err
	}
	return headRev(pwd)
}

// headRev returns the rev pwd's working tree is currently checked out at, as
// a full 40-char SHA-1 (64 for SHA-256 repos) — no --short/--abbrev flag is
// passed to `git rev-parse`, so the format matches freshness.fetchBaseTip's,
// which the res.Rev == builtRev comparison in newConsoleFreshnessChecker
// relies on.
func headRev(pwd string) (string, error) {
	cmd := exec.Command("git", "-C", pwd, "rev-parse", "HEAD")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// checkCheckoutSafe refuses a checkout when pwd is on a branch other than
// baseBranch and has uncommitted changes — see consoleGitSync.
func checkCheckoutSafe(pwd, baseBranch string) error {
	branch, err := gitOutput(pwd, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return err
	}
	if branch == baseBranch {
		return nil
	}
	status, err := gitOutput(pwd, "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("refusing to checkout %s: %s has uncommitted changes on %s", baseBranch, pwd, branch)
	}
	return nil
}

// runGit runs `git -C pwd args...`, surfacing git's own stderr on failure.
func runGit(pwd string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", pwd}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
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

// consoleNixBuild re-realizes the image from pwd's now-updated tree via
// runner.RunNixBuild — not a call into this process's own build(), whose
// IMAGE_DRV/IMAGE_TAG are fixed at process start and would not pick up
// anything consoleGitSync just pulled. Output is captured and returned
// rather than streamed to stdout/stderr (issue #765): a live Bubble Tea
// alt-screen program owns those fds while a background rebuild runs, and a
// direct writer would corrupt its renders. The captured text is retrievable
// through Launcher.StaleStatus once the rebuild completes.
func consoleNixBuild(pwd string) (string, error) {
	return runner.RunNixBuild(pwd)
}
