package main

import "spindrift.dev/launcher/internal/runner"

// signalSocketNoneModeReason is a const expression, not a var, so go vet's
// printf check still analyses the formats it composes into. The doctor
// signal-socket-transport row composes from it too (one %s: the network mode).
const signalSocketNoneModeReason = "BOX_SIGNAL_CARRIER=socket is unsupported under NETWORK_MODE=%s -- the socket transport needs loopback, which this mode tears down"

// checkSignalCarrierNetworkModeGate backstops the one NETWORK_MODE pairing
// the socket signal carrier (issue #3725) can never work under:
// NETWORK_MODE=none tears down the loopback the socket transport needs, so
// BOX_SIGNAL_CARRIER=socket must fail closed at launch rather than silently
// fall back to the log carrier.
//
// The no-host-loopback case is deliberately not checked here: it depends on
// the live transport verdict a per-Dispatch probe produces, not a static
// config value, so it lives in the Dispatch-level gate startSignalSocket
// applies (internal/dispatch/signal_socket.go) instead.
func checkSignalCarrierNetworkModeGate(c config) error {
	if c.signalCarrier != "socket" {
		return nil
	}
	if c.networkMode != runner.NetworkModeNone {
		return nil
	}
	return newLaunchGateConfigError(signalSocketNoneModeReason+"; use BOX_SIGNAL_CARRIER=log or a different NETWORK_MODE", c.networkMode)
}
