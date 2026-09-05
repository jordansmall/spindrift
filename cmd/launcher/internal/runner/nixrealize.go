package runner

import (
	"fmt"
	"strings"
	"syscall"
)

// NixRealizer hermetically realizes (builds) a flake attribute's derivation
// at a specific git rev by shelling out to `nix build`. It satisfies
// the freshness.Realizer seam (structurally — this package does not
// import freshness) so the image-freshness background realize's only nix
// invocation stays behind the runner seam.
type NixRealizer struct{}

// Start begins building attr's derivation at rev via `nix build --no-link`
// (avoids writing a `result` symlink into pwd). It blocks only to fork/exec
// the child; the wait function blocks for the build itself. The child
// runs in its own process group, so a Ctrl-C hard-abort orphans it — see
// "Background realize process isolation" in docs/reference.md.
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
