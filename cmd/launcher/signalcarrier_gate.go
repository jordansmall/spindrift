package main

import "spindrift.dev/launcher/internal/runner"

// signalSocketNoneModeReason is a const expression, not a var, so go vet's
// printf check still analyses the formats it composes into. The doctor
// signal-socket-transport row composes from it too (one %s: the network mode).
const signalSocketNoneModeReason = "BOX_SIGNAL_CARRIER=socket is unsupported under NETWORK_MODE=%s -- the socket transport needs loopback, which this mode tears down"

// checkSignalCarrierNetworkModeGate backstops the one NETWORK_MODE pairing
// the socket signal carrier (issue #3725) can never work under:
// NETWORK_MODE=none tears down loopback, so BOX_SIGNAL_CARRIER=socket must
// fail closed at launch rather than silently fall back to the log carrier.
// The no-host-loopback case depends on the live transport verdict, so the
// Dispatch-level gate in internal/dispatch/signal_socket.go checks it.
func checkSignalCarrierNetworkModeGate(c config) error {
	if c.SignalCarrier != "socket" {
		return nil
	}
	if c.runnerNetworkMode() != runner.NetworkModeNone {
		return nil
	}
	return newLaunchGateConfigError(signalSocketNoneModeReason+"; use --box-signal-carrier log or a different NETWORK_MODE", c.NetworkMode)
}
