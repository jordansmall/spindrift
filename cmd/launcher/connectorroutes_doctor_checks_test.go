package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/doctor"
)

func useConnectorRows(t *testing.T, rows ...backendrows.Row) {
	t.Helper()
	useConnectorRowsWith(t, func(string) string { return "" }, rows...)
}

// useConnectorRowsWith applies rows with lookup answering the knob a row's
// baseEnv names.
func useConnectorRowsWith(t *testing.T, lookup func(string) string, rows ...backendrows.Row) {
	t.Helper()
	t.Cleanup(backendrows.Snapshot())
	routeThroughConnector(t, "fixture")
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if err := backendrows.Use(raw, lookup); err != nil {
		t.Fatal(err)
	}
}

// routeThroughConnector registers a backend strategy per name that reads
// through its Connector, so doctor scopes its Route rows to it.
func routeThroughConnector(t *testing.T, names ...string) {
	t.Helper()
	var ss []backendkit.Strategy
	for _, name := range names {
		s := backendkit.Strategy{ConnectorRouted: true}
		s.Name = name
		ss = append(ss, s)
	}
	t.Cleanup(backendkit.ReplaceRegistryForTest(append(backendkit.Strategies(), ss...)))
}

// fixtureConfig runs the Code Forge and the issue tracker both through the
// "fixture" row.
func fixtureConfig() config {
	c := minimalValidConfig()
	c.CodeForge = "fixture"
	c.IssueTracker = "fixture"
	return c
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
	classify, report := doctorCheckSets(fixtureConfig())
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
	out := runConnectorRows(t, connectorRoutesChecks(fixtureConfig()))
	for _, want := range []string{"connector-routes[fixture]", "credential knob fixtureToken (env FIXTURE_TOKEN)", "GET api.fixture.test /repos", "POST api.fixture.test /repos"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestConnectorRoutesChecks_ConfigHostedRowListsTheResolvedHost(t *testing.T) {
	search := connectorhost.Route{Method: "GET", Host: backendrows.BaseHost, Path: "/rest/search"}
	issue := connectorhost.Route{Method: "POST", Host: backendrows.BaseHost, Path: "/rest/issue"}
	row := connectorRow([]connectorhost.Route{search}, []connectorhost.Route{search, issue})
	row.BaseEnv = "FIXTURE_BASE_URL"
	useConnectorRowsWith(t, func(k string) string {
		if k == "FIXTURE_BASE_URL" {
			return "https://acme.example.net/jira"
		}
		return ""
	}, row)
	out := runConnectorRows(t, connectorRoutesChecks(fixtureConfig()))
	for _, want := range []string{"GET acme.example.net /jira/rest/search", "\n+ POST acme.example.net /jira/rest/issue", "host from FIXTURE_BASE_URL"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, backendrows.BaseHost) {
		t.Errorf("output shows the unresolved placeholder:\n%s", out)
	}
	if strings.Contains(out, "+ GET") {
		t.Errorf("accepted route reported as added:\n%s", out)
	}
}

func TestConnectorRoutesChecks_NoKnobFallsBackToEnv(t *testing.T) {
	row := connectorRow(nil, []connectorhost.Route{fixtureGetRepos})
	row.CredentialKnob = ""
	useConnectorRows(t, row)
	out := runConnectorRows(t, connectorRoutesChecks(fixtureConfig()))
	if !strings.Contains(out, "credential FIXTURE_TOKEN;") {
		t.Errorf("output lacks the env-only credential:\n%s", out)
	}
}

func TestConnectorRoutesChecks_WideningFailsNamingAddedRoutes(t *testing.T) {
	useConnectorRows(t, connectorRow([]connectorhost.Route{fixtureGetRepos}, []connectorhost.Route{fixtureGetRepos, fixturePostRepos}))
	out := runConnectorRows(t, connectorRoutesChecks(fixtureConfig()))
	for _, want := range []string{"connector-routes-widened[fixture]", "\n+ POST api.fixture.test /repos", "spindrift-connector-routes.json", "spindrift connectors accept"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "+ GET api.fixture.test /repos") {
		t.Errorf("accepted route reported as added:\n%s", out)
	}
	for _, r := range doctor.RunChecks(connectorRoutesChecks(fixtureConfig())) {
		if doctor.Blocking(r) {
			t.Errorf("%s blocks; a widening must stay advisory", r.Check.Name)
		}
	}
}

func TestConnectorRoutesChecks_HeaderWideningNamesTheHeader(t *testing.T) {
	live := fixtureGetRepos
	live.RequestHeaders, live.ResponseHeaders = []string{"X-Extra"}, []string{"X-Seen"}
	useConnectorRows(t, connectorRow([]connectorhost.Route{fixtureGetRepos}, []connectorhost.Route{live}))
	out := runConnectorRows(t, connectorRoutesChecks(fixtureConfig()))
	for _, want := range []string{"connector-routes-widened[fixture]", "\n+ GET api.fixture.test /repos (sends X-Extra) (reads X-Seen)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestConnectorRoutesChecks_DocumentWideningNamesTheDocuments(t *testing.T) {
	post := fixtureGetRepos
	post.Method = "POST"
	accepted := post
	accepted.Documents = map[string]string{"A": "query { a }"}
	live := post
	live.Documents = map[string]string{"A": "query { a }", "B": "query { b }"}
	useConnectorRows(t, connectorRow([]connectorhost.Route{accepted}, []connectorhost.Route{live}))
	out := runConnectorRows(t, connectorRoutesChecks(fixtureConfig()))
	for _, want := range []string{"connector-routes-widened[fixture]", "\n+ POST api.fixture.test /repos (documents A@ddd02bba, B@115b9db7)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestConnectorRoutesChecks_ChangedDocumentTextIsAWideningWithDistinctLines(t *testing.T) {
	post := fixtureGetRepos
	post.Method = "POST"
	accepted := post
	accepted.Documents = map[string]string{"closeIssue": "mutation { close }"}
	live := post
	live.Documents = map[string]string{"closeIssue": "mutation { close(all: true) }"}
	if accepted.String() == live.String() {
		t.Fatalf("a changed document text renders the same line: %s", live)
	}
	useConnectorRows(t, connectorRow([]connectorhost.Route{accepted}, []connectorhost.Route{live}))
	out := runConnectorRows(t, connectorRoutesChecks(fixtureConfig()))
	for _, want := range []string{"connector-routes-widened[fixture]", "\n+ " + live.String()} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestConnectorRoutesChecks_JiraWriteRoutesRenderAsAdded(t *testing.T) {
	get := func(p string) connectorhost.Route {
		return connectorhost.Route{Method: "GET", Host: backendrows.BaseHost, Path: p}
	}
	put := connectorhost.Route{Method: "PUT", Host: backendrows.BaseHost, Path: "/rest/api/2/issue"}
	post := connectorhost.Route{Method: "POST", Host: backendrows.BaseHost, Path: "/rest/api/2/issue"}
	label := get("/rest/api/2/label")
	accepted := []connectorhost.Route{get("/rest/api/2/search")}
	row := connectorRow(accepted, append([]connectorhost.Route{accepted[0]}, put, post, label))
	row.BaseEnv = "FIXTURE_BASE_URL"
	useConnectorRowsWith(t, func(k string) string {
		if k == "FIXTURE_BASE_URL" {
			return "https://acme.example.net"
		}
		return ""
	}, row)
	out := runConnectorRows(t, connectorRoutesChecks(fixtureConfig()))
	for _, want := range []string{"\n+ PUT acme.example.net /rest/api/2/issue", "\n+ POST acme.example.net /rest/api/2/issue", "\n+ GET acme.example.net /rest/api/2/label"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
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
			for _, r := range doctor.RunChecks(connectorRoutesChecks(fixtureConfig())) {
				if r.Err != nil {
					t.Errorf("%s failed: %v", r.Check.Name, r.Err)
				}
			}
			if out := runConnectorRows(t, connectorRoutesChecks(fixtureConfig())); !strings.Contains(out, "within the accepted baseline") {
				t.Errorf("output lacks the accepted-table message:\n%s", out)
			}
		})
	}
}

func TestConnectorRoutesChecks_NoBaselineCountsEveryRouteAdded(t *testing.T) {
	useConnectorRows(t, connectorRow(nil, []connectorhost.Route{fixtureGetRepos}))
	out := runConnectorRows(t, connectorRoutesChecks(fixtureConfig()))
	if !strings.Contains(out, "+ GET api.fixture.test /repos") {
		t.Errorf("output lacks the added route:\n%s", out)
	}
}

func TestConnectorRoutesChecks_WidenedGrantNamesThePart(t *testing.T) {
	row := connectorRow([]connectorhost.Route{fixtureGetRepos}, []connectorhost.Route{fixtureGetRepos})
	row.CredentialEnv = "OTHER_TOKEN"
	row.AcceptedGrant = &backendrows.Grant{CredentialEnv: "FIXTURE_TOKEN"}
	useConnectorRows(t, row)
	out := runConnectorRows(t, connectorRoutesChecks(fixtureConfig()))
	for _, want := range []string{"connector-routes-widened[fixture]", "credential variable: OTHER_TOKEN (accepted: FIXTURE_TOKEN)"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "routes:\n") || strings.Contains(out, "\n+ ") {
		t.Errorf("no route was added, yet the output lists routes:\n%s", out)
	}
}

// Issue #7383: a run reads through the row its CODE_FORGE or ISSUE_TRACKER
// names, so a second row in the set gets no rows, and one row serving both
// gets them once.
func TestConnectorRoutesChecks_ScopedToTheRowTheRunReads(t *testing.T) {
	other := connectorRow(nil, []connectorhost.Route{fixtureGetRepos})
	other.Name = "other"
	useConnectorRows(t, connectorRow(nil, []connectorhost.Route{fixtureGetRepos}), other)
	routeThroughConnector(t, "other")
	var names []string
	for _, ch := range connectorRoutesChecks(fixtureConfig()) {
		names = append(names, ch.Name)
	}
	if got, want := strings.Join(names, ","), "connector-routes[fixture],connector-routes-widened[fixture]"; got != want {
		t.Errorf("checks = %s, want %s", got, want)
	}
	c := fixtureConfig()
	c.IssueTracker = "other"
	if got := len(connectorRoutesChecks(c)); got != 4 {
		t.Errorf("forge and tracker on two rows: %d checks, want 4", got)
	}
}

func TestConnectorRoutesChecks_UnroutedRunGetsNoChecks(t *testing.T) {
	useConnectorRows(t, connectorRow(nil, []connectorhost.Route{fixtureGetRepos}))
	c := fixtureConfig()
	c.CodeForge, c.IssueTracker = "git", "github"
	if got := connectorRoutesChecks(c); len(got) != 0 {
		t.Errorf("connectorRoutesChecks = %d rows, want none for a run reading no Connector row", len(got))
	}
}

func TestConnectorRoutesChecks_NoRowsNoChecks(t *testing.T) {
	useConnectorRows(t)
	if got := connectorRoutesChecks(fixtureConfig()); len(got) != 0 {
		t.Errorf("connectorRoutesChecks(fixtureConfig()) = %d rows, want none", len(got))
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
	}{{"widened", true}, {"accepted", false}, {"narrowed", false}, {"grant", false}, {"grant-remoteenv", false}} {
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
			if err := backendrows.Use(parsed.Backends, func(string) string { return "" }); err != nil {
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

// The github Connector's log read (issue #6893) is the one Route that opts in
// to a redirect, so a Consumer whose baseline predates it sees exactly that
// route as added, flagged as following one.
func TestConnectorRoutesChecks_RedirectingLogRouteShowsAsWidening(t *testing.T) {
	issues := connectorhost.Route{Method: "GET", Host: "api.github.com", Path: "/repos/o/r/issues"}
	graphql := connectorhost.Route{Method: "POST", Host: "api.github.com", Path: "/graphql"}
	logs := connectorhost.Route{Method: "GET", Host: "api.github.com", Path: "/repos/o/r/actions/jobs", FollowRedirect: true}
	useConnectorRows(t, connectorRow([]connectorhost.Route{issues, graphql}, []connectorhost.Route{issues, graphql, logs}))
	out := runConnectorRows(t, connectorRoutesChecks(fixtureConfig()))
	if want := "\n+ GET api.github.com /repos/o/r/actions/jobs (follows redirect)"; !strings.Contains(out, want) {
		t.Errorf("output lacks %q:\n%s", want, out)
	}
	if strings.Contains(out, "+ GET api.github.com /repos/o/r/issues") || strings.Contains(out, "+ POST") {
		t.Errorf("accepted routes reported as added:\n%s", out)
	}
}

// The github Connector's forge write half (issue #6882) widens a Consumer
// whose baseline holds only the four read routes: each new subtree root shows
// as added, and the accepted reads do not. The declared set is a hand copy of
// connectors/github/routes.nix, which a Go test cannot import.
func TestConnectorRoutesChecks_GithubForgeWritesShowAsWidening(t *testing.T) {
	route := func(method, path string) connectorhost.Route {
		return connectorhost.Route{Method: method, Host: "api.github.com", Path: path}
	}
	accepted := []connectorhost.Route{
		route("GET", "/repos/o/r/issues"),
		route("GET", "/repos/o/r/pulls"),
		route("POST", "/graphql"),
		{Method: "GET", Host: "api.github.com", Path: "/repos/o/r/actions/jobs", FollowRedirect: true},
	}
	added := []connectorhost.Route{
		route("POST", "/repos/o/r/pulls"),
		route("PUT", "/repos/o/r/pulls"),
		route("POST", "/repos/o/r/issues"),
		route("GET", "/repos/o/r/compare"),
		route("GET", "/repos/o/r/git/matching-refs/heads"),
		route("DELETE", "/repos/o/r/git/refs/heads"),
		route("GET", "/repos/o/r/branches"),
		route("GET", "/repos/o/r/rules/branches"),
		route("GET", "/installation/repositories"),
	}
	useConnectorRows(t, connectorRow(accepted, append(append([]connectorhost.Route{}, accepted...), added...)))
	out := runConnectorRows(t, connectorRoutesChecks(fixtureConfig()))
	for _, r := range added {
		if want := "\n+ " + r.String(); !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, r := range accepted {
		if strings.Contains(out, "+ "+r.String()) {
			t.Errorf("accepted route %s reported as added:\n%s", r, out)
		}
	}
}

// The github Connector's tracker write primitives (issue #6881) add two roots
// over a Consumer whose baseline holds the forge write half: GET and POST under
// /labels list and create the repository's labels. Label add, comment and issue
// filing ride the existing POST /issues root and label removal the existing
// POST /graphql root, so they add nothing. The declared set is a hand-picked
// subset of connectors/github/routes.nix, which a Go test cannot import.
func TestConnectorRoutesChecks_GithubTrackerWritesShowAsWidening(t *testing.T) {
	route := func(method, path string) connectorhost.Route {
		return connectorhost.Route{Method: method, Host: "api.github.com", Path: path}
	}
	accepted := []connectorhost.Route{
		route("GET", "/repos/o/r/issues"),
		route("POST", "/repos/o/r/issues"),
		route("POST", "/repos/o/r/pulls"),
		route("GET", "/repos/o/r/branches"),
	}
	added := []connectorhost.Route{
		route("GET", "/repos/o/r/labels"),
		route("POST", "/repos/o/r/labels"),
	}
	useConnectorRows(t, connectorRow(accepted, append(append([]connectorhost.Route{}, accepted...), added...)))
	out := runConnectorRows(t, connectorRoutesChecks(fixtureConfig()))
	for _, r := range added {
		if want := "\n+ " + r.String(); !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, r := range accepted {
		if strings.Contains(out, "+ "+r.String()) {
			t.Errorf("accepted route %s reported as added:\n%s", r, out)
		}
	}
}
