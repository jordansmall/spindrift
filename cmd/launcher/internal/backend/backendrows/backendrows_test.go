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

func noEnv(string) string { return "" }

var getRepos = connectorhost.Route{Method: "GET", Host: "api.fixture.test", Path: "/repos"}

func TestUseRoundTripsRowsThroughLookup(t *testing.T) {
	isolate(t)
	raw := json.RawMessage(`[{"name":"fixture","module":"/nix/store/x-network.wasm",` +
		`"routes":[{"method":"GET","host":"api.fixture.test","path":"/repos"}],"credentialEnv":"FIXTURE_TOKEN"}]`)
	if err := backendrows.Use(raw, noEnv); err != nil {
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
			if err := backendrows.Use(rows(t, []backendrows.Row{row("x", getRepos)}), noEnv); err != nil {
				t.Fatal(err)
			}
			if err := backendrows.Use(raw, noEnv); err != nil {
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
		"empty name":      {rows(t, []backendrows.Row{row("", getRepos)}), []string{"no name"}, true},
		"repeated name":   {rows(t, []backendrows.Row{row("dup", getRepos), row("dup", getRepos)}), []string{"dup", "more than one row"}, true},
		"empty module":    {rows(t, []backendrows.Row{{Name: "nomod", Routes: []connectorhost.Route{getRepos}, CredentialEnv: "T"}}), []string{"nomod", "module"}, true},
		"empty cred":      {rows(t, []backendrows.Row{{Name: "nocred", Module: "/m.wasm", Routes: []connectorhost.Route{getRepos}}}), []string{"nocred", "credentialEnv"}, true},
		"no routes":       {rows(t, []backendrows.Row{{Name: "noroutes", Module: "/m.wasm", CredentialEnv: "T"}}), []string{"noroutes", "route"}, true},
		"core local":      {rows(t, []backendrows.Row{row("local", getRepos)}), []string{"local", "built in"}, true},
		"core git":        {rows(t, []backendrows.Row{row("git", getRepos)}), []string{"git", "built in"}, true},
		"unknown key":     {json.RawMessage(`[{"name":"x","bogus":1}]`), []string{"backend rows", "bogus"}, true},
		"unknown route":   {json.RawMessage(`[{"name":"x","routes":[{"method":"GET","host":"h","path":"/","bogus":1}]}]`), []string{"bogus"}, true},
		"not an array":    {json.RawMessage(`"nope"`), []string{"backend rows"}, true},
		"unknown scheme":  {rows(t, []backendrows.Row{withScheme(row("odd", getRepos), "digest", "")}), []string{"odd", "digest", "credentialScheme"}, true},
		"basic no id":     {rows(t, []backendrows.Row{withScheme(row("nobasic", getRepos), "basic", "")}), []string{"nobasic", "identityEnv"}, true},
		"bearer with id":  {rows(t, []backendrows.Row{withScheme(row("bearid", getRepos), "bearer", "EMAIL")}), []string{"bearid", "identityEnv"}, true},
		"default with id": {rows(t, []backendrows.Row{withScheme(row("defid", getRepos), "", "EMAIL")}), []string{"defid", "identityEnv"}, true},
		"root path":       {rows(t, []backendrows.Row{row("broad", connectorhost.Route{Method: "GET", Host: "h.test", Path: "/"})}), []string{"broad", "GET h.test /"}, false},
		"non-canonical":   {rows(t, []backendrows.Row{row("messy", connectorhost.Route{Method: "GET", Host: "h.test", Path: "/a/"})}), []string{"messy", "GET h.test /a/"}, false},
		"relative":        {rows(t, []backendrows.Row{row("rel", connectorhost.Route{Method: "GET", Host: "h.test", Path: "a"})}), []string{"rel", "GET h.test a"}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			err := backendrows.Use(tc.raw, noEnv)
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
	if err := backendrows.Use(rows(t, []backendrows.Row{row("x", getRepos), row("y", getRepos)}), noEnv); err != nil {
		t.Fatal(err)
	}
	if err := backendrows.Use(rows(t, []backendrows.Row{row("x", getRepos)}), noEnv); err != nil {
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
	err := backendrows.Use(rows(t, []backendrows.Row{row("good", getRepos), {Name: "bad"}}), noEnv)
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
		err := backendrows.Use(rows(t, []backendrows.Row{row("good", getRepos), withAccepted("broad", blanket)}), noEnv)
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
		if err := backendrows.Use(rows(t, []backendrows.Row{withAccepted("none"), withAccepted("some", getRepos)}), noEnv); err != nil {
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

func withScheme(r backendrows.Row, scheme, identityEnv string) backendrows.Row {
	r.CredentialScheme, r.IdentityEnv = scheme, identityEnv
	return r
}

func loadBasic(t *testing.T, srv *httptest.Server, env map[string]string, audit *bytes.Buffer) *connectorhost.Connector {
	t.Helper()
	r := backendrows.Row{
		Name: "fixture-network", Module: fixtureWasm(t), CredentialEnv: "FIXTURE_TOKEN",
		CredentialScheme: "basic", IdentityEnv: "FIXTURE_EMAIL",
		Routes: []connectorhost.Route{{Method: "GET", Host: srv.Listener.Addr().String(), Path: "/repos"}},
	}
	c, err := backendrows.Load(context.Background(), r, backendrows.Options{
		Getenv: func(k string) string { return env[k] }, Audit: audit, Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

func TestLoadSendsBasicAuthFromTheIdentityAndCredential(t *testing.T) {
	var got []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		fmt.Fprint(w, "ok")
	}))
	t.Cleanup(srv.Close)
	c := loadBasic(t, srv, map[string]string{"FIXTURE_EMAIL": "me@example.com", "FIXTURE_TOKEN": "tok-123"}, &bytes.Buffer{})
	if err := c.Call(context.Background(), "fetch", map[string]string{"url": srv.URL + "/repos/x", "method": "GET"}, new(fetched)); err != nil {
		t.Fatal(err)
	}
	// base64("me@example.com:tok-123")
	if want := "Basic bWVAZXhhbXBsZS5jb206dG9rLTEyMw=="; len(got) != 1 || got[0] != want {
		t.Fatalf("backend saw %q, want one request carrying %q", got, want)
	}
}

func TestLoadBasicFailsClosedWithoutIdentityOrCredential(t *testing.T) {
	cases := map[string]map[string]string{
		"no identity":   {"FIXTURE_TOKEN": "tok-123"},
		"no credential": {"FIXTURE_EMAIL": "me@example.com"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("request reached the backend without full Basic credentials")
			}))
			t.Cleanup(srv.Close)
			var audit bytes.Buffer
			c := loadBasic(t, srv, env, &audit)
			err := c.Call(context.Background(), "fetch", map[string]string{"url": srv.URL + "/repos/x", "method": "GET"}, new(fetched))
			if err == nil || !errors.Is(err, connectorhost.ErrCall) {
				t.Fatalf("err = %v, want a failed call", err)
			}
			// The guest receives the send failure as text, so the sentinel is matched by message.
			if !strings.Contains(err.Error(), connectorhost.ErrHTTPSend.Error()) {
				t.Errorf("err = %v, want the %q failure", err, connectorhost.ErrHTTPSend)
			}
			lines := strings.Split(strings.TrimSpace(audit.String()), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], `"failed"`) {
				t.Errorf("audit = %q, want exactly one failed entry", audit.String())
			}
		})
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
	if err := backendrows.Use(raw, noEnv); err != nil {
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

func baseRow(routes ...connectorhost.Route) backendrows.Row {
	r := row("site", routes...)
	r.BaseEnv = "SITE_BASE_URL"
	return r
}

func envOf(base string) func(string) string {
	return func(k string) string {
		if k == "SITE_BASE_URL" {
			return base
		}
		return ""
	}
}

func TestUseSubstitutesTheConfiguredBaseURLIntoRoutes(t *testing.T) {
	search := connectorhost.Route{Method: "GET", Host: "{base}", Path: "/rest/api/3/search"}
	cases := map[string]struct {
		base string
		want connectorhost.Route
	}{
		"bare host":        {"https://acme.example.net", connectorhost.Route{Method: "GET", Host: "acme.example.net", Path: "/rest/api/3/search"}},
		"trailing slash":   {"https://acme.example.net/", connectorhost.Route{Method: "GET", Host: "acme.example.net", Path: "/rest/api/3/search"}},
		"path prefix":      {"https://acme.example.net/jira", connectorhost.Route{Method: "GET", Host: "acme.example.net", Path: "/jira/rest/api/3/search"}},
		"prefix and slash": {"https://acme.example.net:8443/jira/", connectorhost.Route{Method: "GET", Host: "acme.example.net:8443", Path: "/jira/rest/api/3/search"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			r := baseRow(search, getRepos)
			r.AcceptedRoutes = []connectorhost.Route{search}
			if err := backendrows.Use(rows(t, []backendrows.Row{r}), envOf(tc.base)); err != nil {
				t.Fatal(err)
			}
			got, _ := backendrows.Lookup("site")
			if len(got.Routes) != 2 || got.Routes[0] != tc.want || got.Routes[1] != getRepos {
				t.Errorf("Routes = %v, want [%v %v]", got.Routes, tc.want, getRepos)
			}
			if len(got.AcceptedRoutes) != 1 || got.AcceptedRoutes[0] != tc.want {
				t.Errorf("AcceptedRoutes = %v, want [%v]", got.AcceptedRoutes, tc.want)
			}
			if w := got.Widening(); len(w) != 1 || w[0] != getRepos {
				t.Errorf("Widening() = %v, want only %v", w, getRepos)
			}
		})
	}
}

// baseSearch is the one {base} route the base-URL tests below resolve.
var baseSearch = connectorhost.Route{Method: "GET", Host: "{base}", Path: "/rest"}

func TestUseRefusesAnInvalidBaseURLNamingTheBackend(t *testing.T) {
	cases := map[string]struct {
		row  backendrows.Row
		base string
	}{
		"plaintext":        {shadowedBaseRow(baseSearch), "http://acme.example.net"},
		"ipv6 zone":        {shadowedBaseRow(baseSearch), "https://[fe80::1%25en0]"},
		"no scheme":        {shadowedBaseRow(baseSearch), "acme.example.net"},
		"userinfo":         {shadowedBaseRow(baseSearch), "https://u:p@acme.example.net"},
		"query":            {shadowedBaseRow(baseSearch), "https://acme.example.net/?x=1"},
		"fragment":         {shadowedBaseRow(baseSearch), "https://acme.example.net/#x"},
		"escaped path":     {shadowedBaseRow(baseSearch), "https://acme.example.net/ji%2Fra"},
		"bad escape":       {shadowedBaseRow(baseSearch), "https://u:p%zz@acme.example.net"},
		"empty host":       {shadowedBaseRow(baseSearch), "https:///jira"},
		"malformed host":   {shadowedBaseRow(baseSearch), "https://acme.example.net:"},
		"unparseable":      {shadowedBaseRow(baseSearch), "https://acme example.net"},
		"host with no env": {row("site", baseSearch), "https://acme.example.net"},
		"non-canonical":    {shadowedBaseRow(baseSearch), "https://acme.example.net/jira//"},
		"root doubled":     {shadowedBaseRow(baseSearch), "https://acme.example.net//"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			err := backendrows.Use(rows(t, []backendrows.Row{row("good", getRepos), tc.row}), envOf(tc.base))
			if err == nil {
				t.Fatal("Use() = nil, want an error")
			}
			if !strings.Contains(err.Error(), `backend "site"`) {
				t.Errorf("err = %q, want it to name the backend", err)
			}
			if strings.Contains(err.Error(), "p%zz") {
				t.Errorf("err = %q, want it to leave the base URL value out", err)
			}
			if _, ok := backendrows.Lookup("good"); ok {
				t.Error("good row stored although the document was refused")
			}
		})
	}
}

func shadowedBaseRow(routes ...connectorhost.Route) backendrows.Row {
	r := baseRow(routes...)
	r.Shadow, r.ShadowEvery = true, 20
	return r
}

func TestUseSkipsANonShadowBaseRowWithAnUnusableBase(t *testing.T) {
	for name, base := range map[string]string{
		"unset":                 "",
		"plaintext":             "http://jira.corp:8080",
		"ipv6 zone":             "https://[fe80::1%25en0]",
		"query":                 "https://acme.example.net/?x=1",
		"escaped path":          "https://acme.example.net/ji%2Fra",
		"userinfo":              "https://u:p@acme.example.net",
		"unparseable":           "https://acme example.net",
		"empty host":            "https:///jira",
		"empty port":            "https://acme.example.net:",
		"trailing double slash": "https://jira.corp/jira//",
		"root doubled":          "https://jira.corp//",
		"leading double slash":  "https://jira.corp//jira",
		"dot segment":           "https://jira.corp/./jira",
	} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			if err := backendrows.Use(rows(t, []backendrows.Row{row("good", getRepos), baseRow(baseSearch)}), envOf(base)); err != nil {
				t.Fatalf("Use() = %v, want the row left unapplied", err)
			}
			if _, ok := backendrows.Lookup("site"); ok {
				t.Error("row with an unusable base applied")
			}
			if _, ok := backendrows.Lookup("good"); !ok {
				t.Error("good row not applied")
			}
		})
	}
}

