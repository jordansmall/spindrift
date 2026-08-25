package runner

import (
	"bytes"
	"fmt"
	"strings"
)

// hermeticFlakeRef builds the git+file flake reference both nix eval and
// nix build read: the flake rooted at pwd, at rev -- a fetched commit-ish,
// never the working tree.
func hermeticFlakeRef(pwd, rev, attr string) string {
	return fmt.Sprintf("git+file://%s?rev=%s#%s", pwd, rev, attr)
}

// NixEvaluator hermetically evaluates a flake attribute's output path at a
// specific git rev by shelling out to `nix eval`. It satisfies the
// freshness.Evaluator seam (structurally — this package does not import
// freshness) so the image-freshness probe's only nix invocation stays behind
// the runner seam, matching every other sandbox exec call.
type NixEvaluator struct{}

// Eval hermetically evaluates attr's outPath at rev via `nix eval --raw`
// against a git+file flake reference — no checkout, no pull.
func (NixEvaluator) Eval(pwd, rev, attr string) (string, error) {
	// ".outPath" is the suffix nix eval needs to resolve a derivation's
	// output path (nix build, by contrast, wants the derivation attr
	// itself -- see NixRealizer.Start).
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
