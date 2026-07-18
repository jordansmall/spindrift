// Package freshness answers whether the loaded agent image would be
// rebuilt if dispatch ran against the current base-branch tip — the
// image-freshness boundary ADR 0019 establishes for continuous-pipe
// dispatch (#478). Probe fetches the base ref, hermetically evaluates the
// image attr's output path at that fetched rev (a git+file flake eval,
// never a checkout or pull), derives the same content-hash tag
// `build`/EnsureReady gates on, and compares it against the loaded image's
// tag. It never mutates the working copy, the checkout, or the loaded
// image.
package freshness

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Evaluator hermetically evaluates a flake attribute's output path at a
// specific git rev. The real implementation shells out to `nix eval`; tests
// substitute a Fake so no nix round-trip is required.
type Evaluator interface {
	// Eval returns the outPath of attr in the flake rooted at pwd, evaluated
	// at rev — a fetched commit-ish, never the working tree.
	Eval(pwd, rev, attr string) (outPath string, err error)
}

// Result is the outcome of a Probe call.
type Result struct {
	// Applicable is false for the bwrap runtime, which keeps its store
	// read-only and has no loaded image to compare.
	Applicable bool
	// Fresh is true when the evaluated image's content-hash tag matches the
	// loaded one. Meaningless when Applicable is false.
	Fresh bool
	// Message is a human-readable summary safe to print on `preview`.
	Message string
	// Rev is the fetched base-tip sha Eval was hermetically evaluated at —
	// "" when Applicable is false or the fetch itself failed. A caller that
	// rebuilds against this same tip (the Console's in-session rebuild,
	// issue #652) can recognize "already rebuilt this tip" against Rev
	// without re-parsing Message.
	Rev string
}

// storeHashPrefixLen and storeHashLen locate the 32-char base32 content
// hash in a nix store path: paths are always
// "/nix/store/<32-char-hash>-<name>", matching mkHarness.nix's own
// imageHash extraction (chars 11-42).
const (
	storeHashPrefixLen = len("/nix/store/")
	storeHashLen       = 32
)

// imageTagFromOutPath derives the "spindrift:<hash>" tag from a nix store
// output path the same way mkHarness.nix's imageHash does — the exact
// currency `build`/EnsureReady gates on (an already-loaded tag skips the
// rebuild), so a fresh verdict here always corresponds to a rebuild build
// would actually perform.
func imageTagFromOutPath(outPath string) (string, error) {
	if !strings.HasPrefix(outPath, "/nix/store/") || len(outPath) < storeHashPrefixLen+storeHashLen {
		return "", fmt.Errorf("not a nix store path: %q", outPath)
	}
	hash := outPath[storeHashPrefixLen : storeHashPrefixLen+storeHashLen]
	return "spindrift:" + hash, nil
}

// Probe answers whether the loaded OCI image would be rebuilt if dispatch
// ran against the current base-branch tip.
func Probe(runtime, pwd, baseBranch, flakeImageAttr, imageTag string, eval Evaluator) Result {
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
	outPath, err := eval.Eval(pwd, rev, attr)
	if err != nil {
		return Result{
			Applicable: true,
			Fresh:      false,
			Message:    fmt.Sprintf("could not evaluate image at %s tip %s: %v — assuming rebuild needed", baseBranch, rev, err),
			Rev:        rev,
		}
	}

	tipTag, err := imageTagFromOutPath(outPath)
	if err != nil {
		return Result{
			Applicable: true,
			Fresh:      false,
			Message:    fmt.Sprintf("could not derive image tag at %s tip %s: %v — assuming rebuild needed", baseBranch, rev, err),
			Rev:        rev,
		}
	}

	if tipTag == imageTag {
		return Result{
			Applicable: true,
			Fresh:      true,
			Message:    fmt.Sprintf("fresh (%s tip %s matches the loaded image %s)", baseBranch, rev, imageTag),
			Rev:        rev,
		}
	}
	return Result{
		Applicable: true,
		Fresh:      false,
		Message:    fmt.Sprintf("rebuild needed (%s tip %s produces %s, loaded image is %s)", baseBranch, rev, tipTag, imageTag),
		Rev:        rev,
	}
}

// fetchBaseTip fetches baseBranch from origin at pwd — no checkout, no pull,
// no working-copy mutation — and returns the fetched commit sha as a full
// 40-char SHA-1 (64 for SHA-256 repos): no --short/--abbrev flag is passed to
// `git rev-parse`, so the format matches the launcher's own headRev, which
// the Console's rebuilt-tip comparison (res.Rev == builtRev) relies on.
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
