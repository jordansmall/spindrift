package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/github"
)

// probeSlug is the repository the scripted API resolves any probe to.
const probeSlug = "jordansmall/spindrift"

// githubAPI scripts the GitHub GraphQL endpoint the Connector reads: the
// repository-identity query (Probe) and the viewer query whose X-OAuth-Scopes
// header is a classic token's grant. It records each Authorization header.
type githubAPI struct {
	*httptest.Server
	mu          sync.Mutex
	scopes      string
	viewerCode  int
	probeCode   int
	seenAuth    []string
	unexpectedQ []string
}

func newGithubAPI() *githubAPI {
	a := &githubAPI{scopes: strings.Join(narrowTokenScopes, ", "), viewerCode: http.StatusOK, probeCode: http.StatusOK}
	a.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.seenAuth = append(a.seenAuth, r.Header.Get("Authorization"))
		if r.Method == http.MethodGet {
			a.serveREST(w, r)
			return
		}
		var req struct{ Query string }
		if r.Method == http.MethodPost && r.URL.Path == "/graphql" {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		switch {
		case strings.Contains(req.Query, "nameWithOwner"):
			if a.probeCode != http.StatusOK {
				w.WriteHeader(a.probeCode)
				return
			}
			_, _ = fmt.Fprintf(w, `{"data":{"repository":{"url":"https://github.com/%[1]s","nameWithOwner":"%[1]s","autoMergeAllowed":false}}}`, probeSlug)
		case strings.Contains(req.Query, "viewer"):
			w.Header().Set("X-OAuth-Scopes", a.scopes)
			w.WriteHeader(a.viewerCode)
			_, _ = w.Write([]byte(`{"data":{"viewer":{"login":"bot"}}}`))
		default:
			a.unexpectedQ = append(a.unexpectedQ, r.Method+" "+r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	return a
}

// auths returns the Authorization headers seen so far.
func (a *githubAPI) auths() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.seenAuth...)
}

// set mutates the script under the lock and restores it when the test ends.
func (a *githubAPI) set(t *testing.T, mutate func(*githubAPI)) {
	t.Helper()
	a.mu.Lock()
	scopes, viewer, probe := a.scopes, a.viewerCode, a.probeCode
	mutate(a)
	a.mu.Unlock()
	t.Cleanup(func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.scopes, a.viewerCode, a.probeCode = scopes, viewer, probe
	})
}

// rowEnvAt returns the env useBakedGithubRow reads, with the compiled
// Connector's Route table pointed at host, as a real host reaches
// api.github.com. ok is false when no compiled Connector is configured.
func rowEnvAt(host string) (getenv func(string) string, ok bool, err error) {
	wasm := os.Getenv(connectorWasmEnv)
	if wasm == "" {
		return nil, false, nil
	}
	var table []connectorhost.Route
	if err := json.Unmarshal([]byte(os.Getenv(connectorRoutesEnv)), &table); err != nil {
		return nil, false, fmt.Errorf("%s: %w", connectorRoutesEnv, err)
	}
	for i := range table {
		if table[i].Host != "api.github.com" {
			return nil, false, fmt.Errorf("route %s %s names host %q, want api.github.com", table[i].Method, table[i].Path, table[i].Host)
		}
		table[i].Host = host
	}
	routes, err := json.Marshal(table)
	if err != nil {
		return nil, false, err
	}
	return func(k string) string {
		switch k {
		case connectorWasmEnv:
			return wasm
		case connectorRoutesEnv:
			return string(routes)
		}
		return ""
	}, true, nil
}

// The package runs in one of two modes. With a compiled github Connector
// (the wasm and routes env nix/checks/go.nix sets) every test reads GitHub
// through the baked row against defaultAPI, and ghStubDir holds a gh that
// records its calls and fails. Without one the row is absent and ghStubDir
// holds a gh answering the scope audit, so the tests that only need an audited
// token still run.
var (
	defaultAPI *githubAPI
	ghStubDir  string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "quickstart-gh")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ghStubDir = dir
	code := runMain(m)
	os.RemoveAll(dir)
	os.Exit(code)
}

