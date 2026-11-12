package backendrows_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
)

const fixturesEnv = "SPINDRIFT_CONNECTOR_FIXTURES_DIR"

func isolate(t *testing.T) {
	t.Helper()
	backendrows.Reset()
	t.Cleanup(backendrows.Reset)
}

func rows(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func row(name string, routes ...connectorhost.Route) backendrows.Row {
	return backendrows.Row{Name: name, Module: "/x/" + name + ".wasm", Routes: routes, CredentialEnv: "TOKEN"}
}

var getRepos = connectorhost.Route{Method: "GET", Host: "api.fixture.test", Path: "/repos"}

func TestUseRoundTripsRowsThroughLookup(t *testing.T) {
	isolate(t)
	raw := json.RawMessage(`[{"name":"fixture","module":"/nix/store/x-network.wasm",` +
		`"routes":[{"method":"GET","host":"api.fixture.test","path":"/repos"}],"credentialEnv":"FIXTURE_TOKEN"}]`)
	if err := backendrows.Use(raw); err != nil {
		t.Fatal(err)
	}
	got, ok := backendrows.Lookup("fixture")
	if !ok {
		t.Fatal("Lookup(fixture) = false")
	}
	want := backendrows.Row{Name: "fixture", Module: "/nix/store/x-network.wasm", Routes: []connectorhost.Route{getRepos}, CredentialEnv: "FIXTURE_TOKEN"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Lookup = %+v, want %+v", got, want)
	}
	if _, ok := backendrows.Lookup("other"); ok {
		t.Error("Lookup(other) = true, want false")
	}
}

func TestUseWithoutRowsClearsTheEarlierRows(t *testing.T) {
	for name, raw := range map[string]json.RawMessage{"nil": nil, "empty": {}, "whitespace": []byte("  \n"), "empty array": []byte("[]")} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			if err := backendrows.Use(rows(t, []backendrows.Row{row("x", getRepos)})); err != nil {
				t.Fatal(err)
			}
			if err := backendrows.Use(raw); err != nil {
				t.Fatal(err)
			}
			if _, ok := backendrows.Lookup("x"); ok {
				t.Error("Lookup(x) = true after a document with no rows")
			}
		})
	}
}

func TestUseRefusesInvalidRowsNamingTheBackend(t *testing.T) {
	cases := map[string]struct {
		raw       json.RawMessage
		want      []string
		routeless bool
	}{
		"empty name":    {rows(t, []backendrows.Row{row("", getRepos)}), []string{"no name"}, true},
		"repeated name": {rows(t, []backendrows.Row{row("dup", getRepos), row("dup", getRepos)}), []string{"dup", "more than one row"}, true},
		"empty module":  {rows(t, []backendrows.Row{{Name: "nomod", Routes: []connectorhost.Route{getRepos}, CredentialEnv: "T"}}), []string{"nomod", "module"}, true},
		"empty cred":    {rows(t, []backendrows.Row{{Name: "nocred", Module: "/m.wasm", Routes: []connectorhost.Route{getRepos}}}), []string{"nocred", "credentialEnv"}, true},
		"no routes":     {rows(t, []backendrows.Row{{Name: "noroutes", Module: "/m.wasm", CredentialEnv: "T"}}), []string{"noroutes", "route"}, true},
		"core local":    {rows(t, []backendrows.Row{row("local", getRepos)}), []string{"local", "built in"}, true},
		"core git":      {rows(t, []backendrows.Row{row("git", getRepos)}), []string{"git", "built in"}, true},
		"unknown key":   {json.RawMessage(`[{"name":"x","bogus":1}]`), []string{"backend rows", "bogus"}, true},
		"unknown route": {json.RawMessage(`[{"name":"x","routes":[{"method":"GET","host":"h","path":"/","bogus":1}]}]`), []string{"bogus"}, true},
		"not an array":  {json.RawMessage(`"nope"`), []string{"backend rows"}, true},
		"root path":     {rows(t, []backendrows.Row{row("broad", connectorhost.Route{Method: "GET", Host: "h.test", Path: "/"})}), []string{"broad", "GET h.test /"}, false},
		"non-canonical": {rows(t, []backendrows.Row{row("messy", connectorhost.Route{Method: "GET", Host: "h.test", Path: "/a/"})}), []string{"messy", "GET h.test /a/"}, false},
		"relative":      {rows(t, []backendrows.Row{row("rel", connectorhost.Route{Method: "GET", Host: "h.test", Path: "a"})}), []string{"rel", "GET h.test a"}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			err := backendrows.Use(tc.raw)
			if err == nil {
				t.Fatal("Use() = nil, want an error")
			}
			if !strings.HasPrefix(err.Error(), "backend rows: ") {
				t.Errorf("err = %q, want the backend rows prefix", err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("err = %q, want it to contain %q", err, w)
				}
			}
			if !tc.routeless && !errors.Is(err, connectorhost.ErrRouteTable) {
				t.Errorf("err = %v, want ErrRouteTable", err)
			}
		})
	}
}

