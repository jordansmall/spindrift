// Package freshness answers whether the loaded agent image (OCI) or bundled
// agent closure (bwrap) would be rebuilt if dispatch ran against the current
// base-branch tip (ADR 0019, #478). Probe evaluates the image attr at the
// fetched base rev, never a checkout or pull, and compares the identity
// build/EnsureReady gates on. It mutates nothing.
package freshness

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Evaluator hermetically evaluates a flake attribute's output path at a git rev.
type Evaluator interface {
	// Eval returns the outPath of attr in the flake rooted at pwd, evaluated at
	// rev, a fetched commit-ish and never the working tree.
	Eval(pwd, rev, attr string) (outPath string, err error)
}

// Result is the outcome of a Probe call.
type Result struct {
	// Applicable is false when the probe cannot be checked at all, for any
	// runnerKind: pwd isn't a git repository, the base branch or origin remote
	// is missing, or the flake doesn't provide the image attr.
	Applicable bool
	// Fresh is true when the evaluated identity matches the loaded one, a
	// content-hash tag for OCI or a raw nix store path for bwrap (#2667).
	// Meaningless when Applicable is false.
	Fresh bool
	// Message is a human-readable summary safe to print on `preview`.
	Message string
	// Rev is the fetched base-tip sha Eval was evaluated at, empty when
	// Applicable is false or the fetch failed. A caller that rebuilds against
	// this same tip (#652) can recognize "already rebuilt this tip" from Rev
	// without re-parsing Message.
	Rev string
	// TipTag is the identity a rebuild would load: the "<repo>:<hash>" tag for
	// an OCI runnerKind, the raw nix store outPath for bwrap. Empty when the
	// probe never derived it, when the image dimension is fresh (no divergence
	// to name), or on a launcher eval/hash-derive failure. The non-convergence
	// diagnostic (#2113) names it alongside the loaded value.
	TipTag string
	// LauncherFresh is true when no host-launcher rebuild would be needed. It
	// is false (fail-closed) on every branch that returns before the launcher
	// comparison is known, and true when flakeLauncherAttr is empty, because
	// "not configured" is not "stale". Fresh requires both this and the image
	// comparison.
	LauncherFresh bool
	// ImageFresh is true only when the image dimension matched, independent of
	// the launcher dimension and of the overall Fresh verdict. False
	// (fail-closed) on every branch that returns before the image comparison
	// is known; once known it carries through the rest of Probe, including a
	// launcher eval/hash-derive failure.
	ImageFresh bool
	// TipLauncherHash is the bare 32-char store hash flakeLauncherAttr would
	// produce at the base tip. Empty when flakeLauncherAttr is empty or the
	// probe never got far enough to derive it. Mirrors TipTag for the launcher
	// dimension (#2682).
	TipLauncherHash string
}

// A nix store path is always "/nix/store/<32-char-hash>-<name>"; these offsets
// match mkHarness.nix's own imageHash extraction (chars 11-42).
const (
	storeHashPrefixLen = len("/nix/store/")
	storeHashLen       = 32
)

// imageTagFromOutPath derives the "<repo>:<hash>" tag the same way
// mkHarness.nix's imageHash does, so a fresh verdict matches the tag
// build/EnsureReady gates on. repo is the loaded image's own repo, so a
// driver-scoped image (e.g. "spindrift-opencode") compares against the repo it
// was loaded under rather than a hardcoded "spindrift".
func imageTagFromOutPath(outPath, repo string) (string, error) {
	hash, err := storeHash(outPath)
	if err != nil {
		return "", err
	}
	return repo + ":" + hash, nil
}

// storeHash extracts the bare 32-char content hash, without the "<repo>:"
// prefix imageTagFromOutPath adds. The launcher dimension (#1364) compares this
// bare hash: a host-launcher binary has no repo concept.
func storeHash(outPath string) (string, error) {
	if !strings.HasPrefix(outPath, "/nix/store/") || len(outPath) < storeHashPrefixLen+storeHashLen {
		return "", fmt.Errorf("not a nix store path: %q", outPath)
	}
	return outPath[storeHashPrefixLen : storeHashPrefixLen+storeHashLen], nil
}

// imageRepo returns everything before the LAST colon of an "<repo>:<tag>"
// reference, since a repo can itself contain a colon (a registry host:port
// prefix). Falls back to "spindrift" when imageTag has no colon at all.
func imageRepo(imageTag string) string {
	i := strings.LastIndex(imageTag, ":")
	if i < 0 {
		return "spindrift"
	}
	return imageTag[:i]
}