func runMain(m *testing.M) int {
	script := "#!/bin/sh\nprintf 'HTTP/2.0 200 OK\\nX-OAuth-Scopes: public_repo\\n\\n{}'\n"
	api := newGithubAPI()
	defer api.Close()
	getenv, ok, err := rowEnvAt(api.Listener.Addr().String())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if ok {
		script = "#!/bin/sh\necho call >> '" + filepath.Join(ghStubDir, "calls") + "'\nexit 99\n"
		defaultAPI = api
		if err := useBakedGithubRow(getenv); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer github.ConnectorClientForTest(api.Client())()
	}
	if err := os.WriteFile(filepath.Join(ghStubDir, "gh"), []byte(script), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	os.Setenv("PATH", ghStubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return m.Run()
}

// ghCalls counts the gh invocations so far; it only counts in Connector mode.
func ghCalls() int {
	b, _ := os.ReadFile(filepath.Join(ghStubDir, "calls"))
	return strings.Count(string(b), "call")
}

// requireConnector skips a test that needs the compiled github Connector.
func requireConnector(t *testing.T) *githubAPI {
	t.Helper()
	if defaultAPI == nil {
		t.Skipf("%s unset: no compiled github Connector to test", connectorWasmEnv)
	}
	return defaultAPI
}

// ghScopes makes the wizard's scope audit read scopes: from the Connector's
// backend when one is configured, otherwise from a gh on PATH.
func ghScopes(t *testing.T, scopes []string) {
	t.Helper()
	joined := strings.Join(scopes, ", ")
	if defaultAPI != nil {
		defaultAPI.set(t, func(a *githubAPI) { a.scopes = joined })
		return
	}
	fakeGH(t, "printf 'HTTP/2.0 200 OK\\nX-OAuth-Scopes: "+joined+"\\n\\n{}'")
}

// ghFailing makes every scope read fail.
func ghFailing(t *testing.T) {
	t.Helper()
	if defaultAPI != nil {
		defaultAPI.set(t, func(a *githubAPI) { a.viewerCode = http.StatusUnauthorized })
		return
	}
	fakeGH(t, "echo 'HTTP 401: Bad credentials' >&2; exit 1")
}

func fakeGH(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A build with nothing baked in leaves quickstart on the native gh.
func TestUseBakedGithubRow_NothingBakedAppliesNoRow(t *testing.T) {
	t.Cleanup(backendrows.Snapshot())
	if err := backendrows.Use(nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := useBakedGithubRow(func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	if _, ok := backendrows.Lookup("github"); ok {
		t.Error("a github row is applied with no Connector baked in")
	}
}

func TestUseBakedGithubRow_RefusesMalformedRoutes(t *testing.T) {
	t.Cleanup(backendrows.Snapshot())
	env := map[string]string{connectorWasmEnv: "/x/github.wasm", connectorRoutesEnv: "not json"}
	err := useBakedGithubRow(func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), connectorRoutesEnv) {
		t.Fatalf("useBakedGithubRow = %v, want an error naming %s", err, connectorRoutesEnv)
	}
}

// A half-baked build (one var without the other) is a wiring bug: it names the
// missing variable rather than applying a row with no module or no routes.
func TestUseBakedGithubRow_RefusesOneVarWithoutTheOther(t *testing.T) {
	for _, tc := range []struct{ set, missing string }{
		{connectorRoutesEnv, connectorWasmEnv},
		{connectorWasmEnv, connectorRoutesEnv},
	} {
		t.Run(tc.missing, func(t *testing.T) {
			t.Cleanup(backendrows.Snapshot())
			if err := backendrows.Use(nil, nil); err != nil {
				t.Fatal(err)
			}
			env := map[string]string{tc.set: "[]"}
			err := useBakedGithubRow(func(k string) string { return env[k] })
			if err == nil || !strings.Contains(err.Error(), tc.missing) {
				t.Fatalf("useBakedGithubRow = %v, want an error naming %s", err, tc.missing)
			}
			if _, ok := backendrows.Lookup("github"); ok {
				t.Error("a github row is applied from half a bake")
			}
		})
	}
}

// The baked row names the wizard's token as the Connector's credential.
func TestUseBakedGithubRow_AppliesTheGithubRow(t *testing.T) {
	t.Cleanup(backendrows.Snapshot())
	env := map[string]string{
		connectorWasmEnv:   "/x/github.wasm",
		connectorRoutesEnv: `[{"method":"POST","host":"api.github.com","path":"/graphql"}]`,
	}
	if err := useBakedGithubRow(func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
	row, ok := backendrows.Lookup("github")
	if !ok || row.Module != "/x/github.wasm" || row.CredentialEnv != "GH_TOKEN" || len(row.Routes) != 1 {
		t.Errorf("row = %+v, %v; want the baked module, GH_TOKEN and the one route", row, ok)
	}
}

func githubStrategy(t *testing.T) backendkit.Strategy {
	t.Helper()
	s, ok := backendkit.ByName("github")
	if !ok {
		t.Fatal("github backend not registered")
	}
	return s
}

// The identity probe the wizard's forge build makes goes through the Connector
// with the wizard's token, and never runs gh.
func TestQuickstartProbe_GoesThroughTheConnectorWithTheWizardsToken(t *testing.T) {
	api := requireConnector(t)
	for _, tc := range []struct {
		code    int
		wantErr error
	}{
		{code: http.StatusOK},
		{code: http.StatusUnauthorized, wantErr: forge.ErrAuthFailure},
		{code: http.StatusNotFound, wantErr: forge.ErrRepoNotFound},
	} {
		t.Run(fmt.Sprint(tc.code), func(t *testing.T) {
			api.set(t, func(a *githubAPI) { a.probeCode = tc.code; a.seenAuth = nil })
			// runQuickstart exports the wizard's token as GH_TOKEN before it builds the forge.
			t.Setenv("GH_TOKEN", "ghp_wizardtoken")
			before := ghCalls()

			it, _, err := buildForge(githubStrategy(t), quickstartShared(probeSlug, "Ada", "ada@example.com"),
				knobLookup(nil, "GH_TOKEN", "ghp_wizardtoken"))
			if err != nil {
				t.Fatal(err)
			}
			slug, err := it.Probe()

			if tc.wantErr == nil && (err != nil || slug != probeSlug) {
				t.Errorf("Probe = (%q, %v), want (%q, nil)", slug, err, probeSlug)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("Probe error = %v, want %v", err, tc.wantErr)
			}
			if got := ghCalls() - before; got != 0 {
				t.Errorf("Probe ran gh %d times", got)
			}
			if a := api.auths(); len(a) == 0 || a[0] != "Bearer ghp_wizardtoken" {
				t.Errorf("backend saw Authorization %v, want the wizard's token", a)
			}
		})
	}
}

// The scope audit reads the pasted token's grant through the Connector, with
// that token and not the ambient GH_TOKEN, and never runs gh.
func TestQuickstartScopeAudit_GoesThroughTheConnectorWithThePastedToken(t *testing.T) {
	api := requireConnector(t)
	api.set(t, func(a *githubAPI) { a.scopes = "repo, gist"; a.seenAuth = nil })
	t.Setenv("GH_TOKEN", "")
	before := ghCalls()
	dir := t.TempDir()
	var out bytes.Buffer
	stdin := strings.NewReader(strings.Join([]string{
		"jordansmall/spindrift", "podman", "Ada Lovelace", "ada@example.com",
		"ghp_pastedtoken", "ACCEPT", "claude-oauth-faketoken",
	}, "\n") + "\n")

	env := fakeEnvironment{runtimes: map[string]bool{"podman": true}}
	if err := runQuickstart(dir, env, &fakeCommandRunner{}, fakeForgeBuilder(passingForge()), &out, stdin, true, false); err != nil {
		t.Fatalf("runQuickstart: %v", err)
	}

	if !strings.Contains(out.String(), "repo") {
		t.Errorf("transcript does not name the over-broad scope:\n%s", out.String())
	}
	if got := ghCalls() - before; got != 0 {
		t.Errorf("the scope audit ran gh %d times", got)
	}
	auths := api.auths()
	if len(auths) == 0 {
		t.Fatal("the backend saw no request")
	}
	for _, a := range auths {
		if a != "Bearer ghp_pastedtoken" {
			t.Errorf("backend saw Authorization %q, want the pasted token", a)
		}
	}
}

// serveREST answers the REST reads a doctor pass makes: an empty issue list,
// a protected base branch and the lifecycle labels.
func (a *githubAPI) serveREST(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/issues"):
		_, _ = w.Write([]byte(`[]`))
	case strings.HasSuffix(r.URL.Path, "/branches/main/protection"):
		_, _ = w.Write([]byte(`{}`))
	case strings.HasSuffix(r.URL.Path, "/labels"):
		_, _ = w.Write([]byte(`[{"name":"ready-for-agent"},{"name":"agent-in-progress"},{"name":"agent-failed"},{"name":"agent-complete"}]`))
	default:
		a.unexpectedQ = append(a.unexpectedQ, r.Method+" "+r.URL.RequestURI())
		http.NotFound(w, r)
	}
}

// The doctor pass the wizard ends on reads the repository through the baked
// Connector alone: the probe, branch protection, the recoverable-issue list
// and the label registry never run gh.
func TestQuickstartDoctorPass_RunsNoGh(t *testing.T) {
	api := requireConnector(t)
	t.Setenv("GH_TOKEN", "ghp_wizardtoken")
	before := ghCalls()
	it, cf, err := buildForge(githubStrategy(t), quickstartShared(probeSlug, "Ada", "ada@example.com"),
		knobLookup(nil, "GH_TOKEN", "ghp_wizardtoken"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer

	err = doctor.Run(it, cf, doctor.Config{
		IssueTracker:    "github",
		Label:           defaultDispatchLabels.Dispatchable,
		InProgressLabel: defaultDispatchLabels.InProgress,
		FailedLabel:     defaultDispatchLabels.Failed,
		CompleteLabel:   defaultDispatchLabels.Complete,
		MergePolicy:     defaultMergePolicy,
		BaseBranch:      defaultBaseBranch,
	}, doctor.NewReporter(&out, true), bufio.NewScanner(strings.NewReader("")), false, nil)

	if err != nil {
		t.Fatalf("doctor.Run: %v\n%s", err, out.String())
	}
	if got := ghCalls() - before; got != 0 {
		t.Errorf("the doctor pass ran gh %d times", got)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.unexpectedQ) != 0 {
		t.Errorf("the backend saw unscripted requests: %v", api.unexpectedQ)
	}
}
