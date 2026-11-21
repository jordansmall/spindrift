package main

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/runner"
)

// c.runtime names a binary that does not exist rather than
// minimalValidConfig()'s echo: echo always exits 0, so the real seam would also
// return a nil-error unix Endpoint and the assertion would pass whether or not
// TestMain's default is wired up.
func TestRegistryProxyTransportSeam_DefaultsToScriptedProbeUnderTest(t *testing.T) {
	c := minimalValidConfig()
	c.runtime = "spindrift-test-nonexistent-runtime-binary"

	endpoint, err := registryProxyTransportFn(c)
	if err != nil {
		t.Fatalf("registryProxyTransportFn() error = %v, want nil -- the package-default seam must never exec c.runtime", err)
	}
	if !endpoint.IsUnix() {
		t.Errorf("registryProxyTransportFn() = %v, want a unix Endpoint from the package-default scripted stub", endpoint)
	}
}

// TestInstallStopSignalSeam_DefaultsToNoOpUnderTest proves TestMain's stub is
// wired up: a nil Stop is the "no stop request is possible" case RunContinuous
// already handles, so a test that never asks for the seam gets no OS-level
// SIGTERM registration at all.
func TestInstallStopSignalSeam_DefaultsToNoOpUnderTest(t *testing.T) {
	ch, abortCh, cleanup := installStopSignal()
	defer cleanup()
	if ch != nil {
		t.Errorf("installStopSignal() channel = %v, want nil under TestMain's stub", ch)
	}
	if abortCh != nil {
		t.Errorf("installStopSignal() abort channel = %v, want nil under TestMain's stub", abortCh)
	}
}

func withRegistryProxyTransportFake(t *testing.T, fake *runner.Fake) {
	t.Helper()
	orig := registryProxyTransportFn
	registryProxyTransportFn = func(config) (registrymanifest.Endpoint, error) {
		endpoint, _, err := fake.RegistryProxyTransport()
		return endpoint, err
	}
	t.Cleanup(func() { registryProxyTransportFn = orig })
}

// The not-configured arm must not call the seam: a dispatch probes for the
// registry proxy only when routes are configured (`needRegistryProxy` in
// internal/dispatch/box.go), so this row must not start a container for a
// question no dispatch asks. A socket-carrier dispatch still probes with no
// routes; the signal-socket-transport row reports that.
func TestRegistryProxyTransportCheck_UnsetFileReportsNotConfiguredWithoutProbing(t *testing.T) {
	fake := runner.NewFake()
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.RegistryProxyRoutesFile = ""

	check := registryProxyTransportCheck(c)
	output, err := check.Probe()
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil", err)
	}
	if got := check.SuccessMsg(output); got != "registry-proxy-transport (not configured)" {
		t.Errorf("SuccessMsg(%v) = %q, want %q", output, got, "registry-proxy-transport (not configured)")
	}
	if fake.RegistryProxyTransportCalls != 0 {
		t.Errorf("RegistryProxyTransportCalls = %d, want 0 (not-configured arm must not probe)", fake.RegistryProxyTransportCalls)
	}
}

// The row reports whatever the prober answered, not a locally guessed default.
func TestRegistryProxyTransportCheck_UnixSocketReportsUnixTransport(t *testing.T) {
	fake := runner.NewFake()
	fake.RegistryProxyTransportEndpoint = registrymanifest.NewUnixEndpoint("")
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.RegistryProxyRoutesFile = "routes.toml"

	check := registryProxyTransportCheck(c)
	output, err := check.Probe()
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil", err)
	}
	if got := check.SuccessMsg(output); got != "registry-proxy-transport (unix socket)" {
		t.Errorf("SuccessMsg(%v) = %q, want %q", output, got, "registry-proxy-transport (unix socket)")
	}
	if fake.RegistryProxyTransportCalls != 1 {
		t.Errorf("RegistryProxyTransportCalls = %d, want 1", fake.RegistryProxyTransportCalls)
	}
}

// TCP is a passing outcome, not a failure (ADR 0044/0045): a runtime that cannot
// mount a socket but resolves the loopback host over TCP must report success,
// never render as a failing or MISSING row.
func TestRegistryProxyTransportCheck_TCPReportsTCPTransportWithoutError(t *testing.T) {
	fake := runner.NewFake()
	fake.RegistryProxyTransportEndpoint = registrymanifest.NewTCPEndpoint("", "")
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.RegistryProxyRoutesFile = "routes.toml"

	check := registryProxyTransportCheck(c)
	output, err := check.Probe()
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil -- TCP is a passing outcome, not a failure", err)
	}
	if got := check.SuccessMsg(output); got != "registry-proxy-transport (tcp)" {
		t.Errorf("SuccessMsg(%v) = %q, want %q", output, got, "registry-proxy-transport (tcp)")
	}
}

