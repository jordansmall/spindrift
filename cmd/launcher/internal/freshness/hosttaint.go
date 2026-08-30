// hosttaint.go holds pure helpers for recognizing and diagnosing a
// non-converging freshness divergence caused by a host-realized derivation
// (e.g. a darwin package, extraClosure, or skill, for OCI; or a
// host-architecture-realized derivation for bwrap) reaching the agent
// image/closure graph through a consumer flake. Such a derivation can never
// evaluate to the same content-hash tag (OCI) or store path (bwrap) the
// baked image/closure carries, so Probe's ordinary "rebuild needed" verdict
// never converges no matter how many times the caller rebuilds — these
// helpers let a caller that tracks its own prior stale rev recognize that
// case and hand the operator an actionable diagnostic instead of looping
// forever.
package freshness

import "fmt"

// NonConverging reports whether a stale verdict at staleRev, given the rev
// the prior launcher process already exited stale on (priorStaleRev), is a
// non-converging host-tainted divergence: the same base tip was already
// rebuilt against yet the image is still stale. An empty staleRev (no rev
// fetched) is never non-converging.
func NonConverging(staleRev, priorStaleRev string) bool {
	return staleRev != "" && staleRev == priorStaleRev
}

// HostTaintDiagnostic returns a one-shot operator diagnostic for a
// non-converging (host-tainted) freshness divergence — a divergence that
// persists after rebuilding to the base tip. It names the likely cause (a
// host-realized derivation reaching the image/closure graph through a
// consumer flake's packages, extraClosures, or skills) and gives the
// command to locate it. runnerKind selects the wording: for
// runnerKind == KindBwrap, tipTag/imageTag are bare nix store paths (see
// Probe's doc comment), and darwin can never be the culprit — the
// agent-closure package, and bwrap generally, only exists when the host is
// already Linux (lib/mkHarness.nix's isLinux && runtime == "bwrap" gate).
// Any other runnerKind is treated as OCI, where tipTag/imageTag are
// "repo:tag" strings and a darwin-realized derivation is the common cause.
func HostTaintDiagnostic(runnerKind, baseBranch, rev, flakeImageAttr, tipTag, imageTag string) string {
	if runnerKind == KindBwrap {
		return fmt.Sprintf(
			"freshness divergence did not converge after rebuilding to %s tip %s: "+
				"the evaluated closure identity %s still does not match the loaded closure identity %s.\n"+
				"This persisted across a rebuild to the base tip, so the divergence is "+
				"structural (host), not stale content — a rebuild cannot fix it.\n"+
				"Likely cause: a consumer flake's packages, extraClosures, or skills "+
				"pulls in a host-realized derivation (e.g. one that differs by host "+
				"architecture, or any other non-deterministic host-realized output) "+
				"that reaches the %s closure graph. Such a derivation can never "+
				"evaluate to the same store path the baked closure carries, so the "+
				"paths will never converge.\n"+
				"To locate the offending derivation, run:\n"+
				"  nix derivation show -r %s",
			baseBranch, rev, tipTag, imageTag, flakeImageAttr, flakeImageAttr,
		)
	}
	return fmt.Sprintf(
		"freshness divergence did not converge after rebuilding to %s tip %s: "+
			"the evaluated image tag %s still does not match the loaded image tag %s.\n"+
			"This persisted across a rebuild to the base tip, so the divergence is "+
			"structural (host), not stale content — a rebuild cannot fix it.\n"+
			"Likely cause: a consumer flake's packages, extraClosures, or skills "+
			"pulls in a host-system (e.g. darwin) derivation that reaches the %s "+
			"image graph. On a non-Linux host that derivation can never evaluate "+
			"to the same content-hash tag the baked image carries, so the tags will "+
			"never converge.\n"+
			"To locate the offending derivation, run:\n"+
			"  nix derivation show -r %s | grep -i darwin",
		baseBranch, rev, tipTag, imageTag, flakeImageAttr, flakeImageAttr,
	)
}
