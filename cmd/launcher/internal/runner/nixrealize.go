package runner

import (
	"fmt"
	"strings"
	"syscall"
)

// NixRealizer hermetically realizes (builds) a flake attribute's derivation at
// a specific git rev by shelling out to `nix build`. It satisfies the
// freshness.Realizer seam structurally — this package does not import
// freshness — so the image-freshness realize's only nix invocation stays
// behind the runner seam, like every other sandbox exec call.
//
// A realize surviving the launcher's own exit falls out of Start forking the
// child and never waiting for it. ADR 0019's image-freshness boundary relies
// on that survival: dispatch can exit at a wave boundary while a background
// realize keeps the single-user nix store's lock held for the build's full
// duration.
type NixRealizer struct{}

// Start begins building attr's derivation at rev via `nix build --no-link`
// against a git+file flake reference — no checkout, pull, or working-tree
// mutation. --no-link is required: without it `nix build` writes a `result`
// symlink into pwd's working tree. Start blocks only long enough to fork/exec
// the child; the returned wait function blocks for the build itself, wrapping
// a non-nil error with the flake reference and stderr.
//
// The child gets its own process group. Setpgid is set once at fork time,
// before it is known what will eventually terminate the launcher, so it cannot
// be scoped to only the "launcher already exited" case. Without it, any signal
// delivered to the launcher's process group would reach the still-running `nix
// build` child and kill it mid-build — defeating the whole point of starting
// it in the background (ADR 0019).
//
// Accepted trade-off: dogfood.sh's Ctrl-C hard-abort sends SIGINT to the whole
// foreground group while the launcher is usually still alive mid-wave. Setpgid
// having already detached the child means it survives orphaned, still holding
// the nix store lock, rather than dying with the group. Not fixed, because the
// same fork-time Setpgid is what protects the ordinary post-exit case. A
// launcher-side SIGINT handler forwarding into the child's group would be the
// mitigation, but Start's signature exposes neither pid nor pgid, so that needs
// a seam change first.
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