func TestUseStillValidatesAnUnconfiguredBaseRow(t *testing.T) {
	isolate(t)
	bad := baseRow(baseSearch)
	bad.CredentialEnv = ""
	err := backendrows.Use(rows(t, []backendrows.Row{bad}), envOf(""))
	if err == nil || !strings.Contains(err.Error(), "no credentialEnv") {
		t.Errorf("err = %v, want the missing credentialEnv refused", err)
	}
}

func TestUseRefusesAShadowedBaseRowWithAnUnsetBase(t *testing.T) {
	isolate(t)
	err := backendrows.Use(rows(t, []backendrows.Row{shadowedBaseRow(baseSearch)}), envOf(""))
	if err == nil || !strings.Contains(err.Error(), "SITE_BASE_URL is not set") {
		t.Errorf("err = %v, want it to name SITE_BASE_URL as not set", err)
	}
}

func TestUseMapsARootBaseRouteToTheBarePrefix(t *testing.T) {
	isolate(t)
	root := connectorhost.Route{Method: "GET", Host: "{base}", Path: "/"}
	if err := backendrows.Use(rows(t, []backendrows.Row{baseRow(root)}), envOf("https://acme.example.net/jira")); err != nil {
		t.Fatal(err)
	}
	got, _ := backendrows.Lookup("site")
	if want := (connectorhost.Route{Method: "GET", Host: "acme.example.net", Path: "/jira"}); got.Routes[0] != want {
		t.Errorf("Routes[0] = %v, want %v", got.Routes[0], want)
	}
}

