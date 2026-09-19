// hosttaint.go recognizes a freshness divergence no rebuild can fix: a
// host-realized derivation (a darwin package, extraClosure, or skill for OCI;
// a host-architecture derivation for bwrap) reaching the agent image or
// closure graph through a consumer flake can never evaluate to the baked
// content-hash tag or store path, so Probe's "rebuild needed" never converges.
package freshness

import "fmt"

// NonConverging reports whether staleRev repeats the rev the prior launcher
// process already exited stale on, meaning a rebuild against the same base tip
// left it stale. An empty staleRev, meaning no rev was fetched, is never
// non-converging.
func NonConverging(staleRev, priorStaleRev string) bool {
	return staleRev != "" && staleRev == priorStaleRev
}

// HostTaintDiagnostic returns a one-shot operator diagnostic for a freshness
// divergence that persisted across a rebuild to the base tip. Under KindBwrap,
// tipTag and imageTag are bare nix store paths and darwin can never be the
// culprit, since lib/mkHarness.nix gates agent-closure on isLinux. Any other
// runnerKind is OCI, where the tags are "repo:tag" strings.
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