// trimFlakeAttrPrefix strips the ".#" flake-CLI shorthand so Probe's Eval call
// and RealizeTip's Start call address the same attribute string whichever form
// flakeImageAttr was configured with.
func trimFlakeAttrPrefix(attr string) string {
	return strings.TrimPrefix(attr, ".#")
}

// KindBwrap is the RUNNER_KIND value selecting the bwrap runner. bwrap has no
// "repo:tag" registry concept, so Probe compares the evaluated outPath directly
// against the loaded imageTag, itself a bare store path. Any other runnerKind,
// including an OCI runtime name, is treated as an OCI kind.
const KindBwrap = "bwrap"

// ProbeSpec is the set of params Probe needs to answer a freshness check.
type ProbeSpec struct {
	// RunnerKind is the RUNNER_KIND document artifact (#2538 AC1): KindBwrap
	// selects the bwrap comparison path, any other value an OCI kind.
	RunnerKind, Pwd, BaseBranch string
	// ImageTag is the loaded image's tag, an OCI "repo:tag" string or, for
	// KindBwrap, a bare nix store path.
	FlakeImageAttr, ImageTag string
	// FlakeLauncherAttr and LoadedLauncherHash drive the optional
	// host-launcher freshness dimension (#1364); a non-empty FlakeLauncherAttr
	// turns it on.
	FlakeLauncherAttr, LoadedLauncherHash string
}

// Probe answers whether the loaded image (OCI or bwrap agent closure) would be
// rebuilt if dispatch ran against the current base-branch tip. A caller must
// pass config.runnerKind, not the raw RUNTIME value: a bwrap-kind harness can
// carry an OCI runtime name, which a runtime-name comparison misclassifies as
// OCI. The launcher dimension (#1364) evaluates at the same fetched rev.
func Probe(spec ProbeSpec, eval Evaluator) Result {
	rev, err := fetchBaseTip(spec.Pwd, spec.BaseBranch)
	if err != nil {
		if isNotAGitRepository(err) {
			return Result{
				Applicable: false,
				Message:    fmt.Sprintf("not applicable (%s is not a git repository; freshness cannot be checked or rebuilt here)", spec.Pwd),
			}
		}
		if isRemoteRefMissing(err) {
			return Result{
				Applicable: false,
				Message:    fmt.Sprintf("not applicable (%s has no %s branch on origin; freshness cannot be checked here)", spec.Pwd, spec.BaseBranch),
			}
		}
		if isNoOriginRemote(err) {
			return Result{
				Applicable: false,
				Message:    fmt.Sprintf("not applicable (%s has no reachable origin remote; freshness cannot be checked here)", spec.Pwd),
			}
		}
		return Result{
			Applicable: true,
			Fresh:      false,
			Message:    fmt.Sprintf("could not fetch %s to check image freshness: %v — assuming rebuild needed", spec.BaseBranch, err),
		}
	}

	attr := trimFlakeAttrPrefix(spec.FlakeImageAttr)
	outPath, err := eval.Eval(spec.Pwd, rev, attr)
	if err != nil {
		if isImageAttrMissing(err) {
			return Result{
				Applicable: false,
				Message:    fmt.Sprintf("not applicable (%s does not provide %s; not the spindrift image-source flake, so freshness cannot be checked here)", spec.Pwd, attr),
			}
		}
		return Result{
			Applicable: true,
			Fresh:      false,
			Message:    fmt.Sprintf("could not evaluate image at %s tip %s: %v — assuming rebuild needed", spec.BaseBranch, rev, err),
			Rev:        rev,
		}
	}

	var tipTag string
	var imageFresh bool
	if spec.RunnerKind == KindBwrap {
		tipTag = outPath
		imageFresh = outPath == spec.ImageTag
	} else {
		tipTag, err = imageTagFromOutPath(outPath, imageRepo(spec.ImageTag))
		if err != nil {
			return Result{
				Applicable: true,
				Fresh:      false,
				Message:    fmt.Sprintf("could not derive image tag at %s tip %s: %v — assuming rebuild needed", spec.BaseBranch, rev, err),
				Rev:        rev,
			}
		}
		imageFresh = tipTag == spec.ImageTag
	}

	launcherConfigured := spec.FlakeLauncherAttr != ""
	launcherFresh := true
	var tipLauncherHash string
	if launcherConfigured {
		launcherAttr := trimFlakeAttrPrefix(spec.FlakeLauncherAttr)
		launcherOutPath, err := eval.Eval(spec.Pwd, rev, launcherAttr)
		if err != nil {
			return Result{
				Applicable: true,
				Fresh:      false,
				ImageFresh: imageFresh,
				Message:    fmt.Sprintf("could not evaluate launcher at %s tip %s: %v — assuming rebuild needed", spec.BaseBranch, rev, err),
				Rev:        rev,
			}
		}
		tipLauncherHash, err = storeHash(launcherOutPath)
		if err != nil {
			return Result{
				Applicable: true,
				Fresh:      false,
				ImageFresh: imageFresh,
				Message:    fmt.Sprintf("could not derive launcher hash at %s tip %s: %v — assuming rebuild needed", spec.BaseBranch, rev, err),
				Rev:        rev,
			}
		}
		launcherFresh = tipLauncherHash == spec.LoadedLauncherHash
	}

	resultTipTag := tipTag
	if imageFresh {
		resultTipTag = ""
	}

	return Result{
		Applicable:      true,
		Fresh:           imageFresh && launcherFresh,
		LauncherFresh:   launcherFresh,
		ImageFresh:      imageFresh,
		Message:         freshnessMessage(spec.RunnerKind, spec.BaseBranch, rev, launcherConfigured, imageFresh, launcherFresh, tipTag, spec.ImageTag, tipLauncherHash, spec.LoadedLauncherHash),
		Rev:             rev,
		TipTag:          resultTipTag,
		TipLauncherHash: tipLauncherHash,
	}
}

