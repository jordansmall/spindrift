// Package freshness answers whether the loaded agent image would be
// rebuilt if dispatch ran against the current base-branch tip — the
// image-freshness boundary ADR 0019 establishes for continuous-pipe
// dispatch (#478). Probe fetches the base ref, hermetically evaluates the
// image attr's drvPath at that fetched rev (a git+file flake eval, never a
// checkout or pull), and compares it against the baked derivation path. It
// never mutates the working copy, the checkout, or the loaded image.
package freshness

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Evaluator hermetically evaluates a flake attribute's derivation path at a
// specific git rev. The real implementation shells out to `nix eval`; tests
// substitute a Fake so no nix round-trip is required.
type Evaluator interface {
	// Eval returns the drvPath of attr in the flake rooted at pwd, evaluated
	// at rev — a fetched commit-ish, never the working tree.
	Eval(pwd, rev, attr string) (drvPath string, err error)
}

// Result is the outcome of a Probe call.
type Result struct {
	// Applicable is false for the bwrap runtime, which keeps its store
	// read-only and has no loaded image to compare.
	Applicable bool
	// Fresh is true when the evaluated drvPath matches the baked one.
	// Meaningless when Applicable is false.
	Fresh bool
	// Message is a human-readable summary safe to print on `preview`.
	Message string
}

// Probe answers whether the loaded OCI image would be rebuilt if dispatch
// ran against the current base-branch tip.
func Probe(runtime, pwd, baseBranch, flakeImageAttr, imageDrv string, eval Evaluator) Result {
	if runtime == "bwrap" {
		return Result{
			Applicable: false,
			Message:    "not applicable (bwrap runtime keeps its store read-only; no loaded image to compare)",
		}
	}

	rev, err := fetchBaseTip(pwd, baseBranch)
	if err != nil {
		return Result{
			Applicable: true,
			Fresh:      false,
			Message:    fmt.Sprintf("could not fetch %s to check image freshness: %v — assuming rebuild needed", baseBranch, err),
		}
	}

	attr := strings.TrimPrefix(flakeImageAttr, ".#")
	drvPath, err := eval.Eval(pwd, rev, attr)
	if err != nil {
		return Result{
			Applicable: true,
			Fresh:      false,
			Message:    fmt.Sprintf("could not evaluate image at %s tip %s: %v — assuming rebuild needed", baseBranch, rev, err),
		}
	}

	if drvPath == imageDrv {
		return Result{
			Applicable: true,
			Fresh:      true,
			Message:    fmt.Sprintf("fresh (%s tip %s matches the loaded image)", baseBranch, rev),
		}
	}
	return Result{
		Applicable: true,
		Fresh:      false,
		Message:    fmt.Sprintf("rebuild needed (%s tip %s changed image inputs)", baseBranch, rev),
	}
}

// fetchBaseTip fetches baseBranch from origin at pwd — no checkout, no pull,
// no working-copy mutation — and returns the fetched commit sha.
func fetchBaseTip(pwd, baseBranch string) (string, error) {
	fetch := exec.Command("git", "-C", pwd, "fetch", "origin", baseBranch)
	var stderr bytes.Buffer
	fetch.Stderr = &stderr
	if err := fetch.Run(); err != nil {
		return "", fmt.Errorf("git fetch origin %s: %w: %s", baseBranch, err, strings.TrimSpace(stderr.String()))
	}
	out, err := exec.Command("git", "-C", pwd, "rev-parse", "FETCH_HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse FETCH_HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