// A prober error is indeterminate (ErrDegraded), which Reporter.Results renders as
// "advisory:" rather than "MISSING:". The probe failed to determine an answer,
// it did not detect a broken transport.
func TestRegistryProxyTransportCheck_ProbeErrorWrapsErrDegraded(t *testing.T) {
	fake := runner.NewFake()
	fake.RegistryProxyTransportErr = errors.New("boom")
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.RegistryProxyRoutesFile = "routes.toml"

	check := registryProxyTransportCheck(c)
	_, err := check.Probe()
	if !errors.Is(err, doctor.ErrDegraded) {
		t.Errorf("Probe() error = %v, want it to wrap doctor.ErrDegraded", err)
	}
}

// An endpoint that is neither IsUnix() nor IsTCP() is an indeterminate probe
// answer, not a silently blank transport.
func TestRegistryProxyTransportCheck_ZeroEndpointWrapsErrDegraded(t *testing.T) {
	fake := runner.NewFake()
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.RegistryProxyRoutesFile = "routes.toml"

	check := registryProxyTransportCheck(c)
	_, err := check.Probe()
	if !errors.Is(err, doctor.ErrDegraded) {
		t.Errorf("Probe() error = %v, want it to wrap doctor.ErrDegraded for a zero Endpoint", err)
	}
}

// doctorCheckSets' report half appends the row unconditionally, unlike the
// per-route and drift rows: it carries its own not-configured arm, so
// `spindrift doctor` always shows one transport line (issue #3114). The
// classify-side exclusion is pinned in doctor_checks_test.go.
func TestDoctorCheckSets_WiresRegistryProxyTransportCheck(t *testing.T) {
	fake := runner.NewFake()
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.RegistryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_DOCTOR_CHECK_SETS_WIRES_TRANSPORT" }
`)
	_, checks := doctorCheckSets(c)
	checkByName(t, checks, registryProxyTransportCheckName)

	c = minimalValidConfig()
	c.RegistryProxyRoutesFile = ""
	_, checks = doctorCheckSets(c)
	unconfigured := checkByName(t, checks, registryProxyTransportCheckName)
	if _, err := unconfigured.Probe(); err != nil {
		t.Fatalf("Probe() error = %v, want nil", err)
	}
	if fake.RegistryProxyTransportCalls != 0 {
		t.Errorf("RegistryProxyTransportCalls = %d, want 0 -- the wired-up row's not-configured arm must not probe (doctor must start no container)", fake.RegistryProxyTransportCalls)
	}
}

// Both rows render adjacent in one report and share a probe, so they must
// share one runtime-readiness clause; the full strings are pinned so
// composing them from the constant cannot change a byte of operator output.
func TestRegistryProxyTransportCheck_RemedySharesRuntimeReachableClauseWithSignalSocketRow(t *testing.T) {
	c := minimalValidConfig()

	cases := []struct {
		name   string
		remedy string
		want   string
	}{
		{
			name:   signalSocketTransportCheckName,
			remedy: signalSocketTransportCheck(c).Remedy,
			want:   "pass --box-signal-carrier log, or use a NETWORK_MODE that permits loopback (not no-host-loopback or none); on an indeterminate probe instead, confirm the configured container runtime is running and reachable, then re-run `spindrift doctor`",
		},
		{
			name:   registryProxyTransportCheckName,
			remedy: registryProxyTransportCheck(c).Remedy,
			want:   "confirm the configured container runtime is running and reachable, then re-run `spindrift doctor` -- only an indeterminate probe needs action here, since both a unix socket and a TCP transport are working outcomes",
		},
	}
	for _, tc := range cases {
		if tc.remedy != tc.want {
			t.Errorf("%s Remedy = %q, want %q", tc.name, tc.remedy, tc.want)
		}
		if !strings.Contains(tc.remedy, runtimeReachableRemedy) {
			t.Errorf("%s Remedy = %q, want it to contain runtimeReachableRemedy", tc.name, tc.remedy)
		}
	}
}