// freshnessMessage names whichever dimensions drove a rebuild-needed verdict.
// With launcherConfigured false it keeps the original image-only wording.
// runnerKind picks the noun for the loaded value: "image" for OCI, "closure"
// for bwrap (#2667), where that slot holds a raw nix store path.
func freshnessMessage(runnerKind, baseBranch, rev string, launcherConfigured, imageFresh, launcherFresh bool, tipTag, imageTag, tipLauncherHash, loadedLauncherHash string) string {
	loaded := "image"
	if runnerKind == KindBwrap {
		loaded = "closure"
	}

	if !launcherConfigured {
		if imageFresh {
			return fmt.Sprintf("fresh (%s tip %s matches the loaded %s %s)", baseBranch, rev, loaded, imageTag)
		}
		return fmt.Sprintf("rebuild needed (%s tip %s produces %s, loaded %s is %s)", baseBranch, rev, tipTag, loaded, imageTag)
	}

	if imageFresh && launcherFresh {
		return fmt.Sprintf("fresh (image and launcher both match %s tip %s, loaded %s is %s)", baseBranch, rev, loaded, imageTag)
	}

	imageClause := fmt.Sprintf("image: %s tip %s produces %s, loaded %s is %s", baseBranch, rev, tipTag, loaded, imageTag)
	launcherClause := fmt.Sprintf("launcher: %s tip %s produces %s, loaded launcher is %s", baseBranch, rev, tipLauncherHash, loadedLauncherHash)

	switch {
	case !imageFresh && launcherFresh:
		return fmt.Sprintf("rebuild needed (%s)", imageClause)
	case imageFresh && !launcherFresh:
		return fmt.Sprintf("rebuild needed (%s)", launcherClause)
	default:
		return fmt.Sprintf("rebuild needed (%s; %s)", imageClause, launcherClause)
	}
}

// fetchBaseTip fetches baseBranch from origin at pwd with no checkout, pull, or
// working-copy mutation. It returns the full-length sha: no --short/--abbrev is
// passed, so the format matches the launcher's own headRev, which the Console's
// res.Rev == builtRev comparison relies on.
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

// isNotAGitRepository distinguishes git's "not a git repository" diagnostic,
// meaning pwd is in no worktree at all, from a transient failure inside a real
// repository. Git uses that same wording whether it stops at the filesystem
// root or at a mount boundary, so a substring match covers both.
func isNotAGitRepository(err error) bool {
	return strings.Contains(err.Error(), "not a git repository")
}

// isRemoteRefMissing reports git's "couldn't find remote ref" diagnostic:
// origin has no baseBranch. That is definitive, not transient, so the caller
// should proceed rather than treat it as rebuild-needed (#1753).
func isRemoteRefMissing(err error) bool {
	return strings.Contains(err.Error(), "couldn't find remote ref")
}

// isImageAttrMissing reports nix's "does not provide attribute" diagnostic: the
// flake at pwd is not the image-source flake, rather than an attr that exists
// failing to evaluate. That is definitive, not transient, so the caller should
// proceed rather than treat it as rebuild-needed (#1754).
func isImageAttrMissing(err error) bool {
	return strings.Contains(err.Error(), "does not provide attribute")
}

// isNoOriginRemote reports git's "does not appear to be a git repository"
// diagnostic: no origin remote is configured, or it points somewhere
// unreachable. A fully local repo (CODE_FORGE=local) has nothing to fetch, so
// the caller should proceed rather than treat it as rebuild-needed (#2034).
func isNoOriginRemote(err error) bool {
	return strings.Contains(err.Error(), "does not appear to be a git repository")
}
