package main

import (
	"fmt"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/runner"
)

// signalSocketTransportCheckName keeps the row's Name field and its
// SuccessMsg closure from drifting apart on a rename (issue #2853).
const signalSocketTransportCheckName = "signal-socket-transport"

// signalSocketTransportChecks builds the row only when
// BOX_SIGNAL_CARRIER=socket; under the log carrier it must not render at
// all, unlike registryProxyTransportCheck's always-present not-configured
// arm.
func signalSocketTransportChecks(c config) []doctor.Check {
	if c.SignalCarrier != "socket" {
		return nil
	}
	return []doctor.Check{signalSocketTransportCheck(c)}
}

// signalSocketTransportCheck reuses registryProxyTransportFn, the same probe
// a dispatch's signal socket consumes. It is Advisory: an unavailable
// transport must warn, never fail the check.
func signalSocketTransportCheck(c config) doctor.Check {
	return doctor.Check{
		Name:   signalSocketTransportCheckName,
		Tier:   doctor.Advisory,
		Remedy: "pass --box-signal-carrier log, or use a NETWORK_MODE that permits loopback (not no-host-loopback or none); on an indeterminate probe instead, " + runtimeReachableRemedy,
		Probe: func() (any, error) {
			if c.runnerNetworkMode() == runner.NetworkModeNone {
				// The verdict cannot depend on the transport: this mode tears
				// down loopback, so checkSignalCarrierNetworkModeGate already
				// refuses the pairing at launch, and probing would only cost a
				// runtime round trip.
				return nil, fmt.Errorf(signalSocketNoneModeReason+": %w", c.NetworkMode, doctor.ErrDegraded)
			}
			endpoint, err := registryProxyTransportFn(c)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", err, doctor.ErrDegraded)
			}
			switch {
			case endpoint.IsUnix():
				return "unix socket", nil
			case endpoint.IsTCP():
				if c.runnerNetworkMode().DeniesHostLoopback() {
					// internal/dispatch/signal_socket.go's gate fails closed
					// on this too: a TCP-only transport under a mode that
					// blocks loopback leaves the socket carrier no path. A live
					// probe errors before its TCP sub-probe under
					// no-host-loopback and the probe cache refuses to replay
					// that (#3775), so this is a backstop.
					return nil, fmt.Errorf("BOX_SIGNAL_CARRIER=socket has no usable transport under NETWORK_MODE=%s -- this runtime can only reach the Signal socket over its TCP fallback, which this mode blocks: %w", c.NetworkMode, doctor.ErrDegraded)
				}
				return "tcp", nil
			default:
				// A zero Endpoint is an indeterminate probe answer, not a
				// socket-vs-TCP finding, so report it rather than print a
				// blank transport.
				return nil, fmt.Errorf("signal socket transport probe returned neither a unix nor a TCP endpoint: %w", doctor.ErrDegraded)
			}
		},
		SuccessMsg: func(output any) string {
			return fmt.Sprintf("%s (%s)", signalSocketTransportCheckName, output)
		},
	}
}
