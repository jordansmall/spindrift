package main

import "io"

// gatedContext extends readContext (issue #2941) with the validate+gate
// prologue a dispatch-capable entry point needs before it can trust config
// enough to act on it: readContext alone deliberately never validates, so it
// is only safe for the read-only paths (doctor's probes, reconcile) that
// came before this issue. preview() is the one caller today; bootstrap.go
// still hardcodes its own six inline gate calls directly instead of going
// through this constructor, and doctor never constructs a gatedContext at
// all — it walks gateRegistry directly (runDoctor/walkSplitGateRegistry),
// with no validate() step. Embedding readContext means a gatedContext
// carries the same config/issueTracker/codeForge trio, just with the
// guarantee that construction already ran validate(c) and every gate in
// gateRegistry (issue #2942).
type gatedContext struct {
	readContext
}

// newGatedContext builds a gatedContext: it validates rc.config first, since
// enforcing that invariant is exactly what distinguishes a gatedContext from
// the plain readContext it embeds, then walks every gate in the exact
// interleaved order bootstrap.go's own inline gate calls use — capability,
// network-mode, bwrap-pasta, bwrap-overlay, gh-token, forgejo-token —
// stopping at and returning the first failure. The bwrap gates are spliced
// between gateRegistry's non-Network and Network halves (via
// splitGateRegistryByNetwork) rather than appended after, so a config that
// trips both a bwrap gate and a token gate reports the same "first broken
// thing" preview (via this function) and real dispatch (via bootstrap())
// would each stop at, and the token gates' live network calls never run
// ahead of a bwrap-pasta/bwrap-overlay failure dispatch itself would never
// get past either. See splitGateRegistryByNetwork's doc in launchgates.go
// for why this split can't silently diverge from doctor's report order.
func newGatedContext(w io.Writer) (gatedContext, error) {
	rc := newReadContext()
	if err := validate(rc.config); err != nil {
		return gatedContext{}, err
	}
	nonNetwork, network := splitGateRegistryByNetwork(gateRegistry)
	if err := walkGateRegistry(nonNetwork, rc.config, w, io.Discard, false); err != nil {
		return gatedContext{}, err
	}
	if err := checkBwrapPastaGate(rc.config); err != nil {
		return gatedContext{}, err
	}
	if err := checkBwrapOverlayGate(rc.config); err != nil {
		return gatedContext{}, err
	}
	if err := walkGateRegistry(network, rc.config, w, io.Discard, false); err != nil {
		return gatedContext{}, err
	}
	return gatedContext{readContext: rc}, nil
}
