package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/registryroutes"
)

// Built through routeChecksFor because registryroutes.Parse already rejects a
// userinfo-bearing upstream-origin at parse time, so a real routes file cannot
// produce this Route. Unlike its two sibling branches, this branch omits raw
// from its message, so the error must name "userinfo" while carrying neither
// the password nor the raw URL it came from.
func TestRouteChecksFor_UpstreamOriginWithUserinfoFailsWithoutLeakingCredential(t *testing.T) {
	const raw = "https://user:s3cr3t@host.example.com"
	routes := []registryroutes.Route{{
		MatchHost:      "registry.example.com",
		UpstreamOrigin: raw,
	}}
	checks := routeChecksFor(routes)
	ch := checkByName(t, checks, "registry-route-origin[registry.example.com]")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() succeeded, want an error for a userinfo-bearing upstream-origin")
	}
	msg := err.Error()
	if !strings.Contains(msg, "userinfo") {
		t.Errorf("Probe() error %q must mention \"userinfo\"", msg)
	}
	if strings.Contains(msg, "s3cr3t") {
		t.Errorf("Probe() error %q must not contain the credential value", msg)
	}
	if strings.Contains(msg, raw) {
		t.Errorf("Probe() error %q must not contain the raw upstream-origin", msg)
	}
}

// Each route yields a credential row and an upstream row, and every row's Name
// comes from its own route's match host, not a shared generic name (issue #3144
// AC: "rows named by the route's match host").
func TestRouteChecksFor_ValidFileYieldsTwoRowsPerRouteNamedByMatchHost(t *testing.T) {
	const envVar = "SPINDRIFT_TEST_REGISTRY_ROUTE_DOCTOR_CHECKS_VALID"
	t.Setenv(envVar, "s3cr3t-value")

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry-a.example.com"
credential = { env = "`+envVar+`" }

[[routes]]
match-host = "registry-b.example.com"
credential = { env = "`+envVar+`" }
`)

	routes := mustLoadRoutes(t, c.registryProxyRoutesFile)
	checks := routeChecksFor(routes)
	if len(checks) != 4 {
		t.Fatalf("routeChecksFor() returned %d rows, want 4", len(checks))
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

// The failing row's error names the route's match host and the "credential"
// field, never the credential value, following the shape the existing
// registry-proxy-routes row already uses for its own per-route Peek.
func TestRouteChecksFor_UnresolvableCredentialFailsNamingRouteAndField(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DOCTOR_CHECKS_UNSET" }
`)

	routes := mustLoadRoutes(t, c.registryProxyRoutesFile)
	checks := routeChecksFor(routes)
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

// Built through routeChecksFor rather than a routes-file fixture because
// registryroutes.Parse already rejects a malformed upstream-origin at parse
// time, so a real file cannot produce this Route. See routeChecksFor's doc
// comment.
func TestRouteChecksFor_InvalidUpstreamOriginFailsNamingRouteAndField(t *testing.T) {
	routes := []registryroutes.Route{{
		MatchHost:      "registry.example.com",
		UpstreamOrigin: "not-a-url",
	}}
	checks := routeChecksFor(routes)
	ch := checkByName(t, checks, "registry-route-origin[registry.example.com]")
	_, err := ch.Probe()
	if err == nil {
		t.Fatal("Probe() succeeded, want an error for a malformed upstream-origin")
	}
	if !strings.Contains(err.Error(), "registry.example.com") {
		t.Errorf("Probe() error %q must name the route's match host", err.Error())
	}
	if !strings.Contains(err.Error(), "upstream-origin") {
		t.Errorf("Probe() error %q must name the \"upstream-origin\" field", err.Error())
	}
	if strings.Contains(err.Error(), "upstream-origin: upstream-origin") {
		t.Errorf("Probe() error %q must not stutter the \"upstream-origin\" field name (the validator's own message already names it)", err.Error())
	}
}

