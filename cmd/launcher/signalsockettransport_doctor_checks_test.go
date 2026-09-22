package main

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/runner"
)

// The row reports whatever the shared probe answered, not a locally guessed
// default -- same reasoning as registryProxyTransportCheck's unix arm.
func TestSignalSocketTransportCheck_UnixSocketReportsUnixTransport(t *testing.T) {
	fake := runner.NewFake()
	fake.RegistryProxyTransportEndpoint = registrymanifest.NewUnixEndpoint("")
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.signalCarrier = "socket"

	check := signalSocketTransportCheck(c)
	output, err := check.Probe()
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil", err)
	}
	if got := check.SuccessMsg(output); got != "signal-socket-transport (unix socket)" {
		t.Errorf("SuccessMsg(%v) = %q, want %q", output, got, "signal-socket-transport (unix socket)")
	}
}

// TCP is a passing outcome, not a failure, under a network mode that still
// permits loopback -- same reasoning as the dispatch-level gate.
func TestSignalSocketTransportCheck_TCPUnderPermissiveModeReportsTCPTransport(t *testing.T) {
	fake := runner.NewFake()
	fake.RegistryProxyTransportEndpoint = registrymanifest.NewTCPEndpoint("", "")
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.signalCarrier = "socket"
	c.networkMode = runner.NetworkModeHost

	check := signalSocketTransportCheck(c)
	output, err := check.Probe()
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil -- TCP is a passing outcome under a mode that permits loopback", err)
	}
	if got := check.SuccessMsg(output); got != "signal-socket-transport (tcp)" {
		t.Errorf("SuccessMsg(%v) = %q, want %q", output, got, "signal-socket-transport (tcp)")
	}
}

// TCP under no-host-loopback leaves no usable transport for the socket
// carrier -- the same condition the Dispatch-level gate in
// internal/dispatch/signal_socket.go fails closed on, but here it must only
// warn (Advisory), never block doctor's exit code.
func TestSignalSocketTransportCheck_TCPUnderNoHostLoopbackWrapsErrDegraded(t *testing.T) {
	fake := runner.NewFake()
	fake.RegistryProxyTransportEndpoint = registrymanifest.NewTCPEndpoint("", "")
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.signalCarrier = "socket"
	c.networkMode = runner.NetworkModeNoHostLoopback

	check := signalSocketTransportCheck(c)
	_, err := check.Probe()
	if !errors.Is(err, doctor.ErrDegraded) {
		t.Errorf("Probe() error = %v, want it to wrap doctor.ErrDegraded", err)
	}
	if err == nil || !strings.Contains(err.Error(), string(runner.NetworkModeNoHostLoopback)) {
		t.Errorf("Probe() error = %v, want it to name %q", err, runner.NetworkModeNoHostLoopback)
	}
	if check.Tier != doctor.Advisory {
		t.Errorf("Tier = %v, want doctor.Advisory -- this row must never block the run", check.Tier)
	}
}

// NETWORK_MODE=none tears down loopback outright, so the row must degrade
// regardless of what the probe would have answered -- a unix verdict here
// would otherwise contradict the signal-carrier-network-mode gate that
// already refuses this pairing at launch (checkSignalCarrierNetworkModeGate,
// signalcarrier_gate.go).
func TestSignalSocketTransportCheck_NetworkModeNoneWithUnixEndpointWrapsErrDegraded(t *testing.T) {
	fake := runner.NewFake()
	fake.RegistryProxyTransportEndpoint = registrymanifest.NewUnixEndpoint("")
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.signalCarrier = "socket"
	c.networkMode = runner.NetworkModeNone

	check := signalSocketTransportCheck(c)
	_, err := check.Probe()
	if !errors.Is(err, doctor.ErrDegraded) {
		t.Errorf("Probe() error = %v, want it to wrap doctor.ErrDegraded", err)
	}
	if err == nil || !strings.Contains(err.Error(), string(runner.NetworkModeNone)) {
		t.Errorf("Probe() error = %v, want it to name %q", err, runner.NetworkModeNone)
	}
	if fake.RegistryProxyTransportCalls != 0 {
		t.Errorf("RegistryProxyTransportCalls = %d, want 0 -- NETWORK_MODE=none never needs the probe seam", fake.RegistryProxyTransportCalls)
	}
}

