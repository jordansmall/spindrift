// Package freshness answers whether the loaded agent image (OCI) or bundled
// agent closure (bwrap) would be rebuilt if dispatch ran against the current
// base-branch tip — the image-freshness boundary ADR 0019 establishes for
// continuous-pipe dispatch (#478). Probe fetches the base ref, hermetically
// evaluates the image attr's output path at that fetched rev (a git+file
// flake eval, never a checkout or pull), derives the same identity
// `build`/EnsureReady gates on — a "<repo>:<hash>" content-hash tag for OCI,
// a raw nix store path for bwrap (issue #2667) — and compares it against the
// loaded value. It never mutates the working copy, the checkout, or the
// loaded image/closure.
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
	// Applicable is false when the probe cannot be checked at all (e.g. pwd
	// isn't a git repository, the base branch or origin remote is missing,
	// or the flake doesn't provide the image attr) — for any runnerKind,
	// including "bwrap".
	Applicable bool
	// Fresh is true when the evaluated identity matches the loaded one — a
	// content-hash tag for OCI, a raw nix store path for bwrap (issue
	// #2667). Meaningless when Applicable is false.
	Fresh bool
	// Message is a human-readable summary safe to print on `preview`.
	Message string
	// Rev is the fetched base-tip sha Eval was hermetically evaluated at —
	// "" when Applicable is false or the fetch itself failed. A caller that
	// rebuilds against this same tip (the Console's in-session rebuild,
	// issue #652) can recognize "already rebuilt this tip" against Rev
	// without re-parsing Message.
	Rev string
	// TipTag is the image identity freshly evaluated at the base tip — the
	// identity a rebuild would load. For an OCI runnerKind this is the
	// "<repo>:<hash>" tag derived from the evaluated outPath; for "bwrap"
	// there is no repo:tag concept, so this is the raw nix store outPath
	// itself, compared byte-for-byte. Empty when the probe never got far
	// enough to derive it, when the image dimension itself is fresh (only the
	// launcher dimension is stale — there is no genuine divergence to name),
	// or on a launcher eval/hash-derive failure. The non-convergence
	// diagnostic (issue #2113) names it alongside the loaded value so an
	// operator sees the two identities that will never converge.
	TipTag string
	// LauncherFresh is true when the host-launcher-only dimension is
	// considered fresh — no launcher rebuild would be needed. Meaningless
	// when Applicable is false. False (fail-closed) on every branch reached
	// before the launcher comparison itself is known — every
	// not-applicable/error branch that returns before the launcher
	// dimension is even reached (fetch failure, image eval error, image
	// tag-derive error), and a launcher eval/hash-derive failure once
	// flakeLauncherAttr is configured. Only reached and set true
	// when flakeLauncherAttr is empty (the launcher dimension isn't
	// configured — "not configured" is not "stale"), or when the freshly
	// evaluated launcher store hash matches loadedLauncherHash. Fresh is true
	// only when both LauncherFresh and the image comparison are true.
	LauncherFresh bool
	// ImageFresh is true only when the image dimension itself matched —
	// independent of the launcher dimension, and independent of the overall
	// Fresh verdict (which is also false when only the launcher is stale).
	// False (fail-closed) on every branch reached before the image
	// comparison itself is known — the not-applicable branches and every
	// image-side error (fetch failure, image eval error, image tag-derive
	// error). Once the image comparison is known, it carries that same
	// value through the rest of Probe, including a launcher eval/hash-derive
	// failure (the image succeeded; only the launcher dimension errored) and
	// the final success return.
	ImageFresh bool
	// TipLauncherHash is the bare 32-char store hash of flakeLauncherAttr
	// freshly evaluated at the base tip — the hash a launcher rebuild would
	// produce. Empty when flakeLauncherAttr is empty or the probe never got
	// far enough to derive it. Mirrors TipTag for the launcher dimension, for
	// a future caller (issue #2682) to compare without re-parsing Message.
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

// imageTagFromOutPath derives the "<repo>:<hash>" tag from a nix store
// output path the same way mkHarness.nix's imageHash does — the exact
// currency `build`/EnsureReady gates on (an already-loaded tag skips the
// rebuild), so a fresh verdict here always corresponds to a rebuild build
// would actually perform. repo is the loaded image's own repo (see
// imageRepo), so a driver-scoped image (e.g. "spindrift-opencode") compares
// its tip tag against the same repo it was loaded under, rather than a
// hardcoded "spindrift" repo.
func imageTagFromOutPath(outPath, repo string) (string, error) {
	hash, err := storeHash(outPath)
	if err != nil {
		return "", err
	}
	return repo + ":" + hash, nil
}

// storeHash extracts the bare 32-char base32 content hash from a nix store
// output path (see storeHashPrefixLen/storeHashLen), without the
// "<repo>:" tag prefixing imageTagFromOutPath adds on top. The launcher
// freshness dimension (issue #1364) compares this bare hash directly —
// there is no "repo" concept for a host-launcher binary the way there is
// for an OCI image.
func storeHash(outPath string) (string, error) {
	if !strings.HasPrefix(outPath, "/nix/store/") || len(outPath) < storeHashPrefixLen+storeHashLen {
		return "", fmt.Errorf("not a nix store path: %q", outPath)
	}
	return outPath[storeHashPrefixLen : storeHashPrefixLen+storeHashLen], nil
}

// imageRepo derives the repo portion of an "<repo>:<tag>" image reference —
// everything before the LAST colon, since a repo can itself contain a colon
// (e.g. a registry host:port prefix). Falls back to the default "spindrift"
// repo when imageTag has no colon at all (a degenerate/empty tag), rather
// than deriving an empty or nonsensical repo.
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

