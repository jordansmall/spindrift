package freshness

import (
	"fmt"
	"strings"
)

// Artifact is the Box artifact seam (ADR 0069 decision 3): what freshness
// compares, what it calls that thing, how it explains a divergence no rebuild
// fixes, and whether a Box-only rebuild may be swapped in without draining.
// Freshness reads it, never the runner family, so a new runner row reuses an
// existing adapter by naming its artifact. Use OCIImage or StoreClosure; a
// zero Artifact is not usable.
type Artifact struct {
	// Name is the artifact's identifier, as a runner row's Artifact and the run
	// document's BOX_ARTIFACT spell it.
	Name string
	// Noun is how the loaded value reads in freshness messages.
	Noun string
	// HotSwappable is true when a box-only rebuild can be bound in without
	// draining the run.
	HotSwappable bool

	tipIdentity func(outPath, loaded string) (string, error)
	hostTaint   func(baseBranch, rev, flakeImageAttr, tipTag, imageTag string) string
}

// TipIdentity derives the identity a rebuild at the base tip would load from
// the evaluated outPath, given the loaded identity. Probe compares the result
// to loaded for equality.
func (a Artifact) TipIdentity(outPath, loaded string) (string, error) {
	return a.tipIdentity(outPath, loaded)
}

// HostTaintDiagnostic returns a one-shot operator diagnostic for a freshness
// divergence that persisted across a rebuild to the base tip. tipTag and
// imageTag are this artifact's identities.
func (a Artifact) HostTaintDiagnostic(baseBranch, rev, flakeImageAttr, tipTag, imageTag string) string {
	return a.hostTaint(baseBranch, rev, flakeImageAttr, tipTag, imageTag)
}

// OCIImage is identified by a "repo:tag" reference whose tag is the image's
// content hash.
var OCIImage = Artifact{
	Name: "oci-image",
	Noun: "image",
	tipIdentity: func(outPath, loaded string) (string, error) {
		return imageTagFromOutPath(outPath, ImageRepo(loaded))
	},
	hostTaint: func(baseBranch, rev, flakeImageAttr, tipTag, imageTag string) string {
		return hostTaintText(baseBranch, rev, flakeImageAttr,
			fmt.Sprintf("the evaluated image tag %s still does not match the loaded image tag %s.", tipTag, imageTag),
			"Likely cause: a consumer flake's packages, extraClosures, or skills "+
				"pulls in a host-system (e.g. darwin) derivation that reaches the "+flakeImageAttr+" "+
				"image graph. On a non-Linux host that derivation can never evaluate "+
				"to the same content-hash tag the baked image carries, so the tags will "+
				"never converge.",
			" | grep -i darwin")
	},
}

// StoreClosure is identified by its bare nix store path. Darwin can never be
// the host-taint culprit, since lib/mkHarness.nix gates agent-closure on
// isLinux.
var StoreClosure = Artifact{
	Name:         "store-closure",
	Noun:         "closure",
	HotSwappable: true,
	tipIdentity: func(outPath, _ string) (string, error) {
		return outPath, nil
	},
	hostTaint: func(baseBranch, rev, flakeImageAttr, tipTag, imageTag string) string {
		return hostTaintText(baseBranch, rev, flakeImageAttr,
			fmt.Sprintf("the evaluated closure identity %s still does not match the loaded closure identity %s.", tipTag, imageTag),
			"Likely cause: a consumer flake's packages, extraClosures, or skills "+
				"pulls in a host-realized derivation (e.g. one that differs by host "+
				"architecture, or any other non-deterministic host-realized output) "+
				"that reaches the "+flakeImageAttr+" closure graph. Such a derivation can never "+
				"evaluate to the same store path the baked closure carries, so the "+
				"paths will never converge.",
			"")
	},
}

// LookupArtifact resolves a BOX_ARTIFACT name. An empty name is OCIImage, the
// way an empty RUNNER_KIND resolves to OCI; an unknown name is an error rather
// than a silent default.
func LookupArtifact(name string) (Artifact, error) {
	switch name {
	case "", OCIImage.Name:
		return OCIImage, nil
	case StoreClosure.Name:
		return StoreClosure, nil
	}
	return Artifact{}, fmt.Errorf("unknown Box artifact %q (want %s or %s)", name, OCIImage.Name, StoreClosure.Name)
}

// ImageRepo returns everything before the LAST colon of an "<repo>:<tag>"
// reference, since a repo can itself contain a colon (a registry host:port
// prefix). Falls back to "spindrift" when imageTag has no colon at all.
func ImageRepo(imageTag string) string {
	i := strings.LastIndex(imageTag, ":")
	if i < 0 {
		return "spindrift"
	}
	return imageTag[:i]
}

// hostTaintText assembles the diagnostic around the parts that differ per
// artifact: what was compared, the likely cause, and an optional filter piped
// onto the derivation-show command. Keeping the shared sentences here stops the
// adapters drifting apart.
func hostTaintText(baseBranch, rev, flakeImageAttr, mismatch, cause, showSuffix string) string {
	return fmt.Sprintf(
		"freshness divergence did not converge after rebuilding to %s tip %s: %s\n"+
			"This persisted across a rebuild to the base tip, so the divergence is "+
			"structural (host), not stale content — a rebuild cannot fix it.\n"+
			"%s\n"+
			"To locate the offending derivation, run:\n"+
			"  nix derivation show -r %s%s",
		baseBranch, rev, mismatch, cause, flakeImageAttr, showSuffix,
	)
}
