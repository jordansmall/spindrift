package main

import (
	"errors"
	"os"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/registrymanifest"
	"spindrift.dev/launcher/internal/runner"
)

// TestMain installs a scripted default for registryProxyTransportFn before
// any test in package main runs. Any test driving doctorReport or
// doctorReportChecks end-to-end over a configured registryProxyRoutesFile
// (registryroutes_doctor_checks_test.go, bwrap_doctor_checks_test.go) probes
// the registry-proxy-transport row too, as a side effect of exercising the
// row set as a whole. Left at its production default the seam binds a unix
// listener and execs c.runtime, and doctor tests must start no container
// (issue #3114), so the safe answer has to be the package-wide default
// rather than something each unrelated test must know to ask for.
//
// Tests exercising the row's own behaviour override this default via
// withRegistryProxyTransportFake.
func TestMain(m *testing.M) {
	registryProxyTransportFn = func(config) (registrymanifest.Endpoint, error) {
		return registrymanifest.NewUnixEndpoint(""), nil
	}
	os.Exit(m.Run())
}

// TestRegistryProxyTransportSeam_DefaultsToScriptedProbeUnderTest verifies
// TestMain's package-wide default holds when no test-local override is in
// scope -- the state every other doctor test in this package probes the row
// under.
//
// c.runtime names a binary that does not exist rather than
// minimalValidConfig()'s echo: echo always exits 0, so the real seam returns
// a nil-error unix Endpoint for it too and the assertion would pass whether
// or not the default is wired up. A nonexistent binary diverges the two
// paths -- the real seam's exec fails, the scripted default never inspects
// c.runtime at all.
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

// withRegistryProxyTransportFake points registryProxyTransportFn at fake's
// RegistryProxyTransport for the duration of the test, restoring the
// original seam afterward -- the same save/t.Cleanup-restore shape
// withDriftRepoDir (registryroutesdrift_doctor_checks_test.go) uses.
func withRegistryProxyTransportFake(t *testing.T, fake *runner.Fake) {
	t.Helper()
	orig := registryProxyTransportFn
	registryProxyTransportFn = func(config) (registrymanifest.Endpoint, error) {
		endpoint, _, err := fake.RegistryProxyTransport()
		return endpoint, err
	}
	t.Cleanup(func() { registryProxyTransportFn = orig })
}

// TestRegistryProxyTransportCheck_UnsetFileReportsNotConfiguredWithoutProbing
// verifies the not-configured arm (c.registryProxyRoutesFile == "") returns
// "not configured" with no error, and -- the AC this row exists to satisfy
// -- never calls the seam at all: a dispatch never probes the transport
// either when no registry proxy is configured (dispatch/box.go's own
// `if len(d.cfg.RegistryProxyRoutes) > 0` gate), so doctor must mirror that
// exactly rather than starting a container to answer a question a dispatch
// would never ask.
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

// TestRegistryProxyTransportCheck_UnixSocketReportsUnixTransport verifies
// the configured arm reports "unix socket" and no error when the seam's
// scripted endpoint is a unix Endpoint -- the row's report comes straight
// from the prober's answer, not a locally-guessed default.
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

// TestRegistryProxyTransportCheck_TCPReportsTCPTransportWithoutError
// verifies TCP is a passing outcome, not a failure (ADR 0044/0045): a
// runtime that can't mount a socket but resolves the loopback host over TCP
// must report success, never render as a failing/MISSING row.
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

// TestRegistryProxyTransportCheck_ProbeErrorWrapsErrDegraded verifies a
// prober error is reported as an indeterminate result (ErrDegraded), which
// ReportResults renders as "advisory:" rather than "MISSING:" -- the probe
// failed to determine an answer, it did not affirmatively detect a broken
// transport.
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

// TestRegistryProxyTransportCheck_ZeroEndpointWrapsErrDegraded verifies an
// endpoint that is neither IsUnix() nor IsTCP() (the zero Endpoint) is
// treated as an indeterminate probe answer, not silently reported as a
// blank transport.
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

// TestDoctorReportChecks_WiresRegistryProxyTransportCheck verifies
// doctorReportChecks appends registryProxyTransportCheck(c)'s row
// unconditionally -- present both when c.registryProxyRoutesFile is set and
// when it's unset, unlike the per-route and drift rows (issue #3114: the row
// itself carries the not-configured arm, so `spindrift doctor` always shows
// one transport line, mirroring registry-proxy-routes). The classify-side
// exclusion (the row is Advisory, not a configuration fault) already has a
// home in TestDoctorCheckSets_ClassifyExcludesBwrapAndDriftRowsButIncludesPerRouteRows
// (bwrap_doctor_checks_test.go); this test does not duplicate it.
func TestDoctorReportChecks_WiresRegistryProxyTransportCheck(t *testing.T) {
	fake := runner.NewFake()
	withRegistryProxyTransportFake(t, fake)

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_DOCTOR_REPORT_CHECKS_WIRES_TRANSPORT" }
`)
	checkByName(t, doctorReportChecks(c), registryProxyTransportCheckName)

	c = minimalValidConfig()
	c.registryProxyRoutesFile = ""
	unconfigured := checkByName(t, doctorReportChecks(c), registryProxyTransportCheckName)
	if _, err := unconfigured.Probe(); err != nil {
		t.Fatalf("Probe() error = %v, want nil", err)
	}
	if fake.RegistryProxyTransportCalls != 0 {
		t.Errorf("RegistryProxyTransportCalls = %d, want 0 -- the wired-up row's not-configured arm must not probe (doctor must start no container)", fake.RegistryProxyTransportCalls)
	}
}
