package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/doctor"
)

func useConnectorRows(t *testing.T, rows ...backendrows.Row) {
	t.Helper()
	t.Cleanup(backendrows.Snapshot())
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := backendrows.Use(raw); err != nil {
		t.Fatal(err)
	}
}

func connectorRow(accepted, live []connectorhost.Route) backendrows.Row {
	return backendrows.Row{Name: "fixture", Module: "/x/fixture.wasm", CredentialKnob: "fixtureToken", CredentialEnv: "FIXTURE_TOKEN", Routes: live, AcceptedRoutes: accepted}
}

var (
	fixtureGetRepos  = connectorhost.Route{Method: "GET", Host: "api.fixture.test", Path: "/repos"}
	fixturePostRepos = connectorhost.Route{Method: "POST", Host: "api.fixture.test", Path: "/repos"}
	fixtureGetOne    = connectorhost.Route{Method: "GET", Host: "api.fixture.test", Path: "/repos/x"}
)

func runConnectorRows(t *testing.T, checks []doctor.Check) string {
	t.Helper()
	var buf bytes.Buffer
	doctor.NewReporter(&buf, true).Results(doctor.RunChecks(checks))
	return buf.String()
}

func TestDoctorCheckSets_ConnectorRoutesRowsAreReportOnly(t *testing.T) {
	useConnectorRows(t, connectorRow([]connectorhost.Route{fixtureGetRepos}, []connectorhost.Route{fixtureGetRepos}))
	classify, report := doctorCheckSets(minimalValidConfig())
	for _, name := range []string{"connector-routes[fixture]", "connector-routes-widened[fixture]"} {
		if ch := checkByName(t, report, name); ch.Tier != doctor.Advisory {
			t.Errorf("%s Tier = %v, want Advisory", name, ch.Tier)
		}
		for _, ch := range classify {
			if ch.Name == name {
				t.Errorf("classify contains %q, want it report-only", name)
			}
		}
	}
}

func TestConnectorRoutesChecks_ListRoutesAndCredential(t *testing.T) {
	useConnectorRows(t, connectorRow([]connectorhost.Route{fixtureGetRepos}, []connectorhost.Route{fixtureGetRepos, fixturePostRepos}))
	out := runConnectorRows(t, connectorRoutesChecks())
	for _, want := range []string{"connector-routes[fixture]", "credential knob fixtureToken (env FIXTURE_TOKEN)", "GET api.fixture.test /repos", "POST api.fixture.test /repos"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestConnectorRoutesChecks_NoKnobFallsBackToEnv(t *testing.T) {
	row := connectorRow(nil, []connectorhost.Route{fixtureGetRepos})
	row.CredentialKnob = ""
	useConnectorRows(t, row)
	out := runConnectorRows(t, connectorRoutesChecks())
	if !strings.Contains(out, "credential FIXTURE_TOKEN;") {
		t.Errorf("output lacks the env-only credential:\n%s", out)
	}
}

func TestConnectorRoutesChecks_WideningFailsNamingAddedRoutes(t *testing.T) {
	useConnectorRows(t, connectorRow([]connectorhost.Route{fixtureGetRepos}, []connectorhost.Route{fixtureGetRepos, fixturePostRepos}))
	out := runConnectorRows(t, connectorRoutesChecks())
	for _, want := range []string{"connector-routes-widened[fixture]", "\n+ POST api.fixture.test /repos", "spindrift-connector-routes.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "+ GET api.fixture.test /repos") {
		t.Errorf("accepted route reported as added:\n%s", out)
	}
	for _, r := range doctor.RunChecks(connectorRoutesChecks()) {
		if doctor.Blocking(r) {
			t.Errorf("%s blocks; a widening must stay advisory", r.Check.Name)
		}
	}
}

func TestConnectorRoutesChecks_WithinAcceptedPasses(t *testing.T) {
	for name, tc := range map[string]struct{ accepted, live []connectorhost.Route }{
		"unchanged": {[]connectorhost.Route{fixtureGetRepos}, []connectorhost.Route{fixtureGetRepos}},
		"narrowed":  {[]connectorhost.Route{fixtureGetRepos}, []connectorhost.Route{fixtureGetOne}},
	} {
		t.Run(name, func(t *testing.T) {
			useConnectorRows(t, connectorRow(tc.accepted, tc.live))
			for _, r := range doctor.RunChecks(connectorRoutesChecks()) {
				if r.Err != nil {
					t.Errorf("%s failed: %v", r.Check.Name, r.Err)
				}
			}
			if out := runConnectorRows(t, connectorRoutesChecks()); !strings.Contains(out, "within the accepted") {
				t.Errorf("output lacks the accepted-table message:\n%s", out)
			}
		})
	}
}

func TestConnectorRoutesChecks_NoBaselineCountsEveryRouteAdded(t *testing.T) {
	useConnectorRows(t, connectorRow(nil, []connectorhost.Route{fixtureGetRepos}))
	out := runConnectorRows(t, connectorRoutesChecks())
	if !strings.Contains(out, "+ GET api.fixture.test /repos") {
		t.Errorf("output lacks the added route:\n%s", out)
	}
}

func TestConnectorRoutesChecks_NoRowsNoChecks(t *testing.T) {
	useConnectorRows(t)
	if got := connectorRoutesChecks(); len(got) != 0 {
		t.Errorf("connectorRoutesChecks() = %d rows, want none", len(got))
	}
}

// The baseline a Consumer commits beside its flake reaches doctor through the
// Nix-rendered input document: a Connector whose live table broadens the
// accepted one (a flake-lock bump that left the baseline alone) is flagged,
// while an equal or narrower live table is not.
func TestConnectorRoutesWidened_FromNixRenderedBaseline(t *testing.T) {
	dir := os.Getenv("SPINDRIFT_FIXTURE_CONNECTOR_BASELINE_DOCS")
	if dir == "" {
		t.Skip("SPINDRIFT_FIXTURE_CONNECTOR_BASELINE_DOCS is unset")
	}
	for _, tc := range []struct {
		doc      string
		widening bool
	}{{"widened", true}, {"accepted", false}, {"narrowed", false}} {
		t.Run(tc.doc, func(t *testing.T) {
			t.Cleanup(backendrows.Snapshot())
			raw, err := os.ReadFile(filepath.Join(dir, tc.doc+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var parsed struct {
				Backends json.RawMessage `json:"backends"`
			}
			if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Backends) == 0 {
				t.Fatalf("document carries no backends section (err=%v): %s", err, raw)
			}
			if err := backendrows.Use(parsed.Backends); err != nil {
				t.Fatal(err)
			}
			row, ok := backendrows.Lookup("fixture")
			if !ok {
				t.Fatal("backendrows.Lookup(fixture) = false after the document's rows applied")
			}
			_, err = connectorRoutesWidenedCheck(row).Probe()
			if tc.widening {
				if err == nil || !strings.Contains(err.Error(), "+ GET api.fixture.test /repos") {
					t.Fatalf("widened baseline: want an error naming %q, got %v", "+ GET api.fixture.test /repos", err)
				}
			} else if err != nil {
				t.Fatalf("%s baseline must not flag a widening, got %v", tc.doc, err)
			}
		})
	}
}