// Same as above but with a TCP-shaped probe answer, to show the verdict
// under NETWORK_MODE=none does not depend on the transport at all.
func TestSignalSocketTransportCheck_NetworkModeNoneWithTCPEndpointWrapsErrDegraded(t *testing.T) {
	fake := runner.NewFake()
	fake.RegistryProxyTransportEndpoint = registrymanifest.NewTCPEndpoint("", "")
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.signalCarrier = "socket"
	c.networkMode = runner.NetworkModeNone

	check := signalSocketTransportCheck(c)
	_, err := check.Probe()
	if !errors.Is(err, doctor.ErrDegraded) {
		t.Errorf("Probe() error = %v, want it to wrap doctor.ErrDegraded", err)
	}
	if err == nil || !strings.Contains(err.Error(), string(runner.NetworkModeNone)) {
		t.Errorf("Probe() error = %v, want it to name %q", err, runner.NetworkModeNone)
	}
	if fake.RegistryProxyTransportCalls != 0 {
		t.Errorf("RegistryProxyTransportCalls = %d, want 0 -- NETWORK_MODE=none never needs the probe seam", fake.RegistryProxyTransportCalls)
	}
}

// A zero Endpoint is an indeterminate probe answer, not a silently blank
// transport -- same reasoning as registryProxyTransportCheck's zero arm.
func TestSignalSocketTransportCheck_ZeroEndpointWrapsErrDegraded(t *testing.T) {
	fake := runner.NewFake()
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.signalCarrier = "socket"

	check := signalSocketTransportCheck(c)
	_, err := check.Probe()
	if !errors.Is(err, doctor.ErrDegraded) {
		t.Errorf("Probe() error = %v, want it to wrap doctor.ErrDegraded for a zero Endpoint", err)
	}
}

// A probe error itself is also indeterminate, same as the registry row.
func TestSignalSocketTransportCheck_ProbeErrorWrapsErrDegraded(t *testing.T) {
	fake := runner.NewFake()
	fake.RegistryProxyTransportErr = errors.New("boom")
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.signalCarrier = "socket"

	check := signalSocketTransportCheck(c)
	_, err := check.Probe()
	if !errors.Is(err, doctor.ErrDegraded) {
		t.Errorf("Probe() error = %v, want it to wrap doctor.ErrDegraded", err)
	}
}

// Under BOX_SIGNAL_CARRIER=log the row must be absent entirely (unlike the
// registry row, which always renders a "not configured" arm) and the shared
// probe seam must never be called for its sake.
func TestDoctorCheckSets_LogCarrierOmitsSignalSocketTransportRowWithoutProbing(t *testing.T) {
	fake := runner.NewFake()
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.signalCarrier = "log"

	classify, report := doctorCheckSets(c)
	for _, ch := range append(append([]doctor.Check{}, classify...), report...) {
		if ch.Name == signalSocketTransportCheckName {
			t.Fatalf("doctorCheckSets() under BOX_SIGNAL_CARRIER=log included %q, want it absent", signalSocketTransportCheckName)
		}
	}
	if fake.RegistryProxyTransportCalls != 0 {
		t.Errorf("RegistryProxyTransportCalls = %d, want 0 -- the log carrier must never probe the socket transport", fake.RegistryProxyTransportCalls)
	}
}

// Under BOX_SIGNAL_CARRIER=socket the row is present in the report half,
// never in classify -- it must never make validateConfig exit 2 (Advisory
// tier, issue #3728).
func TestDoctorCheckSets_SocketCarrierWiresSignalSocketTransportCheckToReportOnly(t *testing.T) {
	fake := runner.NewFake()
	fake.RegistryProxyTransportEndpoint = registrymanifest.NewUnixEndpoint("")
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.signalCarrier = "socket"

	classify, report := doctorCheckSets(c)
	checkByName(t, report, signalSocketTransportCheckName)

	for _, ch := range classify {
		if ch.Name == signalSocketTransportCheckName {
			t.Fatalf("doctorCheckSets() classify half included %q, want it absent (report-only row)", signalSocketTransportCheckName)
		}
	}
}

// The row degrades for two unrelated reasons -- a network mode that leaves
// the socket no transport, and a probe that answered nothing usable -- and
// ReportResults prints one Remedy for both, so it must name the runtime fix
// alongside the knob fix.
func TestSignalSocketTransportCheck_RemedyNamesBothTheKnobAndTheRuntimeFix(t *testing.T) {
	c := minimalValidConfig()
	c.signalCarrier = "socket"

	remedy := signalSocketTransportCheck(c).Remedy
	for _, want := range []string{"BOX_SIGNAL_CARRIER=log", "NETWORK_MODE", "indeterminate"} {
		if !strings.Contains(remedy, want) {
			t.Errorf("Remedy = %q, want it to mention %q", remedy, want)
		}
	}
}