func TestUseReplacesTheEarlierRows(t *testing.T) {
	isolate(t)
	if err := backendrows.Use(rows(t, []backendrows.Row{row("x", getRepos), row("y", getRepos)})); err != nil {
		t.Fatal(err)
	}
	if err := backendrows.Use(rows(t, []backendrows.Row{row("x", getRepos)})); err != nil {
		t.Fatal(err)
	}
	if _, ok := backendrows.Lookup("x"); !ok {
		t.Error("Lookup(x) = false after the second document kept it")
	}
	if _, ok := backendrows.Lookup("y"); ok {
		t.Error("Lookup(y) = true although the second document dropped it")
	}
}

func TestUseAppliesNothingWhenAnyRowIsInvalid(t *testing.T) {
	isolate(t)
	err := backendrows.Use(rows(t, []backendrows.Row{row("good", getRepos), {Name: "bad"}}))
	if err == nil {
		t.Fatal("Use() = nil, want an error")
	}
	if _, ok := backendrows.Lookup("good"); ok {
		t.Error("good row stored although the document was refused")
	}
}

func TestUseValidatesAcceptedRoutesWhenPresent(t *testing.T) {
	withAccepted := func(name string, accepted ...connectorhost.Route) backendrows.Row {
		r := row(name, getRepos)
		r.AcceptedRoutes = accepted
		return r
	}
	t.Run("blanket path is refused and applies nothing", func(t *testing.T) {
		isolate(t)
		blanket := connectorhost.Route{Method: "GET", Host: "api.fixture.test", Path: "/"}
		err := backendrows.Use(rows(t, []backendrows.Row{row("good", getRepos), withAccepted("broad", blanket)}))
		if err == nil {
			t.Fatal("Use() = nil, want an error")
		}
		for _, w := range []string{"backend rows: ", "broad", "accepted routes", "GET api.fixture.test /"} {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("err = %q, want it to contain %q", err, w)
			}
		}
		if !errors.Is(err, connectorhost.ErrRouteTable) {
			t.Errorf("err = %v, want ErrRouteTable", err)
		}
		if _, ok := backendrows.Lookup("good"); ok {
			t.Error("good row stored although the document was refused")
		}
	})
	t.Run("empty and valid baselines pass", func(t *testing.T) {
		isolate(t)
		if err := backendrows.Use(rows(t, []backendrows.Row{withAccepted("none"), withAccepted("some", getRepos)})); err != nil {
			t.Fatalf("Use() = %v", err)
		}
	})
}

func TestCoreBackendsAreRegistered(t *testing.T) {
	for _, n := range []string{"local", "git"} {
		if _, ok := backend.ByName(n); !ok {
			t.Errorf("%q is not a generated backend", n)
		}
	}
}

func fixtureWasm(t *testing.T) string {
	t.Helper()
	dir := os.Getenv(fixturesEnv)
	if dir == "" {
		t.Skipf("%s is unset; it names the dir holding the fixture Connector .wasm files", fixturesEnv)
	}
	return filepath.Join(dir, "network.wasm")
}

type fetched struct {
	Status int    `json:"status"`
	Body   string `json:"body"`
}

