package runner

import (
	"bytes"
	"fmt"
	"strings"
)

// nixEvalRef builds the hermetic git+file flake reference `nix eval` reads:
// the flake rooted at pwd, evaluated at rev — a fetched commit-ish, never the
// working tree — with ".drvPath" appended to attr.
func nixEvalRef(pwd, rev, attr string) string {
	return fmt.Sprintf("git+file://%s?rev=%s#%s.drvPath", pwd, rev, attr)
}

// NixEvaluator hermetically evaluates a flake attribute's derivation path at
// a specific git rev by shelling out to `nix eval`. It satisfies the
// freshness.Evaluator seam (structurally — this package does not import
// freshness) so the image-freshness probe's only nix invocation stays behind
// the runner seam, matching every other sandbox exec call.
type NixEvaluator struct{}

// Eval hermetically evaluates attr's drvPath at rev via `nix eval --raw`
// against a git+file flake reference — no checkout, no pull.
func (NixEvaluator) Eval(pwd, rev, attr string) (string, error) {
	ref := nixEvalRef(pwd, rev, attr)
	cmd := execCommand("nix", "eval", "--raw", ref)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("nix eval %s: %w: %s", ref, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}
