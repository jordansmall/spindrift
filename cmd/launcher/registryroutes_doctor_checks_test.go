package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/registryroutes"
)

// TestRegistryRouteChecks_UnreadableRoutesFileReturnsNil verifies
// registryRouteChecks returns nil when c.registryProxyRoutesFile points at a
// file that can't be read -- deferring to the existing registry-proxy-routes
// row (checks.go), which already reports this same read failure, rather
// than this check surfacing a duplicate error over the identical cause.
func TestRegistryRouteChecks_UnreadableRoutesFileReturnsNil(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = filepath.Join(t.TempDir(), "does-not-exist.toml")

	if got := registryRouteChecks(c); got != nil {
		t.Errorf("registryRouteChecks() = %#v, want nil for an unreadable routes file", got)
	}
}

// TestRegistryRouteChecks_UnparsableRoutesFileReturnsNil verifies
// registryRouteChecks returns nil when c.registryProxyRoutesFile contains
// invalid TOML -- deferring to the existing registry-proxy-routes row
// (checks.go), which already reports this same parse failure, rather than
// this check surfacing a duplicate error over the identical cause.
func TestRegistryRouteChecks_UnparsableRoutesFileReturnsNil(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `not valid toml [[[`)

	if got := registryRouteChecks(c); got != nil {
		t.Errorf("registryRouteChecks() = %#v, want nil for an unparsable routes file", got)
	}
}

// TestRegistryRouteChecks_UpstreamURLWithUserinfoFailsWithoutLeakingCredential
// verifies registryroutes.ValidateUpstreamBaseURL's userinfo branch, reached
// through routeChecksFor since registryroutes.Parse itself already rejects a
// userinfo-bearing upstream-base-url at parse time (normalizeUpstreamBaseURL),
// making this shape of Route unreachable from a real routes file -- see
// routeChecksFor's doc comment. Pins the AC that a failing row names the
// route and the field but never a credential value: unlike its two sibling
// branches, this branch deliberately omits raw from its message, so the
// error must mention "userinfo" while containing neither the userinfo
// password nor the raw URL it came from.
func TestRegistryRouteChecks_UpstreamURLWithUserinfoFailsWithoutLeakingCredential(t *testing.T) {
	const raw = "https://user:s3cr3t@host.example.com"
	routes := []registryroutes.Route{{
		MatchHost:       "registry.example.com",
		UpstreamBaseURL: raw,
	}}
	checks := routeChecksFor(routes)
	ch := checkByName(t, checks, "registry-route-upstream[registry.example.com]")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() succeeded, want an error for a userinfo-bearing upstream-base-url")
	}
	msg := err.Error()
	if !strings.Contains(msg, "userinfo") {
		t.Errorf("Probe() error %q must mention \"userinfo\"", msg)
	}
	if strings.Contains(msg, "s3cr3t") {
		t.Errorf("Probe() error %q must not contain the credential value", msg)
	}
	if strings.Contains(msg, raw) {
		t.Errorf("Probe() error %q must not contain the raw upstream-base-url", msg)
	}
}

// TestRegistryRouteChecks_UnsetFileReturnsNil verifies registryRouteChecks
// returns nil -- not an empty non-nil slice, and no rows at all -- when
// c.registryProxyRoutesFile is unset (issue #3144 slice 1 gate): the
// per-route rows are opt-in alongside the routes file, never emitted for a
// Consumer still on the scalar REGISTRY_PROXY_* knobs.
func TestRegistryRouteChecks_UnsetFileReturnsNil(t *testing.T) {
	c := minimalValidConfig()
	if got := registryRouteChecks(c); got != nil {
		t.Errorf("registryRouteChecks() = %#v, want nil when registryProxyRoutesFile is unset", got)
	}
}