// A route serves the paths its derived path-set admits, never a declared base
// path (ADR 0047, issue #3261).
func TestRouteChecksFor_UpstreamOriginWithPathFails(t *testing.T) {
	routes := []registryroutes.Route{{
		MatchHost:      "registry.example.com",
		UpstreamOrigin: "https://registry.example.com/artifactory",
	}}
	checks := routeChecksFor(routes)
	ch := checkByName(t, checks, "registry-route-origin[registry.example.com]")
	if _, err := ch.Probe(); err == nil {
		t.Fatal("Probe() succeeded, want an error for an upstream-origin carrying a path")
	}
}

// The row reports the declared origin as its detail so an operator reading the
// report can see which origin the route will forward to.
func TestRouteChecksFor_DeclaredUpstreamOriginRowReportsIt(t *testing.T) {
	routes := []registryroutes.Route{{
		MatchHost:      "registry.example.com",
		UpstreamOrigin: "https://registry.example.com:8443",
	}}
	checks := routeChecksFor(routes)
	ch := checkByName(t, checks, "registry-route-origin[registry.example.com]")
	got, err := ch.Probe()
	if err != nil {
		t.Fatalf("Probe() unexpected error: %v", err)
	}
	if got != "https://registry.example.com:8443" {
		t.Errorf("Probe() = %v, want the declared origin", got)
	}
}

// Declaring no upstream-origin is the common case, so the row must pass and say
// so, rather than routeUpstreamCheck running ValidateUpstreamOrigin against ""
// and reporting a route Parse itself already accepted as broken.
func TestRouteChecksFor_DerivedOriginRowPasses(t *testing.T) {
	routes := []registryroutes.Route{{
		MatchHost: "registry.example.com",
	}}
	checks := routeChecksFor(routes)
	ch := checkByName(t, checks, "registry-route-origin[registry.example.com]")
	got, err := ch.Probe()
	if err != nil {
		t.Errorf("Probe() unexpected error for a route with no declared origin: %v", err)
	}
	if s, ok := got.(string); !ok || !strings.Contains(s, "derived") {
		t.Errorf("Probe() = %v, want a detail saying the origin is derived", got)
	}
}

// Issue #3144 slice 1: doctorCheckSets wires the per-route rows into the
// report half for a configured routes file.
func TestDoctorCheckSets_WiresPerRouteRows(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DOCTOR_CHECKS_WIRING" }
`)
	_, checks := doctorCheckSets(c)
	checkByName(t, checks, "registry-route-credential[registry.example.com]")
}

// Each Peek spawns the credential's subprocess, and a real exec credential can
// prompt a vault or biometric confirmation, so a full doctor run must peek once,
// not once for validateConfig's classification and again for runDoctor's report
// (issue #3144 review finding). The test drives doctorReport, the entry point
// cmdDoctor calls, because the report row set alone never runs in real use.
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
credential = { exec = ["/bin/sh", "-c", "echo x >> `+counterFile+`; echo tok"] }
`)

	var stdout, stderr bytes.Buffer
	doctorReport(doctorReadContext(c, f), &stdout, &stderr, strings.NewReader(""), false)

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

