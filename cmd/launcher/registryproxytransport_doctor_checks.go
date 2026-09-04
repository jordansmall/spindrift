package main

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// registryProxyTransportFn probes the configured runtime for its registry-
// proxy transport decision (runner.Runner.RegistryProxyTransport, issue
// #3111/#3114): a unix Endpoint when the runtime can mount a connectable
// socket into a Box, a TCP Endpoint naming the loopback host otherwise. A
// seam var, following the bwrap_doctor_checks.go pattern (validateOverlayFn
// et al.), so registryProxyTransportCheck's tests can script a runner.Fake's
// answer instead of probing a real runtime -- doctor must never start a
// container. tcpAddHost is discarded here: it steers a dispatch's --add-host
// wiring, not a fact doctor has any use reporting.
var registryProxyTransportFn = func(c config) (registrymanifest.Endpoint, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return registrymanifest.Endpoint{}, err
	}
	endpoint, _, err := runnerForKind(c, runnerConfig(c), pwd).RegistryProxyTransport()
	return endpoint, err
}

// registryProxyTransportCheckName is the registry-proxy-transport row's
// Name, factored into a const so the row's Name field and its SuccessMsg
// closure can't drift apart on a future rename (issue #2853).
const registryProxyTransportCheckName = "registry-proxy-transport"

// registryProxyTransportCheck builds the registry-proxy-transport row: it
// reports which transport (unix socket vs loopback TCP) a dispatch would use
// to reach the launcher-side registry proxy, via the identical
// registryProxyTransportFn seam (RegistryProxyTransport) a dispatch itself
// calls (internal/dispatch/box.go), so doctor's report can never drift from
// what a real dispatch does.
//
// Advisory, unconditionally: unlike registryProxyRoutesCheck, this row never
// affects doctor's exit-2 classification -- both a socket and a TCP answer
// are working outcomes (ADR 0044/0045), so there is no failing transport to
// gate a launch on, only an indeterminate one (the probe erroring, or
// returning neither IsUnix() nor IsTCP()) worth surfacing as "advisory:".
func registryProxyTransportCheck(c config) doctor.Check {
	return doctor.Check{
		Name:   registryProxyTransportCheckName,
		Tier:   doctor.Advisory,
		Remedy: "confirm the configured container runtime is running and reachable, then re-run `spindrift doctor` -- only an indeterminate probe needs action here, since both a unix socket and a TCP transport are working outcomes",
		Probe: func() (any, error) {
			if c.registryProxyRoutesFile == "" {
				return "not configured", nil
			}
			endpoint, err := registryProxyTransportFn(c)
			if err != nil {
				return nil, fmt.Errorf("%w: %w", err, doctor.ErrDegraded)
			}
			switch {
			case endpoint.IsUnix():
				return "unix socket", nil
			case endpoint.IsTCP():
				return "tcp", nil
			default:
				// The zero Endpoint: neither scheme, an indeterminate probe
				// answer rather than a genuine socket-vs-TCP finding -- report
				// it via ErrDegraded rather than silently printing a blank
				// transport.
				return nil, fmt.Errorf("registry proxy transport probe returned neither a unix nor a TCP endpoint: %w", doctor.ErrDegraded)
			}
		},
		SuccessMsg: func(output any) string {
			return fmt.Sprintf("%s (%s)", registryProxyTransportCheckName, output)
		},
	}
}
