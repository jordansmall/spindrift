package runner

import (
	"bytes"
	"fmt"
	"io"
	"os"
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
// driving loop (dogfood.sh, CI) re-invoke fresh. Output streams to
// stdout/stderr so the operator sees the same build progress dogfood.sh
// prints.
func RunNixBuild(pwd string) error {
	cmd := execCommand("nix", "run", ".#", "--", "build")
	cmd.Dir = pwd
	cmd.Stdout = os.Stdout
	var stderr bytes.Buffer
	cmd.Stderr = io.MultiWriter(os.Stderr, &stderr)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nix run .# -- build: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