func TestLoadResolvesTheCredentialHostSideAndEnforcesTheRoutes(t *testing.T) {
	var seen []*http.Request
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r)
		fmt.Fprint(w, "ok "+r.URL.Path)
	}))
	t.Cleanup(srv.Close)
	host := srv.Listener.Addr().String()

	var audit bytes.Buffer
	r := backendrows.Row{
		Name: "fixture-network", Module: fixtureWasm(t), CredentialEnv: "FIXTURE_TOKEN",
		Routes: []connectorhost.Route{{Method: "GET", Host: host, Path: "/repos"}},
	}
	c, err := backendrows.Load(context.Background(), r, backendrows.Options{
		Getenv: func(k string) string {
			if k == "FIXTURE_TOKEN" {
				return "tok-123"
			}
			return ""
		},
		Audit:  &audit,
		Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })

	var out fetched
	if err := c.Call(context.Background(), "fetch", map[string]string{"url": srv.URL + "/repos/x", "method": "GET"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != 200 || out.Body != "ok /repos/x" {
		t.Errorf("fetched %+v", out)
	}
	if len(seen) != 1 || seen[0].Header.Get("Authorization") != "Bearer tok-123" {
		t.Fatalf("backend saw %d requests, want one carrying Bearer tok-123", len(seen))
	}

	err = c.Call(context.Background(), "fetch", map[string]string{"url": srv.URL + "/admin", "method": "GET"}, new(fetched))
	if !errors.Is(err, connectorhost.ErrRouteRefused) {
		t.Fatalf("err = %v, want ErrRouteRefused", err)
	}
	if len(seen) != 1 {
		t.Errorf("undeclared request reached the backend")
	}
	lines := strings.Split(strings.TrimSpace(audit.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"sent"`) || !strings.Contains(lines[1], `"refused"`) {
		t.Errorf("audit = %q, want a sent then a refused entry", audit.String())
	}
	if strings.Contains(audit.String(), "tok-123") {
		t.Error("credential in audit log")
	}
}

func TestLoadFailsClosedWithoutTheCredential(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("request reached the backend without a credential")
	}))
	t.Cleanup(srv.Close)
	r := backendrows.Row{
		Name: "fixture-network", Module: fixtureWasm(t), CredentialEnv: "FIXTURE_TOKEN",
		Routes: []connectorhost.Route{{Method: "GET", Host: srv.Listener.Addr().String(), Path: "/repos"}},
	}
	c, err := backendrows.Load(context.Background(), r, backendrows.Options{
		Getenv: func(string) string { return "" }, Audit: &bytes.Buffer{}, Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	err = c.Call(context.Background(), "fetch", map[string]string{"url": srv.URL + "/repos/x", "method": "GET"}, new(fetched))
	if err == nil || !errors.Is(err, connectorhost.ErrCall) {
		t.Fatalf("err = %v, want a failed call", err)
	}
}

func TestLoadReportsAnUnreadableModule(t *testing.T) {
	r := row("gone", getRepos)
	r.Module = filepath.Join(t.TempDir(), "absent.wasm")
	if _, err := backendrows.Load(context.Background(), r, backendrows.Options{Audit: &bytes.Buffer{}}); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("err = %v, want a read error naming the backend", err)
	}
}

func TestWidening(t *testing.T) {
	r := func(method, host, path string) connectorhost.Route {
		return connectorhost.Route{Method: method, Host: host, Path: path}
	}
	cases := []struct {
		name     string
		live     []connectorhost.Route
		accepted []connectorhost.Route
		want     []connectorhost.Route
	}{
		{"unchanged", []connectorhost.Route{getRepos}, []connectorhost.Route{getRepos}, nil},
		{"no baseline counts every route as added", []connectorhost.Route{getRepos}, nil, []connectorhost.Route{getRepos}},
		{"new host", []connectorhost.Route{getRepos, r("GET", "evil.test", "/repos")}, []connectorhost.Route{getRepos},
			[]connectorhost.Route{r("GET", "evil.test", "/repos")}},
		{"new method", []connectorhost.Route{getRepos, r("POST", "api.fixture.test", "/repos")}, []connectorhost.Route{getRepos},
			[]connectorhost.Route{r("POST", "api.fixture.test", "/repos")}},
		{"new path", []connectorhost.Route{getRepos, r("GET", "api.fixture.test", "/users")}, []connectorhost.Route{getRepos},
			[]connectorhost.Route{r("GET", "api.fixture.test", "/users")}},
		{"sibling sharing a prefix is not a subtree", []connectorhost.Route{r("GET", "api.fixture.test", "/reposx")}, []connectorhost.Route{getRepos},
			[]connectorhost.Route{r("GET", "api.fixture.test", "/reposx")}},
		{"broadened pattern", []connectorhost.Route{getRepos}, []connectorhost.Route{r("GET", "api.fixture.test", "/repos/x")},
			[]connectorhost.Route{getRepos}},
		{"narrowed pattern", []connectorhost.Route{r("GET", "api.fixture.test", "/repos/x")}, []connectorhost.Route{getRepos}, nil},
		{"host is compared literally", []connectorhost.Route{r("GET", "API.fixture.test", "/repos")}, []connectorhost.Route{getRepos},
			[]connectorhost.Route{r("GET", "API.fixture.test", "/repos")}},
		{"dropped route is no widening", nil, []connectorhost.Route{getRepos}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := backendrows.Row{Routes: tc.live, AcceptedRoutes: tc.accepted}.Widening()
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("Widening() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUseAcceptsAcceptedRoutesAndAllListsByName(t *testing.T) {
	isolate(t)
	raw := json.RawMessage(`[` +
		`{"name":"zeta","module":"/x/z.wasm","routes":[{"method":"GET","host":"h","path":"/a"}],"credentialEnv":"T","acceptedRoutes":[]},` +
		`{"name":"alpha","module":"/x/a.wasm","routes":[{"method":"GET","host":"h","path":"/a"}],"credentialEnv":"T",` +
		`"acceptedRoutes":[{"method":"GET","host":"h","path":"/a"}]}]`)
	if err := backendrows.Use(raw); err != nil {
		t.Fatal(err)
	}
	all := backendrows.All()
	if len(all) != 2 || all[0].Name != "alpha" || all[1].Name != "zeta" {
		t.Fatalf("All() = %+v, want alpha then zeta", all)
	}
	if len(all[0].AcceptedRoutes) != 1 || len(all[1].AcceptedRoutes) != 0 {
		t.Errorf("AcceptedRoutes not decoded: %+v", all)
	}
}