func TestUseRoundTripsAndValidatesShadowRows(t *testing.T) {
	shadowed := func(shadow bool, every int) backendrows.Row {
		r := row("gh", getRepos)
		r.Shadow, r.ShadowEvery = shadow, every
		return r
	}
	t.Run("round trip", func(t *testing.T) {
		isolate(t)
		if err := backendrows.Use(rows(t, []backendrows.Row{shadowed(true, 20)}), noEnv); err != nil {
			t.Fatal(err)
		}
		got, _ := backendrows.Lookup("gh")
		if !got.Shadow || got.ShadowEvery != 20 {
			t.Errorf("Lookup = %+v, want shadow every 20", got)
		}
		if strings.Contains(string(rows(t, []backendrows.Row{row("x", getRepos)})), "shadow") {
			t.Error("a non-shadow row renders a shadow key")
		}
	})
	for name, tc := range map[string]struct {
		row  backendrows.Row
		want string
	}{
		"negative":              {shadowed(true, -1), "negative shadowEvery"},
		"negative without flag": {shadowed(false, -1), "negative shadowEvery"},
		"every without shadow":  {shadowed(false, 5), "shadowEvery without shadow"},
		"shadow without every":  {shadowed(true, 0), "shadow without shadowEvery"},
	} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			err := backendrows.Use(rows(t, []backendrows.Row{tc.row}), noEnv)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), `"gh"`) {
				t.Errorf("err = %v, want it to name gh and contain %q", err, tc.want)
			}
		})
	}
}

func TestLoadPassesShadowAndObserveToTheNetwork(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	t.Cleanup(srv.Close)
	var audit bytes.Buffer
	var seen []connectorhost.AuditEntry
	r := backendrows.Row{
		Name: "fixture-network", Module: fixtureWasm(t), CredentialEnv: "FIXTURE_TOKEN",
		Routes: []connectorhost.Route{{Method: "GET", Host: srv.Listener.Addr().String(), Path: "/repos"}},
	}
	c, err := backendrows.Load(context.Background(), r, backendrows.Options{
		Getenv: func(string) string { return "tok" }, Audit: &audit, Client: srv.Client(),
		Shadow: true, Observe: func(e connectorhost.AuditEntry) { seen = append(seen, e) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	if err := c.Call(context.Background(), "fetch", map[string]string{"url": srv.URL + "/repos/x", "method": "GET"}, new(fetched)); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || !seen[0].Shadow || seen[0].Status != 200 || !strings.Contains(audit.String(), `"shadow":true`) {
		t.Errorf("observed %+v, audit %q", seen, audit.String())
	}
}