// TestRegistryRouteChecks_ValidFileYieldsTwoRowsPerRouteNamedByMatchHost
// verifies a well-formed two-route file yields exactly four rows -- a
// credential row and an upstream row per route -- and that every row's Name
// is derived from its own route's match host, not a shared/generic name
// (issue #3144 AC: "rows named by the route's match host").
func TestRegistryRouteChecks_ValidFileYieldsTwoRowsPerRouteNamedByMatchHost(t *testing.T) {
	const envVar = "SPINDRIFT_TEST_REGISTRY_ROUTE_DOCTOR_CHECKS_VALID"
	t.Setenv(envVar, "s3cr3t-value")

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry-a.example.com"
upstream-base-url = "https://registry-a.example.com"
credential = { env = "`+envVar+`" }

[[routes]]
match-host = "registry-b.example.com"
upstream-base-url = "https://registry-b.example.com"
credential = { env = "`+envVar+`" }
`)

	checks := registryRouteChecks(c)
	if len(checks) != 4 {
		t.Fatalf("registryRouteChecks() returned %d rows, want 4", len(checks))
	}
	for _, host := range []string{"registry-a.example.com", "registry-b.example.com"} {
		found := false
		for _, ch := range checks {
			if strings.Contains(ch.Name, host) {
				found = true
			}
		}
		if !found {
			t.Errorf("no row named after match host %q; rows: %v", host, checkNames(checks))
		}
	}
	for _, ch := range checks {
		if _, err := ch.Probe(); err != nil {
			t.Errorf("row %q Probe() unexpected error: %v", ch.Name, err)
		}
	}
}

// TestRegistryRouteChecks_UnresolvableCredentialFailsNamingRouteAndField
// verifies a route whose credential env var is unset produces a failing
// credential row -- Probe's error names the route's match host and the
// "credential" field, never the (in this case nonexistent) credential
// value, following the fails-and-passes shape the existing
// registry-proxy-routes row already uses for its own per-route Peek.
func TestRegistryRouteChecks_UnresolvableCredentialFailsNamingRouteAndField(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
upstream-base-url = "https://registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DOCTOR_CHECKS_UNSET" }
`)

	checks := registryRouteChecks(c)
	ch := checkByName(t, checks, "registry-route-credential[registry.example.com]")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() succeeded, want an error for an unresolvable credential")
	}
	if !strings.Contains(err.Error(), "registry.example.com") {
		t.Errorf("Probe() error %q must name the route's match host", err.Error())
	}
	if !strings.Contains(err.Error(), "credential") {
		t.Errorf("Probe() error %q must name the \"credential\" field", err.Error())
	}
}

// TestRegistryRouteChecks_InvalidUpstreamBaseURLFailsNamingRouteAndField
// verifies a route with a malformed upstream-base-url produces a failing
// upstream row naming the route's match host and the "upstream-base-url"
// field. Built directly via routeChecksFor rather than a routes-file
// fixture: registryroutes.Parse itself already rejects a malformed
// upstream-base-url at parse time (normalizeUpstreamBaseURL), so this shape
// of Route is unreachable from a real file -- see routeChecksFor's doc
// comment.
func TestRegistryRouteChecks_InvalidUpstreamBaseURLFailsNamingRouteAndField(t *testing.T) {
	routes := []registryroutes.Route{{
		MatchHost:       "registry.example.com",
		UpstreamBaseURL: "not-a-url",
	}}
	checks := routeChecksFor(routes)
	ch := checkByName(t, checks, "registry-route-upstream[registry.example.com]")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() succeeded, want an error for a malformed upstream-base-url")
	}
	if !strings.Contains(err.Error(), "registry.example.com") {
		t.Errorf("Probe() error %q must name the route's match host", err.Error())
	}
	if !strings.Contains(err.Error(), "upstream-base-url") {
		t.Errorf("Probe() error %q must name the \"upstream-base-url\" field", err.Error())
	}
	if strings.Contains(err.Error(), "upstream-base-url: upstream-base-url") {
		t.Errorf("Probe() error %q must not stutter the \"upstream-base-url\" field name (the validator's own message already names it)", err.Error())
	}
}

