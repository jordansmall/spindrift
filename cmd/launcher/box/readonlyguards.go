package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/readonlyguards"
)

// guardsDeps are the read-only guard steps, split out so a test can make the
// install miss what the post-install verification then catches.
type guardsDeps struct {
	Install func(rows []promptassembly.ForbiddenMarkerRow, cfg readonlyguards.Config, out io.Writer) (readonlyguards.Result, error)
	// Setenv prepends the shim dir to box's own PATH, which every child (the
	// prefetch hook, the orchestrator, the Driver) inherits.
	Setenv func(key, value string) error
}

func realGuardsDeps() guardsDeps {
	return guardsDeps{Install: readonlyguards.Install, Setenv: os.Setenv}
}

// readonlyShimDir is where box installs the read-only command shims. $HOME
// rather than the work dir's parent, which in production is the root-owned `/`
// while the Box runs as uid 1000.
func readonlyShimDir(home string) string {
	return filepath.Join(home, ".spindrift", "readonly-gh-shim")
}

// installReadonlyGuards installs the read-only guards (issues #2463, #2465,
// #2509): a git push hook plus command shims for gh and fj, every guard and
// rejection message coming from the forbiddenMarkers registry. A read-write
// Box needs none, and a self-contained dispatch has no forge to guard. An argv0
// absent from PATH is skipped by Install, so a github Box with no fj is fine.
func (r *boxRun) installReadonlyGuards() error {
	env, d := r.env, r.d
	if env.BoxWriteEnabled || env.SelfContained {
		return nil
	}
	rows, err := promptassembly.LoadForbiddenMarkersFile(r.in.ForbiddenMarkersFile)
	if err != nil {
		return err
	}
	shimDir := readonlyShimDir(d.Getenv("HOME"))
	cfg := readonlyguards.Config{ShimDir: shimDir}
	var decoy string
	// Only the git-hook guard is gated on outbox capability: a read-only Box
	// whose hand-off IS a real `git push` must never get that push blocked
	// locally. No backend registered today leaves both unset (issue #2927), but
	// the branch stays live for a future one. The shims install regardless.
	if env.HostMediatedRemote || env.OutboxRelayCapable {
		// A bare decoy outside the work dir, never the work dir itself: every
		// real push targets the branch already checked out there, so a pushurl
		// pointing at it would resolve as "Everything up-to-date" and exit 0
		// without firing a hook. The work dir gets the hook too, catching a push
		// to an explicit URL that bypasses origin's pushurl.
		tmp, err := os.MkdirTemp("", "")
		if err != nil {
			return err
		}
		decoy = filepath.Join(tmp, "readonly-push-guard.git")
		if err := d.Git(r.in.WorkDir, "init", "--bare", "-q", decoy); err != nil {
			return fmt.Errorf("init decoy repo: %w", err)
		}
		if err := d.Git(r.in.WorkDir, "config", "remote.origin.pushurl", decoy); err != nil {
			return fmt.Errorf("point origin's pushurl at the decoy: %w", err)
		}
		cfg.RepoDir = decoy
		cfg.ExtraRepoDirs = []string{r.in.WorkDir}
	} else {
		cfg.SkipGitHook = true
	}
	res, err := d.Guards.Install(rows, cfg, d.Stdout)
	if err != nil {
		return err
	}
	r.say("readonly-guards: installed %d command-shim(s) (%v), hook installed=%v", len(res.Shims), res.Shims, res.HookInstalled)

	path := shimDir
	if cur := d.Getenv("PATH"); cur != "" {
		path += string(os.PathListSeparator) + cur
	}
	if err := d.Guards.Setenv("PATH", path); err != nil {
		return err
	}
	return r.verifyReadonlyGuards(res, shimDir, cfg, decoy)
}

// verifyReadonlyGuards asserts the guards are in place and in force before the
// Driver can run, rather than trusting the install sequence: each installed
// shim must be what its argv0 now resolves to on PATH, and an installed hook
// must exist and be executable in every repo it was written to, with origin's
// pushurl still pointing at the decoy. LookPath resolves against the process
// PATH the preceding Setenv updated.
func (r *boxRun) verifyReadonlyGuards(res readonlyguards.Result, shimDir string, cfg readonlyguards.Config, decoy string) error {
	d := r.d
	for _, argv0 := range res.Shims {
		got, err := d.LookPath(argv0)
		if err != nil {
			return fmt.Errorf("shim %s does not resolve on PATH: %w", argv0, err)
		}
		if want := filepath.Join(shimDir, argv0); got != want {
			return fmt.Errorf("%s resolves to %s, not the guard shim %s", argv0, got, want)
		}
	}
	if !res.HookInstalled {
		return nil
	}
	for _, dir := range append([]string{cfg.RepoDir}, cfg.ExtraRepoDirs...) {
		for _, hook := range readonlyguards.HookPaths(dir) {
			info, err := os.Stat(hook)
			if err != nil {
				return fmt.Errorf("git hook missing: %w", err)
			}
			if info.Mode()&0o111 == 0 {
				return fmt.Errorf("git hook %s is not executable", hook)
			}
		}
	}
	if decoy == "" {
		return nil
	}
	got, err := d.GitOutput(r.in.WorkDir, "config", "--get", "remote.origin.pushurl")
	if err != nil {
		return fmt.Errorf("read origin's pushurl: %w", err)
	}
	if got = strings.TrimSpace(got); got != decoy {
		return fmt.Errorf("origin's pushurl is %q, not the decoy %s", got, decoy)
	}
	return nil
}
