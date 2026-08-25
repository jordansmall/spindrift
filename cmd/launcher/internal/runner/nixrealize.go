package runner

import (
	"fmt"
	"strings"
	"syscall"
)

// NixRealizer hermetically realizes (builds) a flake attribute's derivation
// at a specific git rev by shelling out to `nix build`. It satisfies the
// freshness.Realizer seam (structurally — this package does not import
// freshness) so the image-freshness background realize's only nix
// invocation stays behind the runner seam, matching every other sandbox
// exec call.
//
// A realize surviving the launcher's own exit is not this type's doing: it
// falls out of Start blocking only long enough to fork the `nix build`
// child (never waiting for it), leaving the child to run to completion in
// the background — see freshness.Realizer's doc comment for why that split
// is required to avoid racing os.Exit. That survival is what the
// image-freshness boundary ADR 0019 relies on: dispatch can exit at a wave
// boundary while a background realize keeps the single-user nix store's
// lock held for the build's full duration, independent of the launcher
// process's own lifetime.
//
// Setpgid (see Start's doc comment for why it's set once at fork time,
// before it's known what will eventually terminate the launcher) is a
// narrower, separate concern: it isolates the child from signals delivered
// to the launcher's process group for the child's whole lifetime —
// including while the launcher is still running, not only after the
// launcher has exited.
type NixRealizer struct{}

// Start begins building attr's derivation at rev via `nix build --no-link`
// against a git+file flake reference — no checkout, no pull, no
// working-tree mutation. --no-link is required: without it `nix build`
// writes a `result` symlink into pwd's working tree. Start blocks only long
// enough to fork/exec the `nix build` child process (cmd.Start, not
// cmd.Run) — the returned wait function blocks for the build itself to
// finish, wrapping a non-nil error with the flake reference and stderr.
//
// The child is placed in its own process group (Setpgid) rather than
// inheriting the launcher's. Setpgid is set once, at fork time, before it's
// known what will eventually terminate the launcher — so it can't be scoped
// to only the "launcher already exited" case. Without it, any signal
// delivered to the launcher's process group — whether after the launcher
// has already exited (container/session teardown, a shell delivering
// job-control signals to its foreground process group, ...) or at the very
// moment such a signal is what's terminating the launcher — would reach the
// still-running `nix build` child too and kill it mid-build, even though
// the whole point of starting it in the background is for it to keep
// running past the launcher's own exit (ADR 0019 — see the NixRealizer doc
// comment).
//
// Accepted trade-off: dogfood.sh's Ctrl-C hard-abort (SIGINT to the whole
// foreground process group, see its doc comment near the stop trap) is one
// such signal. freshness.RealizeTip fires synchronously from the `fresh`
// closure inside waves.RunContinuous (main.go), so the launcher is usually
// still alive, mid-wave, when Ctrl-C lands — the SIGINT is what terminates
// the launcher, not something arriving after it's already gone. (The
// stale-image path is the exception: main.go can exit on ErrImageStale
// within seconds of a multi-minute realize starting, waves/continuous.go,
// putting a later Ctrl-C in the ordinary post-exit case instead.) Because
// Setpgid already detached the `nix build` child from the foreground group
// at fork time, that same SIGINT does not reach it: the child survives
// orphaned, still holding the single-user nix store lock, rather than
// dying alongside the launcher and the rest of the foreground group. This
// is accepted, not fixed, because the same fork-time Setpgid is what
// protects the ordinary post-exit case this type exists for (ADR 0019),
// and it can't be scoped to spare Ctrl-C specifically for the same
// fork-time reason given above. A mitigation could still act later, at
// signal-delivery time: the launcher could install a SIGINT handler and
// forward it into the child's own group with syscall.Kill(-pgid,
// syscall.SIGINT) before exiting. That's not wired up today — Start's
// signature (func() error, error) doesn't expose the child's pid or pgid,
// so forwarding would need a seam change here first, and no cmd/launcher
// source file installs a SIGINT handler for this path today (its only
// signal.Notify call, in quickstart/maskedinput.go, handles unrelated
// masked input; the Console TUI's bubbletea run loop installs its own
// separate SIGINT handling). So the orphaned-build cost of the Ctrl-C case
// is a deliberate cost, not one that's impossible to avoid.
func (NixRealizer) Start(pwd, rev, attr string) (func() error, error) {
	// No ".outPath" suffix here, unlike NixEvaluator.Eval's ref: nix build
	// wants the derivation attr itself, not its output path.
	ref := hermeticFlakeRef(pwd, rev, attr)
	cmd := execCommand("nix", "build", ref, "--no-link")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stderr := &boundedWriter{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("nix build %s: %w", ref, err)
	}
	return func() error {
		if err := cmd.Wait(); err != nil {
			return fmt.Errorf("nix build %s: %w: %s", ref, err, strings.TrimSpace(stderr.String()))
		}
		return nil
	}, nil
}
