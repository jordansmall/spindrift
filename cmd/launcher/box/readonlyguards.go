package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/readonlyguards"
)

// guardsDeps is the read-only guard install, split out so a test can make it
// miss what the post-install verification then catches.
type guardsDeps struct {
	Install func(rows []promptassembly.ForbiddenMarkerRow, cfg readonlyguards.Config, out io.Writer) (readonlyguards.Result, error)
}

func realGuardsDeps() guardsDeps {
	return guardsDeps{Install: readonlyguards.Install}
}

// readonlyDecoyName is the basename of the bare decoy repo that the guarded
// remotes' pushurls point at.
const readonlyDecoyName = "readonly-push-guard.git"

// installReadonlyGuards installs the read-only git push hook (issues #2463,
// #2465, #2509), whose guard and rejection message come from the
// forbiddenMarkers registry. A self-contained dispatch has no forge to guard,
// and one whose hand-off is a real `git push` must never get that push
// blocked locally, so neither installs the hook.
func (r *boxRun) installReadonlyGuards() error {
	env, d := r.env, r.d
	if env.SelfContained || !r.needsBox {
		return nil
	}
	rows, err := promptassembly.LoadForbiddenMarkersFile(r.in.ForbiddenMarkersFile)
	if err != nil {
		return err
	}
	// A bare decoy outside the work dir, never the work dir itself: every
	// real push targets the branch already checked out there, so a pushurl
	// pointing at it would resolve as "Everything up-to-date" and exit 0
	// without firing a hook. The work dir gets the hook too, catching a push
	// to an explicit URL that bypasses the remotes' pushurls.
	tmp, err := os.MkdirTemp("", "")
	if err != nil {
		return err
	}
	decoy := filepath.Join(tmp, readonlyDecoyName)
	if err := d.Git(r.in.WorkDir, "init", "--bare", "-q", decoy); err != nil {
		return fmt.Errorf("init decoy repo: %w", err)
	}
	remotes, err := r.guardedRemotes()
	if err != nil {
		return err
	}
	for _, name := range remotes {
		if err := d.Git(r.in.WorkDir, "config", "remote."+name+".pushurl", decoy); err != nil {
			return fmt.Errorf("point remote %s's pushurl at the decoy: %w", name, err)
		}
	}
	cfg := readonlyguards.Config{RepoDir: decoy, ExtraRepoDirs: []string{r.in.WorkDir}}
	res, err := d.Guards.Install(rows, cfg, d.Stdout)
	if err != nil {
		return err
	}
	r.say("readonly-guards: hook installed=%v", res.HookInstalled)
	return r.verifyReadonlyGuards(res, cfg, decoy)
}

// verifyReadonlyGuards asserts the guards are in place and in force before the
// Driver can run, rather than trusting the install sequence: an installed hook
// must exist and run in every repo it was written to, with every remote's
// pushurl still pointing at the decoy.
func (r *boxRun) verifyReadonlyGuards(res readonlyguards.Result, cfg readonlyguards.Config, decoy string) error {
	d := r.d
	if !res.HookInstalled {
		return nil
	}
	for _, dir := range append([]string{cfg.RepoDir}, cfg.ExtraRepoDirs...) {
		for _, hook := range readonlyguards.HookPaths(dir) {
			if _, err := os.Stat(hook); err != nil {
				return fmt.Errorf("git hook missing: %w", err)
			}
			if err := runnable("git hook "+hook, dir, hook); err != nil {
				return err
			}
		}
	}
	remotes, err := r.guardedRemotes()
	if err != nil {
		return err
	}
	for _, name := range remotes {
		got, err := d.GitOutput(r.in.WorkDir, "config", "--get", "remote."+name+".pushurl")
		if err != nil {
			return fmt.Errorf("read remote %s's pushurl: %w", name, err)
		}
		if got = strings.TrimSpace(got); got != decoy {
			return fmt.Errorf("remote %s's pushurl is %q, not the decoy %s", name, got, decoy)
		}
	}
	return nil
}

// guardedRemotes is origin plus every remote the work dir lists. Every one
// takes the decoy pushurl: the mirror clone's remote.pushDefault names a
// remote other than origin, and a push naming none goes there.
func (r *boxRun) guardedRemotes() ([]string, error) {
	d := r.d
	out, err := d.GitOutput(r.in.WorkDir, "remote")
	if err != nil {
		return nil, fmt.Errorf("list remotes: %w", err)
	}
	remotes := []string{"origin"}
	for _, name := range strings.Fields(out) {
		if name != "origin" {
			remotes = append(remotes, name)
		}
	}
	return remotes, nil
}

// probeTimeout bounds one runnable probe; a var so a test can shrink it.
var probeTimeout = 10 * time.Second

// runnable starts path in dir (the caller's cwd when empty) and reports an
// error when it cannot be exec'd, hangs, or dies on a signal. Exit 126 and 127
// are the shell's cannot-execute and not-found codes (a hook whose interpreter
// starts but whose real target is missing), so they count as not runnable. Any
// other non-zero exit still counts as running: the push-guard hook exits 1 by
// design. Output is discarded and stdin is /dev/null so the hook reads no ref
// data.
func runnable(label, dir, path string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("%s timed out after %s (interpreter %s)", label, probeTimeout, shebangInterpreter(path))
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.Exited() {
		switch code := exit.ExitCode(); code {
		case 126, 127:
			return fmt.Errorf("%s started under interpreter %s, but a command it runs is missing or not executable (exit %d)", label, shebangInterpreter(path), code)
		default:
			return nil
		}
	}
	return fmt.Errorf("%s cannot run (interpreter %s): %w", label, shebangInterpreter(path), err)
}

// shebangInterpreter names the interpreter on path's #! line, looking through
// `env` (and its flags) to the program it launches.
func shebangInterpreter(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	defer f.Close()
	line, _ := bufio.NewReader(f).ReadString('\n')
	rest, ok := strings.CutPrefix(line, "#!")
	if !ok {
		return "none (no #! line)"
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return "none (empty #! line)"
	}
	if filepath.Base(fields[0]) == "env" {
		for _, field := range fields[1:] {
			if !strings.HasPrefix(field, "-") {
				return field
			}
		}
	}
	return fields[0]
}
