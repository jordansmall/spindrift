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
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorrecord"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/hostpaths"
)

const fixturesEnv = "SPINDRIFT_CONNECTOR_FIXTURES_DIR"

func isolate(t *testing.T) {
	t.Helper()
	backendrows.Reset()
	t.Cleanup(backendrows.Reset)
	t.Cleanup(backendrows.Snapshot())
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

	audit := auditInTempDir(t, "fixture-network")
	r := backendrows.Row{
		Name: "fixture-network", Module: fixtureWasm(t), CredentialEnv: "FIXTURE_TOKEN",
		Routes: []connectorhost.Route{{Method: "GET", Host: host, Path: "/repos"}},
	}
	c, err := backendrows.Load(context.Background(), r, backendrows.Options{
		Sandbox: connectorsandboxtest.InProcess(),
		Getenv: func(k string) string {
			if k == "FIXTURE_TOKEN" {
				return "tok-123"
			}
			return ""
		},
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
	lines := strings.Split(strings.TrimSpace(audit()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"sent"`) || !strings.Contains(lines[1], `"refused"`) {
		t.Errorf("audit = %q, want a sent then a refused entry", audit())
	}
	if strings.Contains(audit(), "tok-123") {
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
		Sandbox: connectorsandboxtest.InProcess(),
		Getenv:  func(string) string { return "" }, Client: srv.Client(),
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

func loadBasic(t *testing.T, srv *httptest.Server, env map[string]string) *connectorhost.Connector {
	t.Helper()
	r := backendrows.Row{
		Name: "fixture-network", Module: fixtureWasm(t), CredentialEnv: "FIXTURE_TOKEN",
		CredentialScheme: "basic", IdentityEnv: "FIXTURE_EMAIL",
		Routes: []connectorhost.Route{{Method: "GET", Host: srv.Listener.Addr().String(), Path: "/repos"}},
	}
	c, err := backendrows.Load(context.Background(), r, backendrows.Options{
		Sandbox: connectorsandboxtest.InProcess(),
		Getenv:  func(k string) string { return env[k] }, Client: srv.Client(),
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
	auditInTempDir(t, "fixture-network")
	c := loadBasic(t, srv, map[string]string{"FIXTURE_EMAIL": "me@example.com", "FIXTURE_TOKEN": "tok-123"})
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
			audit := auditInTempDir(t, "fixture-network")
			c := loadBasic(t, srv, env)
			err := c.Call(context.Background(), "fetch", map[string]string{"url": srv.URL + "/repos/x", "method": "GET"}, new(fetched))
			if err == nil || !errors.Is(err, connectorhost.ErrCall) {
				t.Fatalf("err = %v, want a failed call", err)
			}
			// The guest receives the send failure as text, so the sentinel is matched by message.
			if !strings.Contains(err.Error(), connectorhost.ErrHTTPSend.Error()) {
				t.Errorf("err = %v, want the %q failure", err, connectorhost.ErrHTTPSend)
			}
			lines := strings.Split(strings.TrimSpace(audit()), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], `"failed"`) {
				t.Errorf("audit = %q, want exactly one failed entry", audit())
			}
		})
	}
}

func TestLoadReportsAnUnreadableModule(t *testing.T) {
	t.Chdir(t.TempDir())
	r := row("gone", getRepos)
	r.Module = filepath.Join(t.TempDir(), "absent.wasm")
	if _, err := backendrows.Load(context.Background(), r, backendrows.Options{}); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("err = %v, want a read error naming the backend", err)
	}
}

func withHeaders(r connectorhost.Route, req, resp []string) connectorhost.Route {
	r.RequestHeaders, r.ResponseHeaders = req, resp
	return r
}

func withDocs(r connectorhost.Route, docs map[string]string) connectorhost.Route {
	r.Documents = docs
	return r
}

func TestWidening(t *testing.T) {
	r := func(method, host, path string) connectorhost.Route {
		return connectorhost.Route{Method: method, Host: host, Path: path}
	}
	gql := r("POST", "api.fixture.test", "/graphql")
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
		{"opting into a redirect widens", []connectorhost.Route{{Method: "GET", Host: "api.fixture.test", Path: "/repos", FollowRedirect: true}}, []connectorhost.Route{getRepos},
			[]connectorhost.Route{{Method: "GET", Host: "api.fixture.test", Path: "/repos", FollowRedirect: true}}},
		{"dropping the redirect opt-in is no widening", []connectorhost.Route{getRepos}, []connectorhost.Route{{Method: "GET", Host: "api.fixture.test", Path: "/repos", FollowRedirect: true}}, nil},
		{"dropped route is no widening", nil, []connectorhost.Route{getRepos}, nil},
		{"an added request header widens", []connectorhost.Route{withHeaders(getRepos, []string{"X-Extra"}, nil)}, []connectorhost.Route{getRepos},
			[]connectorhost.Route{withHeaders(getRepos, []string{"X-Extra"}, nil)}},
		{"an added response header widens", []connectorhost.Route{withHeaders(getRepos, nil, []string{"X-Extra"})}, []connectorhost.Route{getRepos},
			[]connectorhost.Route{withHeaders(getRepos, nil, []string{"X-Extra"})}},
		{"a request header is no response header", []connectorhost.Route{withHeaders(getRepos, nil, []string{"X-Extra"})}, []connectorhost.Route{withHeaders(getRepos, []string{"X-Extra"}, nil)},
			[]connectorhost.Route{withHeaders(getRepos, nil, []string{"X-Extra"})}},
		{"a second request header widens", []connectorhost.Route{withHeaders(getRepos, []string{"X-A", "X-B"}, nil)}, []connectorhost.Route{withHeaders(getRepos, []string{"X-A"}, nil)},
			[]connectorhost.Route{withHeaders(getRepos, []string{"X-A", "X-B"}, nil)}},
		{"the same headers in another case are covered", []connectorhost.Route{withHeaders(getRepos, []string{"x-extra"}, []string{"X-RESP"})}, []connectorhost.Route{withHeaders(getRepos, []string{"X-Extra"}, []string{"x-resp"})}, nil},
		{"a new document widens", []connectorhost.Route{withDocs(gql, map[string]string{"A": "q a", "B": "q b"})}, []connectorhost.Route{withDocs(gql, map[string]string{"A": "q a"})},
			[]connectorhost.Route{withDocs(gql, map[string]string{"A": "q a", "B": "q b"})}},
		{"a changed document text widens", []connectorhost.Route{withDocs(gql, map[string]string{"A": "q a2"})}, []connectorhost.Route{withDocs(gql, map[string]string{"A": "q a"})},
			[]connectorhost.Route{withDocs(gql, map[string]string{"A": "q a2"})}},
		{"dropping the document constraint widens", []connectorhost.Route{gql}, []connectorhost.Route{withDocs(gql, map[string]string{"A": "q a"})},
			[]connectorhost.Route{gql}},
		{"fewer documents are covered", []connectorhost.Route{withDocs(gql, map[string]string{"A": "q a"})}, []connectorhost.Route{withDocs(gql, map[string]string{"A": "q a", "B": "q b"})}, nil},
		{"the same documents are covered", []connectorhost.Route{withDocs(gql, map[string]string{"A": "q a"})}, []connectorhost.Route{withDocs(gql, map[string]string{"A": "q a"})}, nil},
		{"an unconstrained accepted route covers documents", []connectorhost.Route{withDocs(gql, map[string]string{"A": "q a"})}, []connectorhost.Route{gql}, nil},
		{"fewer headers are covered", []connectorhost.Route{getRepos}, []connectorhost.Route{withHeaders(getRepos, []string{"X-Extra"}, []string{"X-Resp"})}, nil},
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

func TestGrantChanges(t *testing.T) {
	base := backendrows.Row{CredentialEnv: "TOK", BaseEnv: "SITE", GitRemote: "https://forge.test"}
	same := backendrows.Grant{CredentialEnv: "TOK", BaseEnv: "SITE", GitRemote: "https://forge.test"}
	with := func(r backendrows.Row, g *backendrows.Grant) backendrows.Row { r.AcceptedGrant = g; return r }
	scheme := func(s string) backendrows.Row { r := base; r.CredentialScheme = s; return r }
	envRemote := backendrows.Row{CredentialEnv: "TOK", GitRemote: "https://forge.test", GitRemoteEnv: "FORGE_URL"}
	cases := []struct {
		name string
		row  backendrows.Row
		want []backendrows.GrantChange
	}{
		{"no grant records routes only", base, nil},
		{"unchanged", with(base, &same), nil},
		{"credential variable", with(base, &backendrows.Grant{CredentialEnv: "OLD", BaseEnv: "SITE", GitRemote: "https://forge.test"}),
			[]backendrows.GrantChange{{"credential variable", "TOK", "OLD"}}},
		{"scheme widens to basic", with(scheme("basic"), &same),
			[]backendrows.GrantChange{{"authorization scheme", "basic", "bearer"}}},
		{"empty and bearer agree", with(scheme("bearer"), &same), nil},
		{"base declared but not accepted", with(base, &backendrows.Grant{CredentialEnv: "TOK", GitRemote: "https://forge.test"}),
			[]backendrows.GrantChange{{"base", "SITE", "none"}}},
		{"literal remote differs", with(base, &backendrows.Grant{CredentialEnv: "TOK", BaseEnv: "SITE", GitRemote: "https://other.test"}),
			[]backendrows.GrantChange{{"Git remote", "https://forge.test", "https://other.test"}}},
		{"remote declared but not accepted", with(base, &backendrows.Grant{CredentialEnv: "TOK", BaseEnv: "SITE"}),
			[]backendrows.GrantChange{{"Git remote", "https://forge.test", "none"}}},
		{"env remote against literal grant", with(envRemote, &backendrows.Grant{CredentialEnv: "TOK", GitRemote: "https://forge.test"}),
			[]backendrows.GrantChange{{"Git remote", "env FORGE_URL", "https://forge.test"}}},
		{"env remote matches", with(envRemote, &backendrows.Grant{CredentialEnv: "TOK", GitRemoteEnv: "FORGE_URL"}), nil},
		{"narrowing is silent", with(backendrows.Row{CredentialEnv: "TOK"}, &same), nil},
		{"several parts", with(base, &backendrows.Grant{}),
			[]backendrows.GrantChange{{"credential variable", "TOK", "none"}, {"base", "SITE", "none"}, {"Git remote", "https://forge.test", "none"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.row.GrantChanges(); fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("GrantChanges() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUseDecodesAcceptedGrantAndResolvedGitRemoteDoesNotDiffer(t *testing.T) {
	isolate(t)
	raw := json.RawMessage(`[{"name":"forge","module":"/x/f.wasm","routes":[{"method":"GET","host":"h","path":"/a"}],` +
		`"credentialEnv":"T","gitRemoteEnv":"FORGE_URL",` +
		`"acceptedGrant":{"credentialEnv":"T","gitRemoteEnv":"FORGE_URL"}}]`)
	env := func(k string) string {
		if k == "FORGE_URL" {
			return "https://forge.test/"
		}
		return ""
	}
	if err := backendrows.Use(raw, env); err != nil {
		t.Fatal(err)
	}
	got, _ := backendrows.Lookup("forge")
	if got.GitRemote != "https://forge.test" || got.AcceptedGrant == nil || got.AcceptedGrant.GitRemoteEnv != "FORGE_URL" {
		t.Fatalf("row = %+v, want resolved remote and decoded grant", got)
	}
	if c := got.GrantChanges(); len(c) != 0 {
		t.Errorf("GrantChanges() = %v, want none", c)
	}
}

func TestUseRefusesAnInvalidAcceptedGrant(t *testing.T) {
	for name, grant := range map[string]string{
		"unknown scheme":        `{"credentialScheme":"digest"}`,
		"both git remote forms": `{"gitRemote":"https://f.test","gitRemoteEnv":"U"}`,
	} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			raw := json.RawMessage(`[{"name":"x","module":"/x/x.wasm","routes":[{"method":"GET","host":"h","path":"/a"}],"credentialEnv":"T","acceptedGrant":` + grant + `}]`)
			if err := backendrows.Use(raw, noEnv); err == nil || !strings.Contains(err.Error(), "accepted grant") {
				t.Errorf("Use() = %v, want an accepted grant error", err)
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
			if len(got.Routes) != 2 || !reflect.DeepEqual(got.Routes[0], tc.want) || !reflect.DeepEqual(got.Routes[1], getRepos) {
				t.Errorf("Routes = %v, want [%v %v]", got.Routes, tc.want, getRepos)
			}
			if len(got.AcceptedRoutes) != 1 || !reflect.DeepEqual(got.AcceptedRoutes[0], tc.want) {
				t.Errorf("AcceptedRoutes = %v, want [%v]", got.AcceptedRoutes, tc.want)
			}
			if w := got.Widening(); len(w) != 1 || !reflect.DeepEqual(w[0], getRepos) {
				t.Errorf("Widening() = %v, want only %v", w, getRepos)
			}
		})
	}
}

// baseSearch is the one {base} route the base-URL tests below resolve.
var baseSearch = connectorhost.Route{Method: "GET", Host: "{base}", Path: "/rest"}

func TestUseRefusesABaseHostWithNoBaseEnvNamingTheBackend(t *testing.T) {
	isolate(t)
	err := backendrows.Use(rows(t, []backendrows.Row{row("good", getRepos), row("site", baseSearch)}), envOf("https://acme.example.net"))
	if err == nil {
		t.Fatal("Use() = nil, want an error")
	}
	if !strings.Contains(err.Error(), `backend "site"`) {
		t.Errorf("err = %q, want it to name the backend", err)
	}
	if _, ok := backendrows.Lookup("good"); ok {
		t.Error("good row stored although the document was refused")
	}
}

// The shadow and primary fields are gone from the row; a stale document that
// still carries one is refused rather than silently changing routing.
func TestUseRefusesTheRetiredShadowAndPrimaryFields(t *testing.T) {
	for _, field := range []string{`"shadow":true`, `"shadowEvery":20`, `"primary":true`} {
		t.Run(field, func(t *testing.T) {
			isolate(t)
			doc := rows(t, []backendrows.Row{row("gh", getRepos)})
			doc = append(bytes.TrimSuffix(doc, []byte("}]")), []byte(","+field+"}]")...)
			if err := backendrows.Use(doc, noEnv); err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Errorf("Use(%s) = %v, want an unknown-field error", doc, err)
			}
		})
	}
}

func TestUseSkipsABaseRowWithAnUnusableBase(t *testing.T) {
	for name, base := range map[string]string{
		"unset":                 "",
		"no scheme":             "acme.example.net",
		"fragment":              "https://acme.example.net/#x",
		"bad escape":            "https://u:p%zz@acme.example.net",
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

func TestBaseErrorReportsTheBaseOfADroppedRowOnly(t *testing.T) {
	isolate(t)
	good, site := row("good", getRepos), baseRow(baseSearch)
	if err := backendrows.Use(rows(t, []backendrows.Row{good, site}), envOf("http://site.lan:3000")); err != nil {
		t.Fatal(err)
	}
	if err := backendrows.BaseError("site"); err == nil || !strings.Contains(err.Error(), "must be an https URL") {
		t.Errorf("BaseError(site) = %v, want the https error", err)
	}
	if err := backendrows.BaseError("good"); err != nil {
		t.Errorf("BaseError(good) = %v, want nil for a row with no base", err)
	}
	if err := backendrows.Use(rows(t, []backendrows.Row{good, site}), envOf("https://site.example.net")); err != nil {
		t.Fatal(err)
	}
	if err := backendrows.BaseError("site"); err != nil {
		t.Errorf("BaseError(site) = %v, want nil once the row applied", err)
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

func TestUseMapsARootBaseRouteToTheBarePrefix(t *testing.T) {
	isolate(t)
	root := connectorhost.Route{Method: "GET", Host: "{base}", Path: "/"}
	if err := backendrows.Use(rows(t, []backendrows.Row{baseRow(root)}), envOf("https://acme.example.net/jira")); err != nil {
		t.Fatal(err)
	}
	got, _ := backendrows.Lookup("site")
	if want := (connectorhost.Route{Method: "GET", Host: "acme.example.net", Path: "/jira"}); !reflect.DeepEqual(got.Routes[0], want) {
		t.Errorf("Routes[0] = %v, want %v", got.Routes[0], want)
	}
}

// recordingLoader is a sandbox that records the network name of each load it is
// handed.
type recordingLoader struct {
	connectorsandbox.Loader
	networks []string
}

func (l *recordingLoader) LoadNetwork(_ context.Context, _ []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
	l.networks = append(l.networks, n.Name)
	return nil, errors.New("recorded")
}

// A reload is a Load like any other, so under the Connector sandbox the
// replacement is loaded into it too.
func TestLoadRoutesEveryLoadThroughTheOptionsSandbox(t *testing.T) {
	t.Chdir(t.TempDir())
	module := filepath.Join(t.TempDir(), "x.wasm")
	if err := os.WriteFile(module, []byte("\x00asm"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := &recordingLoader{}
	r := backendrows.Row{Name: "fixture", Module: module, CredentialEnv: "TOKEN"}
	for range 2 {
		if _, err := backendrows.Load(context.Background(), r, backendrows.Options{Sandbox: l}); err == nil || err.Error() != "recorded" {
			t.Fatalf("Load = %v, want the sandbox's error", err)
		}
	}
	if len(l.networks) != 2 || l.networks[0] != "fixture" || l.networks[1] != "fixture" {
		t.Errorf("sandbox saw loads %v, want two for fixture", l.networks)
	}
}

func TestUseDropsARowItsGateRefuses(t *testing.T) {
	isolate(t)
	backendrows.RegisterGate("gated", func(lookup func(string) string) bool { return lookup("GATE_OPEN") != "" })
	raw := rows(t, []backendrows.Row{row("gated", getRepos)})
	if err := backendrows.Use(raw, noEnv); err != nil {
		t.Fatal(err)
	}
	if _, ok := backendrows.Lookup("gated"); ok {
		t.Error("a row whose gate answers false is applied")
	}
	open := func(k string) string {
		if k == "GATE_OPEN" {
			return "1"
		}
		return ""
	}
	if err := backendrows.Use(raw, open); err != nil {
		t.Fatal(err)
	}
	if _, ok := backendrows.Lookup("gated"); !ok {
		t.Error("a row whose gate answers true is dropped")
	}
}

func TestUseValidatesARowItsGateRefuses(t *testing.T) {
	isolate(t)
	backendrows.RegisterGate("gated", func(func(string) string) bool { return false })
	bad := row("gated")
	bad.Routes = nil
	if err := backendrows.Use(rows(t, []backendrows.Row{bad}), noEnv); err == nil {
		t.Error("an invalid row passed validation because its gate refuses it")
	}
}

func TestSnapshotRestoreDropsAGateRegisteredAfterIt(t *testing.T) {
	isolate(t)
	restore := backendrows.Snapshot()
	backendrows.RegisterGate("gated", func(func(string) string) bool { return false })
	restore()
	if err := backendrows.Use(rows(t, []backendrows.Row{row("gated", getRepos)}), noEnv); err != nil {
		t.Fatal(err)
	}
	if _, ok := backendrows.Lookup("gated"); !ok {
		t.Error("a gate registered after Snapshot survives its restore")
	}
}

// auditInTempDir runs the test in a fresh directory, where Load opens its audit
// file, and returns a reader of what the file holds.
func auditInTempDir(t *testing.T, name string) func() string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return func() string {
		raw, err := os.ReadFile(filepath.Join(hostpaths.LogDir(dir), connectorrecord.Name(name, ".audit.jsonl")))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
}

// A Connector never runs unrecorded: an audit file that will not open fails the
// load, naming the file, before the module is read or compiled.
func TestLoadFailsWhenTheAuditFileWillNotOpen(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	logDir := hostpaths.LogDir(dir)
	if err := os.MkdirAll(filepath.Dir(logDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logDir, nil, 0o644); err != nil { // a file where the log directory belongs
		t.Fatal(err)
	}
	l := &recordingLoader{}
	r := row("gone", getRepos)
	r.Module = filepath.Join(dir, "absent.wasm")

	_, err := backendrows.Load(context.Background(), r, backendrows.Options{Sandbox: l})
	want := filepath.Join(logDir, connectorrecord.Name("gone", ".audit.jsonl"))
	if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "read module") {
		t.Fatalf("err = %v, want the audit open failure naming %s, not the module read", err, want)
	}
	if len(l.networks) != 0 {
		t.Errorf("sandbox saw loads %v, want none", l.networks)
	}
}

func remoteRow(name, remote, remoteEnv string) backendrows.Row {
	r := row(name, getRepos)
	r.GitRemote, r.GitRemoteEnv = remote, remoteEnv
	return r
}

func TestUseDecodesTheGitRemoteFields(t *testing.T) {
	isolate(t)
	raw := json.RawMessage(`[` +
		`{"name":"lit","module":"/x/l.wasm","routes":[{"method":"GET","host":"h","path":"/a"}],"credentialEnv":"T","gitRemote":"https://github.com"},` +
		`{"name":"knob","module":"/x/k.wasm","routes":[{"method":"GET","host":"h","path":"/a"}],"credentialEnv":"T","gitRemoteEnv":"FORGE_URL"},` +
		`{"name":"tracker","module":"/x/t.wasm","routes":[{"method":"GET","host":"h","path":"/a"}],"credentialEnv":"T"}]`)
	env := func(k string) string {
		if k == "FORGE_URL" {
			return "https://git.example.net/forgejo/"
		}
		return ""
	}
	if err := backendrows.Use(raw, env); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"lit": "https://github.com", "knob": "https://git.example.net/forgejo", "tracker": ""} {
		got, ok := backendrows.Lookup(name)
		if !ok || got.GitRemote != want {
			t.Errorf("Lookup(%s) = %q, %v; want GitRemote %q", name, got.GitRemote, ok, want)
		}
	}
}

func TestUseRefusesAMalformedGitRemoteNamingTheBackend(t *testing.T) {
	for name, r := range map[string]backendrows.Row{
		"path":     remoteRow("forge", "https://github.com/org", ""),
		"slash":    remoteRow("forge", "https://github.com/", ""),
		"http":     remoteRow("forge", "http://github.com", ""),
		"userinfo": remoteRow("forge", "https://u:p@github.com", ""),
		"query":    remoteRow("forge", "https://github.com?x=1", ""),
		"both":     remoteRow("forge", "https://github.com", "FORGE_URL"),
	} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			err := backendrows.Use(rows(t, []backendrows.Row{row("good", getRepos), r}), noEnv)
			if err == nil || !strings.Contains(err.Error(), `backend "forge"`) {
				t.Fatalf("Use() = %v, want an error naming the backend", err)
			}
			if _, ok := backendrows.Lookup("good"); ok {
				t.Error("good row stored although the document was refused")
			}
		})
	}
}

func TestUseResolvesGitRemoteEnv(t *testing.T) {
	for name, tc := range map[string]struct{ base, want string }{
		"bare host":      {"https://git.example.net", "https://git.example.net"},
		"trailing slash": {"https://git.example.net/", "https://git.example.net"},
		"subpath":        {"https://git.example.net/forgejo", "https://git.example.net/forgejo"},
		"subpath slash":  {"https://git.example.net:8443/a/b/", "https://git.example.net:8443/a/b"},
	} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			env := func(k string) string {
				if k == "FORGE_URL" {
					return tc.base
				}
				return ""
			}
			if err := backendrows.Use(rows(t, []backendrows.Row{remoteRow("forge", "", "FORGE_URL")}), env); err != nil {
				t.Fatal(err)
			}
			if got, _ := backendrows.Lookup("forge"); got.GitRemote != tc.want {
				t.Errorf("GitRemote = %q, want %q", got.GitRemote, tc.want)
			}
		})
	}
}

func TestUseSkipsARowWithAnUnusableGitRemoteEnv(t *testing.T) {
	for name, base := range map[string]string{
		"unset":      "",
		"no scheme":  "git.example.net",
		"plaintext":  "http://git.lan:3000",
		"userinfo":   "https://u:p@git.example.net",
		"query":      "https://git.example.net/?x=1",
		"dot suffix": "https://git.example.net/./forgejo",
	} {
		t.Run(name, func(t *testing.T) {
			isolate(t)
			env := func(k string) string {
				if k == "FORGE_URL" {
					return base
				}
				return ""
			}
			if err := backendrows.Use(rows(t, []backendrows.Row{row("good", getRepos), remoteRow("forge", "", "FORGE_URL")}), env); err != nil {
				t.Fatalf("Use() = %v, want the row left unapplied", err)
			}
			if _, ok := backendrows.Lookup("forge"); ok {
				t.Error("row with an unusable git remote applied")
			}
			if _, ok := backendrows.Lookup("good"); !ok {
				t.Error("good row not applied")
			}
			if backendrows.BaseError("forge") == nil {
				t.Error("BaseError(forge) = nil, want why the row was dropped")
			}
		})
	}
}

// A Load with no sandbox fails closed rather than loading in the Launcher's
// own address space.
func TestLoadWithoutASandboxFailsWithErrNoConnectorSandbox(t *testing.T) {
	t.Chdir(t.TempDir())
	module := filepath.Join(t.TempDir(), "x.wasm")
	if err := os.WriteFile(module, []byte("\x00asm"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := backendrows.Row{Name: "fixture", Module: module, CredentialEnv: "TOKEN"}

	if _, err := backendrows.Load(context.Background(), r, backendrows.Options{}); !errors.Is(err, connectorsandbox.ErrNoConnectorSandbox) {
		t.Fatalf("Load = %v, want ErrNoConnectorSandbox", err)
	}
}