// KindBwrap is the RUNNER_KIND value selecting the bwrap runner. Unlike an
// OCI kind, bwrap has no "repo:tag" registry concept for its agent closure,
// so Probe compares the freshly evaluated outPath directly against the
// loaded imageTag (itself a bare store path for bwrap) instead of deriving
// an "<repo>:<hash>" tag. Any other runnerKind value, including an OCI
// runtime name, is treated as an OCI kind.
const KindBwrap = "bwrap"

// ProbeSpec is the set of params Probe needs to answer a freshness check.
type ProbeSpec struct {
	// RunnerKind is the RUNNER_KIND document artifact (issue #2538 AC1) —
	// KindBwrap selects the bwrap comparison path, any other value an OCI
	// kind. Pwd and BaseBranch locate the git repo and base branch to fetch
	// the tip of when checking freshness.
	RunnerKind, Pwd, BaseBranch string
	// FlakeImageAttr is the flake attr Probe evaluates at the fetched base
	// tip; ImageTag is the loaded image's tag (an OCI "repo:tag" string, or
	// for KindBwrap a bare nix store path) it's compared against.
	FlakeImageAttr, ImageTag string
	// FlakeLauncherAttr and LoadedLauncherHash drive the optional
	// host-launcher-only freshness dimension (issue #1364): when
	// FlakeLauncherAttr is non-empty, Probe also evaluates it and compares
	// its store hash against LoadedLauncherHash.
	FlakeLauncherAttr, LoadedLauncherHash string
}

// Probe answers whether the loaded image (OCI or bwrap agent closure) would
// be rebuilt if dispatch ran against the current base-branch tip.
// spec.RunnerKind is the RUNNER_KIND document artifact (issue #2538 AC1) —
// never a runtime-name comparison — so a caller must pass config.runnerKind,
// not the raw RUNTIME/c.runtime value: a bwrap-kind harness can still carry
// an OCI runtime name (e.g. an operator override), and comparing that name
// directly would misclassify it as OCI. For spec.RunnerKind == KindBwrap,
// the freshly evaluated outPath is compared byte-for-byte against
// spec.ImageTag (which for bwrap holds a bare nix store path, not a
// "repo:tag" string) — no tag derivation. spec.FlakeLauncherAttr and
// spec.LoadedLauncherHash drive the host-launcher-only freshness dimension
// (issue #1364): when spec.FlakeLauncherAttr is non-empty, Probe also
// evaluates it at the exact same fetched rev used for the image and
// compares its store hash against spec.LoadedLauncherHash. Overall Fresh is
// true only when both dimensions are fresh (or the launcher dimension isn't
// configured at all, i.e. spec.FlakeLauncherAttr == "").
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

// freshnessMessage builds the human-readable summary for a probe that
// successfully evaluated the image dimension (and, if configured, the
// launcher dimension) — naming whichever dimension(s) drove a
// rebuild-needed verdict, or confirming both match when fresh. When the
// launcher dimension isn't configured at all (launcherConfigured is false),
// it preserves the original image-only wording unchanged. runnerKind picks
// the noun for the loaded value — "image" for OCI, "closure" for bwrap
// (issue #2667), since the same slot holds a raw nix store path there, not
// an OCI image.
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

// isNotAGitRepository reports whether err (as returned by fetchBaseTip) is
// git's own "not a git repository" diagnostic — pwd isn't inside any git
// worktree at all — rather than a transient failure (network, unreachable
// remote, missing "origin") inside a real repository. Git emits this same
// "not a git repository" wording whether it stops at the filesystem root or
// at a mount boundary (GIT_DISCOVERY_ACROSS_FILESYSTEM unset), so a substring
// match covers both phrasings.
func isNotAGitRepository(err error) bool {
	return strings.Contains(err.Error(), "not a git repository")
}

// isRemoteRefMissing reports whether err (as returned by fetchBaseTip) is
// git's own "couldn't find remote ref" diagnostic — origin simply has no
// baseBranch — rather than a transient failure (network, unreachable
// remote) inside a repo that could plausibly have it. This is definitive,
// not transient: pwd is not the repo baseBranch lives in, so freshness
// cannot be checked here, and the caller should proceed rather than treat
// it as rebuild-needed (#1753).
func isRemoteRefMissing(err error) bool {
	return strings.Contains(err.Error(), "couldn't find remote ref")
}

// isImageAttrMissing reports whether err (as returned by Eval) is nix's own
// "does not provide attribute" diagnostic — the flake at pwd simply doesn't
// define flakeImageAttr — rather than a genuine evaluation/build failure at
// an attr that does exist. This is definitive, not transient: pwd is not the
// spindrift image-source flake, so freshness cannot be checked here, and the
// caller should proceed rather than treat it as rebuild-needed (#1754).
func isImageAttrMissing(err error) bool {
	return strings.Contains(err.Error(), "does not provide attribute")
}

// isNoOriginRemote reports whether err (as returned by fetchBaseTip) is
// git's own "does not appear to be a git repository" diagnostic — no
// "origin" remote is configured, or origin points somewhere unreachable —
// rather than a transient failure (network, DNS) against a remote that
// could plausibly answer. This is definitive, not transient: a fully local
// repo (e.g. CODE_FORGE=local with no live remote) has nothing to fetch, so
// freshness cannot be checked here, and the caller should proceed rather
// than treat it as rebuild-needed (#2034).
func isNoOriginRemote(err error) bool {
	return strings.Contains(err.Error(), "does not appear to be a git repository")
}
