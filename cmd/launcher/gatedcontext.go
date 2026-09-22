package main

import (
	"io"

	"spindrift.dev/launcher/internal/doctor"
)

// gatedContext is a readContext whose construction already ran validate() and
// every gate in gateRegistry (issues #2941, #2942). preview() and bootstrap()
// build one (issue #2944); the read-only paths keep the plain readContext,
// which never validates.
type gatedContext struct {
	readContext
}

// newGatedContext splices the bwrap gates between gateRegistry's non-Network
// and Network halves rather than appending them after, so preview and real
// dispatch stop at the same first failure and the token gates' live network
// calls never run ahead of a bwrap failure.
func newGatedContext(w io.Writer, kind string, selfContained bool) (gatedContext, error) {
	rc := newReadContext(kind, selfContained)
	if err := validate(rc.config); err != nil {
		return gatedContext{}, err
	}
	// Enforcement has no report stream, so both halves discard it here;
	// hoisted into one instance rather than one io.Discard Reporter per call.
	discardRep := doctor.NewReporter(io.Discard)
	nonNetwork, network := splitGateRegistryByNetwork(gateRegistry)
	if err := walkGateRegistry(nonNetwork, rc.config, w, discardRep, false); err != nil {
		return gatedContext{}, err
	}
	if err := checkBwrapPastaGate(rc.config); err != nil {
		return gatedContext{}, err
	}
	if err := checkBwrapOverlayGate(rc.config); err != nil {
		return gatedContext{}, err
	}
	if err := walkGateRegistry(network, rc.config, w, discardRep, false); err != nil {
		return gatedContext{}, err
	}
	return gatedContext{readContext: rc}, nil
}
