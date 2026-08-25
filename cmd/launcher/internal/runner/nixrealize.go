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
// Start's child process is detached into its own process group (see
// Start's doc comment) specifically so a realize survives the launcher's
// own exit: the image-freshness boundary ADR 0019 establishes lets
// dispatch exit at the wave boundary while a background realize is still
// running, and a single-user nix store's lock is held by the `nix build`
// child for the build's full duration regardless of the launcher process's
// own lifetime.
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
// inheriting the launcher's. Without that, a signal delivered to the
// launcher's process group after the launcher itself has already exited
// (container/session teardown, a shell delivering job-control signals to
// its foreground process group, ...) would reach the still-running `nix
// build` child too and kill it mid-build, even though the whole point of
// starting it in the background is for it to keep running past the
// launcher's own exit (ADR 0019 — see the NixRealizer doc comment).
// Detaching into its own group is what lets it survive that.
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
