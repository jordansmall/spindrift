package main

import (
	"fmt"
	"os"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/registrymanifest"
)

// registryProxyTransportFn probes the runtime for its registry-proxy transport
// decision (issue #3111/#3114). It is a var because doctor must never start a
// container, so tests script a runner.Fake's answer instead. The discarded
// second return is tcpAddHost, which steers a dispatch's --add-host wiring and
// tells doctor nothing.
var registryProxyTransportFn = func(c config) (registrymanifest.Endpoint, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return registrymanifest.Endpoint{}, err
	}
	endpoint, _, err := runnerForKind(c, runnerConfig(c), pwd).RegistryProxyTransport()
	return endpoint, err
}

// registryProxyTransportCheckName keeps the row's Name field and its
// SuccessMsg closure from drifting apart on a rename (issue #2853).
const registryProxyTransportCheckName = "registry-proxy-transport"

// registryProxyTransportCheck builds the registry-proxy-transport row. It calls
// the same RegistryProxyTransport a dispatch calls, so the report cannot drift
// from real behaviour. The row never affects doctor's exit-2 classification:
// both a unix socket and a TCP answer are working outcomes (ADR 0044/0045), so
// only an indeterminate probe is worth reporting.
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
				// A zero Endpoint is an indeterminate probe answer, not a
				// socket-vs-TCP finding, so report it rather than print a
				// blank transport.
				return nil, fmt.Errorf("registry proxy transport probe returned neither a unix nor a TCP endpoint: %w", doctor.ErrDegraded)
			}
		},
		SuccessMsg: func(output any) string {
			return fmt.Sprintf("%s (%s)", registryProxyTransportCheckName, output)
		},
	}
}
