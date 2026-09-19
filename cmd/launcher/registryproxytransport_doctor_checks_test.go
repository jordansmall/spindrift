package main

import (
	"errors"
	"os"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/runner"
)

// TestMain scripts registryProxyTransportFn for the whole package. Left at its
// production default the seam binds a unix listener and execs c.runtime, and
// doctor tests must start no container (issue #3114), so the stub has to be a
// package-wide default, not something each unrelated test asks for. Tests for
// the row's own behaviour override it with withRegistryProxyTransportFake.
func TestMain(m *testing.M) {
	registryProxyTransportFn = func(config) (registrymanifest.Endpoint, error) {
		return registrymanifest.NewUnixEndpoint(""), nil
	}
	os.Exit(m.Run())
}

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

func withRegistryProxyTransportFake(t *testing.T, fake *runner.Fake) {
	t.Helper()
	orig := registryProxyTransportFn
	registryProxyTransportFn = func(config) (registrymanifest.Endpoint, error) {
		endpoint, _, err := fake.RegistryProxyTransport()
		return endpoint, err
	}
	t.Cleanup(func() { registryProxyTransportFn = orig })
}

// The not-configured arm must not call the seam at all. A dispatch only probes
// the transport when a registry proxy is configured (dispatch/box.go's
// `if len(d.cfg.RegistryProxyRoutes) > 0` gate), so doctor must not start a
// container to answer a question a dispatch would never ask.
func TestRegistryProxyTransportCheck_UnsetFileReportsNotConfiguredWithoutProbing(t *testing.T) {
	fake := runner.NewFake()
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = ""

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
	c.registryProxyRoutesFile = "routes.toml"

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
	c.registryProxyRoutesFile = "routes.toml"

	check := registryProxyTransportCheck(c)
	output, err := check.Probe()
	if err != nil {
		t.Fatalf("Probe() error = %v, want nil -- TCP is a passing outcome, not a failure", err)
	}
	if got := check.SuccessMsg(output); got != "registry-proxy-transport (tcp)" {
		t.Errorf("SuccessMsg(%v) = %q, want %q", output, got, "registry-proxy-transport (tcp)")
	}
}

// A prober error is indeterminate (ErrDegraded), which ReportResults renders as
// "advisory:" rather than "MISSING:". The probe failed to determine an answer,
// it did not detect a broken transport.
func TestRegistryProxyTransportCheck_ProbeErrorWrapsErrDegraded(t *testing.T) {
	fake := runner.NewFake()
	fake.RegistryProxyTransportErr = errors.New("boom")
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = "routes.toml"

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
	c.registryProxyRoutesFile = "routes.toml"

	check := registryProxyTransportCheck(c)
	_, err := check.Probe()
	if !errors.Is(err, doctor.ErrDegraded) {
		t.Errorf("Probe() error = %v, want it to wrap doctor.ErrDegraded for a zero Endpoint", err)
	}
}

// doctorCheckSets' report half appends the row unconditionally, unlike the
// per-route and drift rows: the row carries its own not-configured arm, so
// `spindrift doctor` always shows one transport line (issue #3114). The
// classify-side exclusion lives in
// TestDoctorCheckSets_ClassifyExcludesBwrapAndDriftRowsButIncludesPerRouteRows
// (bwrap_doctor_checks_test.go), so this test does not duplicate it.
func TestDoctorCheckSets_WiresRegistryProxyTransportCheck(t *testing.T) {
	fake := runner.NewFake()
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_DOCTOR_CHECK_SETS_WIRES_TRANSPORT" }
`)
	_, checks := doctorCheckSets(c)
	checkByName(t, checks, registryProxyTransportCheckName)

	c = minimalValidConfig()
	c.registryProxyRoutesFile = ""
	_, checks = doctorCheckSets(c)
	unconfigured := checkByName(t, checks, registryProxyTransportCheckName)
	if _, err := unconfigured.Probe(); err != nil {
		t.Fatalf("Probe() error = %v, want nil", err)
	}
	if fake.RegistryProxyTransportCalls != 0 {
		t.Errorf("RegistryProxyTransportCalls = %d, want 0 -- the wired-up row's not-configured arm must not probe (doctor must start no container)", fake.RegistryProxyTransportCalls)
	}
}
