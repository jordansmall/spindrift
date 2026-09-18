package runner

import (
	"fmt"
	"strings"
	"syscall"
)

// NixRealizer builds a flake attribute's derivation at a given rev with `nix build`.
// It satisfies the freshness.Realizer seam without importing freshness.
type NixRealizer struct{}

// Start begins building attr's derivation at rev; `--no-link` keeps a `result`
// symlink out of pwd. It forks and returns; the returned function waits for the
// build. The child runs in its own process group, so a Ctrl-C hard-abort orphans
// it. See "Background realize process isolation" in docs/reference.md.
func (NixRealizer) Start(pwd, rev, attr string) (func() error, error) {
	// nix build wants the derivation attr itself, not its output path, so there
	// is no ".outPath" suffix here, unlike NixEvaluator.Eval's ref.
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
