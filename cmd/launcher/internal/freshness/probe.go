// Package freshness answers whether the loaded agent image (OCI) or bundled
// agent closure (bwrap) would be rebuilt if dispatch ran against the current
// base-branch tip — the image-freshness boundary ADR 0019 establishes for
// continuous-pipe dispatch. Probe fetches the base ref, evaluates the image
// attr's output path at that rev (a git+file flake eval, never a checkout or
// pull), and compares the same identity `build`/EnsureReady gates on. It
// never mutates the working copy, the checkout, or the loaded image/closure.
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

// Result is the outcome of a Probe call. The two dimension flags
// (ImageFresh, LauncherFresh) fail closed: any branch returning before that
// dimension's comparison is known leaves the flag false.
type Result struct {
	// Applicable is false when the probe cannot be checked at all (e.g. pwd
	// isn't a git repository, the base branch or origin remote is missing,
	// or the flake doesn't provide the image attr) — for any runnerKind,
	// including "bwrap".
	Applicable bool
	// Fresh is true only when both dimensions are fresh. Meaningless when
	// Applicable is false.
	Fresh bool
	// Message is a human-readable summary safe to print on `preview`.
	Message string
	// Rev is the fetched base-tip sha Eval was hermetically evaluated at —
	// "" when Applicable is false or the fetch failed. A caller that rebuilds
	// against this tip can recognize "already rebuilt this tip" against Rev
	// without re-parsing Message.
	Rev string
	// TipTag is the image identity a rebuild would load: the "<repo>:<hash>"
	// tag for OCI, or the raw nix store outPath for bwrap, which has no
	// repo:tag concept. Empty when the probe never derived it, or when the
	// image dimension is fresh — there is then no divergence to name.
	TipTag string
	// LauncherFresh covers the optional host-launcher-only dimension. True
	// when FlakeLauncherAttr is empty ("not configured" is not "stale") or
	// the freshly evaluated launcher store hash matches.
	LauncherFresh bool
	// ImageFresh reports the image dimension alone, independent of the
	// launcher dimension and of the overall Fresh verdict. Once known it
	// carries through the rest of Probe, including a launcher-side error.
	ImageFresh bool
	// TipLauncherHash is the bare store hash a launcher rebuild would
	// produce — the launcher dimension's mirror of TipTag, so a caller can
	// compare without re-parsing Message. Empty when unconfigured or never
	// derived.
	TipLauncherHash string
}

// storeHashPrefixLen and storeHashLen locate the 32-char base32 content
// hash in a nix store path: paths are always
// "/nix/store/<32-char-hash>-<name>", matching mkHarness.nix's own
// imageHash extraction (chars 11-42).
const (
	storeHashPrefixLen = len("/nix/store/")
	storeHashLen       = 32
)

// imageTagFromOutPath derives the "<repo>:<hash>" tag the same way
// mkHarness.nix's imageHash does — the exact currency `build`/EnsureReady
// gates on, so a fresh verdict here always corresponds to a rebuild build
// would actually perform. repo is the loaded image's own repo, so a
// driver-scoped image compares against the repo it was loaded under rather
// than a hardcoded one.
func imageTagFromOutPath(outPath, repo string) (string, error) {
	hash, err := storeHash(outPath)
	if err != nil {
		return "", err
	}
	return repo + ":" + hash, nil
}

// storeHash extracts the bare content hash from a nix store output path,
// without the "<repo>:" prefix imageTagFromOutPath adds. The launcher
// dimension compares this bare hash directly — a host-launcher binary has no
// "repo" concept the way an OCI image does.
func storeHash(outPath string) (string, error) {
	if !strings.HasPrefix(outPath, "/nix/store/") || len(outPath) < storeHashPrefixLen+storeHashLen {
		return "", fmt.Errorf("not a nix store path: %q", outPath)
	}
	return outPath[storeHashPrefixLen : storeHashPrefixLen+storeHashLen], nil
}

