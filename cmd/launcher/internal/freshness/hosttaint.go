// hosttaint.go holds pure helpers for recognizing and diagnosing a
// non-converging freshness divergence caused by a host-system derivation
// (e.g. a darwin package, extraClosure, or skill) reaching the linux
// agent-image graph through a consumer flake. On a non-Linux host such a
// derivation can never evaluate to the same content-hash tag the baked
// IMAGE_TAG carries, so Probe's ordinary "rebuild needed" verdict never
// converges no matter how many times the caller rebuilds — these helpers
// let a caller that tracks its own prior stale rev recognize that case and
// hand the operator an actionable diagnostic instead of looping forever.
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
// host-system derivation reaching the image graph through a consumer
// flake's packages, extraClosures, or skills) and gives the command to
// locate it.
func HostTaintDiagnostic(baseBranch, rev, flakeImageAttr, tipTag, imageTag string) string {
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
