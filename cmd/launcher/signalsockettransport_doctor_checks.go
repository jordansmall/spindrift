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
	if c.signalCarrier != "socket" {
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
		Remedy: "set BOX_SIGNAL_CARRIER=log, or use a NETWORK_MODE that permits loopback (not no-host-loopback or none); on an indeterminate probe instead, confirm the configured container runtime is running and reachable, then re-run `spindrift doctor`",
		Probe: func() (any, error) {
			if c.networkMode == runner.NetworkModeNone {
				// Answered before the probe because the verdict cannot depend
				// on the transport: this mode tears down loopback outright, so
				// checkSignalCarrierNetworkModeGate already refuses the pairing
				// at launch, and probing would cost a runtime round trip for a
				// configuration that can never run.
				return nil, fmt.Errorf("BOX_SIGNAL_CARRIER=socket is unsupported under NETWORK_MODE=%s -- the socket transport needs loopback, which this mode tears down: %w", c.networkMode, doctor.ErrDegraded)
			}
			endpoint, err := registryProxyTransportFn(c)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", err, doctor.ErrDegraded)
			}
			switch {
			case endpoint.IsUnix():
				return "unix socket", nil
			case endpoint.IsTCP():
				if c.networkMode == runner.NetworkModeNoHostLoopback {
					// Same condition the Dispatch-level gate in
					// internal/dispatch/signal_socket.go fails closed on for
					// this mode: a TCP-only transport under a mode that blocks
					// loopback leaves the socket carrier no usable path.
					return nil, fmt.Errorf("BOX_SIGNAL_CARRIER=socket has no usable transport under NETWORK_MODE=%s -- this runtime can only reach the Signal socket over its TCP fallback, which this mode blocks: %w", c.networkMode, doctor.ErrDegraded)
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