// Before the fix, an unresolvable route credential failed both the per-route
// registry-route-credential[<host>] row and the aggregate registry-proxy-routes
// row over the identical cause (issue #3144 review finding). Now only the
// per-route row fails, because the aggregate row no longer peeks credentials in
// the doctor report path.
func TestDoctorCheckSets_UnresolvableCredentialFailsOnlyPerRouteRow(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DOCTOR_CHECKS_DEDUP_UNSET" }
`)

	_, checks := doctorCheckSets(c)
	failing := []string{}
	for _, ch := range checks {
		if _, err := ch.Probe(); err != nil {
			failing = append(failing, ch.Name)
		}
	}
	if len(failing) != 1 || failing[0] != "registry-route-credential[registry.example.com]" {
		t.Errorf("doctorCheckSets(c) report failing rows = %v, want exactly [\"registry-route-credential[registry.example.com]\"]", failing)
	}

	routesCh := checkByName(t, checks, registryProxyRoutesCheckName)
	if _, err := routesCh.Probe(); err != nil {
		t.Errorf("registry-proxy-routes row Probe() unexpected error: %v (doctor report path must not re-peek route credentials)", err)
	}
}

// registryProxyRoutesCheck(c, true) is the variant launcherCrossKnobChecks
// wires into validate()'s launch gate (RunChecksFailFast) and validateConfig's
// exit-2 classification. Guards against the doctor-report-only substitution of
// peekCredentials = false leaking into those paths, which would silently let a
// launch proceed with a broken route credential (issue #3144 review finding).
func TestRegistryProxyRoutesCheck_PeekCredentialsTrueStillFailsOnUnresolvable(t *testing.T) {
	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry.example.com"
credential = { env = "SPINDRIFT_TEST_REGISTRY_ROUTE_DOCTOR_CHECKS_LAUNCH_GATE_UNSET" }
`)

	ch := registryProxyRoutesCheck(c, true)
	if _, err := ch.Probe(); err == nil {
		t.Fatal("Probe() succeeded, want an error: launch-gate variant must still peek route credentials")
	}
}

// Before this test, a route that omits the credential key (ADR 0045's
// unauthenticated pass-through) and one whose key resolves returned the
// identical "resolves" detail, so a routes file that lost its credential key to
// a typo looked no different from an intentional pass-through (review finding
// on issue #3145).
func TestRouteChecksFor_CredentialDetailDistinguishesPassThroughFromResolved(t *testing.T) {
	const envVar = "SPINDRIFT_TEST_REGISTRY_ROUTE_DOCTOR_CHECKS_PASSTHROUGH"
	t.Setenv(envVar, "s3cr3t-value")

	c := minimalValidConfig()
	c.registryProxyRoutesFile = writeRoutesFile(t, `
[[routes]]
match-host = "registry-passthrough.example.com"

[[routes]]
match-host = "registry-resolved.example.com"
credential = { env = "`+envVar+`" }
`)

	routes := mustLoadRoutes(t, c.registryProxyRoutesFile)
	checks := routeChecksFor(routes)

	passthroughCh := checkByName(t, checks, "registry-route-credential[registry-passthrough.example.com]")
	detail, err := passthroughCh.Probe()
	if err != nil {
		t.Fatalf("passthrough route Probe() unexpected error: %v", err)
	}
	if detail == "resolves" {
		t.Errorf("passthrough route Probe() detail = %q, want a detail distinct from the resolved-credential case", detail)
	}
	if !strings.Contains(fmt.Sprint(detail), "pass-through") {
		t.Errorf("passthrough route Probe() detail = %q, want it to name the unauthenticated pass-through case", detail)
	}

	resolvedCh := checkByName(t, checks, "registry-route-credential[registry-resolved.example.com]")
	detail, err = resolvedCh.Probe()
	if err != nil {
		t.Fatalf("resolved route Probe() unexpected error: %v", err)
	}
	if detail != "resolves" {
		t.Errorf("resolved route Probe() detail = %q, want unchanged %q", detail, "resolves")
	}
}

func checkNames(checks []doctor.Check) []string {
	names := make([]string, len(checks))
	for i, ch := range checks {
		names[i] = ch.Name
	}
	return names
}

// mustLoadRoutes parses file through the same loader doctorCheckSets uses, so
// these tests exercise the production parse rather than hand-built Routes.
func mustLoadRoutes(t *testing.T, file string) []registryroutes.Route {
	t.Helper()
	routes, err := loadRegistryRoutes(file)
	if err != nil {
		t.Fatalf("loadRegistryRoutes(%q) unexpected error: %v", file, err)
	}
	return routes
}
