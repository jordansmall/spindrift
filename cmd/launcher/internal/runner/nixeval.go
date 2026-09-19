package runner

import (
	"bytes"
	"fmt"
	"strings"
)

// hermeticFlakeRef builds the flake reference that nix eval and nix build both
// read. It pins the flake to a fetched commit-ish, never the working tree.
func hermeticFlakeRef(pwd, rev, attr string) string {
	return fmt.Sprintf("git+file://%s?rev=%s#%s", pwd, rev, attr)
}

// NixEvaluator evaluates a flake attribute's output path at a git rev by
// shelling out to nix eval. It satisfies freshness.Evaluator structurally,
// since this package does not import freshness.
type NixEvaluator struct{}

// Eval evaluates attr's outPath at rev with nix eval --raw, with no checkout
// and no pull.
func (NixEvaluator) Eval(pwd, rev, attr string) (string, error) {
	// nix eval needs the ".outPath" suffix to resolve a derivation's output
	// path; nix build wants the bare derivation attr instead.
	ref := hermeticFlakeRef(pwd, rev, attr) + ".outPath"
	cmd := execCommand("nix", "eval", "--raw", ref)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("nix eval %s: %w: %s", ref, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
