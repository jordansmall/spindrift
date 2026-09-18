package runner

import (
	"fmt"
	"strings"
)

// RunNixBuild re-realizes the sandbox image with a fresh `nix run .# -- build` in
// pwd, which the Console's in-session rebuild needs (issue #652): EnsureReady bakes
// IMAGE_DRV/IMAGE_TAG in at nix wrapper invocation time, so only a new invocation
// re-evaluates the flake from pwd's current tree. It returns output rather than
// streaming it: a live Bubble Tea alt-screen program owns those fds (issue #765).
func RunNixBuild(pwd string) (string, error) {
	cmd := execCommand("nix", "run", ".#", "--", "build")
	cmd.Dir = pwd
	var output boundedWriter
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	captured := output.String()
	if err != nil {
		return captured, fmt.Errorf("nix run .# -- build: %w: %s", err, strings.TrimSpace(captured))
	}
	return captured, nil
}