// imageRepo derives the repo portion of an "<repo>:<tag>" reference —
// everything before the LAST colon, since a repo can itself contain one (a
// registry host:port prefix). A colon-less imageTag falls back to the default
// repo rather than deriving an empty one.
func imageRepo(imageTag string) string {
	i := strings.LastIndex(imageTag, ":")
	if i < 0 {
		return "spindrift"
	}
	return imageTag[:i]
}

// trimFlakeAttrPrefix strips the ".#" flake-CLI shorthand prefix from attr,
// if present, so Probe's eval.Eval call and RealizeTip's Start call always
// address the exact same flake attribute string regardless of which form
// flakeImageAttr was configured with.
func trimFlakeAttrPrefix(attr string) string {
	return strings.TrimPrefix(attr, ".#")
}

// KindBwrap is the RUNNER_KIND value selecting the bwrap runner. Its agent
// closure has no "repo:tag" registry concept, so Probe compares the evaluated
// outPath directly against the loaded imageTag (a bare store path for bwrap)
// instead of deriving a tag. Any other value is treated as an OCI kind.
const KindBwrap = "bwrap"

// ProbeSpec is the set of params Probe needs to answer a freshness check.
type ProbeSpec struct {
	// RunnerKind is the RUNNER_KIND document artifact — KindBwrap selects the
	// bwrap comparison path, any other value an OCI kind. Pwd and BaseBranch
	// locate the repo and the branch whose tip gets fetched.
	RunnerKind, Pwd, BaseBranch string
	// FlakeImageAttr is evaluated at the fetched base tip; ImageTag is the
	// loaded image's tag (an OCI "repo:tag", or a bare nix store path under
	// KindBwrap) it's compared against.
	FlakeImageAttr, ImageTag string
	// FlakeLauncherAttr and LoadedLauncherHash drive the optional
	// host-launcher-only freshness dimension: when FlakeLauncherAttr is
	// non-empty, Probe also evaluates it and compares its store hash.
	FlakeLauncherAttr, LoadedLauncherHash string
}

// Probe answers whether the loaded image (OCI or bwrap agent closure) would
// be rebuilt if dispatch ran against the current base-branch tip.
//
// A caller must pass config.runnerKind, never the raw RUNTIME value: a
// bwrap-kind harness can carry an OCI runtime name under an operator
// override, and comparing that name would misclassify it as OCI.
//
// When configured, the launcher dimension is evaluated at the exact same
// fetched rev as the image. Overall Fresh requires both dimensions.
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

// freshnessMessage names whichever dimension(s) drove a rebuild-needed
// verdict, or confirms both match. runnerKind picks the noun for the loaded
// value: "image" for OCI, "closure" for bwrap, where the same slot holds a
// raw nix store path.
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

// fetchBaseTip fetches baseBranch from origin at pwd — no checkout, no pull,
// no working-copy mutation — and returns the fetched commit sha, unabbreviated
// so the format matches the launcher's own headRev, which the Console's
// rebuilt-tip comparison relies on.
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

// The four predicates below each separate a *definitive* "freshness cannot
// be checked here at all" diagnostic from a transient failure (network, DNS,
// unreachable remote) that could plausibly succeed later. A definitive match
// makes the probe not-applicable so the caller proceeds; a transient one is
// treated as rebuild-needed.

// isNotAGitRepository: pwd isn't inside any git worktree. Git emits this
// same wording whether it stops at the filesystem root or at a mount
// boundary, so a substring match covers both phrasings.
func isNotAGitRepository(err error) bool {
	return strings.Contains(err.Error(), "not a git repository")
}

// isRemoteRefMissing: origin has no baseBranch, so pwd is not the repo that
// branch lives in.
func isRemoteRefMissing(err error) bool {
	return strings.Contains(err.Error(), "couldn't find remote ref")
}

// isImageAttrMissing: the flake at pwd doesn't define the image attr at all,
// as opposed to an evaluation failure at an attr that does exist.
func isImageAttrMissing(err error) bool {
	return strings.Contains(err.Error(), "does not provide attribute")
}

// isNoOriginRemote: no "origin" is configured, or it points somewhere
// unreachable — a fully local repo (CODE_FORGE=local) has nothing to fetch.
func isNoOriginRemote(err error) bool {
	return strings.Contains(err.Error(), "does not appear to be a git repository")
}
