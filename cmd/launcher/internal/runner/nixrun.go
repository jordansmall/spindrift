package runner

import (
	"fmt"
	"strings"
)

// RunNixBuild re-realizes the sandbox image via a fresh `nix run .# --
// build` invocation in pwd — the same command dogfood.sh runs right after
// pulling a merged change. It exists as a distinct seam from EnsureReady:
// EnsureReady's IMAGE_DRV/IMAGE_TAG are baked into the process at nix
// wrapper invocation time and never change afterward, so it cannot pick up
// a tree a caller just pulled from within the same process — only a brand
// new nix invocation re-evaluates the flake from pwd's current tree. This
// is what the Console's in-session rebuild (issue #652) needs; headless
// dispatch never calls it; that path just exits 4 and lets the outer
// driving loop (dogfood.sh, CI) re-invoke fresh. Output is captured and
// returned rather than streamed to the real stdout/stderr (issue #765): the
// Console's background rebuild runs concurrently with a live Bubble Tea
// alt-screen program that owns those fds, and a direct writer would
// interleave with (and corrupt) its renders.
func RunNixBuild(pwd string) (string, error) {
	cmd := execCommand("nix", "run", ".#", "--", "build")
	cmd.Dir = pwd
	var output boundedWriter
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return output.String(), fmt.Errorf("nix run .# -- build: %w: %s", err, strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}