// TestDoctorReportChecks_WiresRegistryRouteChecks verifies doctorReportChecks
// appends registryRouteChecks(c)'s rows: present when
// c.registryProxyRoutesFile is set, absent when it's unset (issue #3144
// slice 1 -- "no routes rows, no new output at all" when the routes file
// isn't configured).
func TestDoctorReportChecks_WiresRegistryRouteChecks(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
upstream-base-url = "https://registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DOCTOR_CHECKS_WIRING" }
`)
	checkByName(t, doctorReportChecks(c), "registry-route-credential[registry.example.com]")

	c = minimalValidConfig()
	for _, ch := range doctorReportChecks(c) {
		if strings.HasPrefix(ch.Name, "registry-route-") {
			t.Errorf("doctorReportChecks output contains %q when registryProxyRoutesFile is unset", ch.Name)
		}
	}
}

// TestDoctorReport_ExecCredentialPeekedOncePerInvocation verifies that a full
// `spindrift doctor` invocation (doctorReport) peeks an exec-sourced route
// credential exactly once -- not once for validateConfig's classification
// pass and again for runDoctor's report pass -- since each Peek spawns the
// credential's subprocess, and a real exec credential may prompt a vault or
// biometric confirmation per invocation (issue #3144 review finding).
// doctorReport is the real entry point cmdDoctor calls; the deleted
// doctorReportChecks-only predecessor of this test exercised the report row
// set in isolation, a set the real command never runs alone, and so had
// already certified the double-Peek defect this test guards as fixed.
func TestDoctorReport_ExecCredentialPeekedOncePerInvocation(t *testing.T) {
	counterFile := filepath.Join(t.TempDir(), "counter")

	f := forge.NewFake()
	f.ProbeRepo = "owner/repo"
	f.Labels = []string{"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"}

	c := minimalValidConfig()
	c.label, c.inProgressLabel, c.failedLabel, c.completeLabel =
		"ready-for-agent", "agent-in-progress", "agent-failed", "agent-complete"
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
upstream-base-url = "https://registry.example.com"
credential = { exec = ["/bin/sh", "-c", "echo x >> `+counterFile+`; echo tok"] }
`)

	var stdout, stderr bytes.Buffer
	doctorReport(f, f, c, &stdout, &stderr, strings.NewReader(""), false)

	data, err := os.ReadFile(counterFile)
	if err != nil {
		t.Fatalf("reading counter file: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 || lines[0] == "" {
		if len(lines) == 1 && lines[0] == "" {
			t.Fatalf("exec credential command never ran; want exactly 1 invocation")
		}
		t.Errorf("exec credential command ran %d times, want exactly 1 (PEEK_INVOCATIONS), stdout=%q stderr=%q", len(lines), stdout.String(), stderr.String())
	}
}

// TestDoctorReportChecks_UnresolvableCredentialFailsOnlyPerRouteRow verifies
// that when a route's credential can't resolve, doctorReportChecks(c)
// reports exactly one failing row for that cause -- the per-route
// registry-route-credential[<host>] row -- while the aggregate
// registry-proxy-routes row (which no longer peeks credentials in the
// doctor report path) still passes. Before the fix both rows failed over
// the identical cause (issue #3144 review finding).
func TestDoctorReportChecks_UnresolvableCredentialFailsOnlyPerRouteRow(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
upstream-base-url = "https://registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DOCTOR_CHECKS_DEDUP_UNSET" }
`)

	checks := doctorReportChecks(c)
	failing := []string{}
	for _, ch := range checks {
		if _, err := ch.Probe(); err != nil {
			failing = append(failing, ch.Name)
		}
	}
	if len(failing) != 1 || failing[0] != "registry-route-credential[registry.example.com]" {
		t.Errorf("doctorReportChecks(c) failing rows = %v, want exactly [\"registry-route-credential[registry.example.com]\"]", failing)
	}

	routesCh := checkByName(t, checks, registryProxyRoutesCheckName)
	if _, err := routesCh.Probe(); err != nil {
		t.Errorf("registry-proxy-routes row Probe() unexpected error: %v (doctor report path must not re-peek route credentials)", err)
	}
}

// TestRegistryProxyRoutesCheck_PeekCredentialsTrueStillFailsOnUnresolvable
// verifies registryProxyRoutesCheck(c, true) -- the variant
// launcherCrossKnobChecks wires into validate()'s launch-gate
// (RunChecksFailFast) and validateConfig's exit-2 classification -- still
// fails on an unresolvable route credential. Guards against the
// doctor-report-only substitution (peekCredentials = false) leaking into
// those paths, which would silently let a launch proceed with a broken
// route credential (issue #3144 review finding).
func TestRegistryProxyRoutesCheck_PeekCredentialsTrueStillFailsOnUnresolvable(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
upstream-base-url = "https://registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DOCTOR_CHECKS_LAUNCH_GATE_UNSET" }
`)

	ch := registryProxyRoutesCheck(c, true)
	if _, err := ch.Probe(); err == nil {
		t.Fatal("Probe() succeeded, want an error: launch-gate variant must still peek route credentials")
	}
}

func checkNames(checks []doctor.Check) []string {
	names := make([]string, len(checks))
	for i, ch := range checks {
		names[i] = ch.Name
	}
	return names
}
