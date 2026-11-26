//go:build integration

package main

import (
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
)

// fakeGithub is a TLS stand-in for api.github.com holding one open issue, #7.
// A tracker-mode run reads and settles through the compiled github Connector in
// the sandbox, so what reaches this server is the proof its writes crossed the
// sandbox.
type fakeGithub struct {
	srv *httptest.Server
	// certFile is the PEM the launcher trusts for the server (SSL_CERT_FILE).
	certFile string

	mu     sync.Mutex
	labels []string
	reqs   []string
}

// labelPosts is every label the Connector added to #7, in arrival order.
func (g *fakeGithub) labelPosts() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.labels...)
}

// requests is every request the server saw, for a failure's context.
func (g *fakeGithub) requests() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return strings.Join(g.reqs, "\n")
}

func startFakeGithub(t *testing.T) *fakeGithub {
	t.Helper()
	if runtime.GOOS == "darwin" {
		// crypto/x509 reads SSL_CERT_FILE only on Linux and the BSDs; darwin
		// verifies against the keychain, so the Connector host rejects the cert.
		t.Skip("darwin ignores SSL_CERT_FILE: the Connector host cannot trust the fake github")
	}
	g := &fakeGithub{}
	issue := map[string]any{
		"number": 7, "title": "T", "state": "open", "body": "B",
		"labels":   []map[string]string{{"name": "ready-for-agent"}},
		"html_url": "https://github.com/" + smokeRepo + "/issues/7",
		"user":     map[string]string{"login": "seam"},
	}
	g.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("fake github: reading %s %s: %v", r.Method, r.URL.RequestURI(), err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		g.reqs = append(g.reqs, r.Method+" "+r.URL.RequestURI())
		g.mu.Unlock()
		var reply any
		switch p := r.URL.Path; {
		case r.Method == http.MethodGet && p == "/repos/"+smokeRepo+"/issues":
			reply = []any{issue}
		case r.Method == http.MethodGet && p == "/repos/"+smokeRepo+"/issues/7":
			reply = issue
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/labels"):
			reply = []map[string]string{{"name": "ready-for-agent"}, {"name": "agent-in-progress"}, {"name": "agent-complete"}, {"name": "agent-failed"}}
		case r.Method == http.MethodPost && p == "/repos/"+smokeRepo+"/issues/7/labels":
			var in struct {
				Labels []string `json:"labels"`
			}
			if err := json.Unmarshal(body, &in); err != nil {
				t.Errorf("fake github: label POST body %q: %v", body, err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			g.mu.Lock()
			g.labels = append(g.labels, in.Labels...)
			g.mu.Unlock()
			reply = []any{}
		case r.Method == http.MethodPost && p == "/graphql":
			// No label ids, so an edit issues no mutation.
			reply = map[string]any{"data": map[string]any{"repository": map[string]any{"issue": map[string]string{"id": "I"}}}}
		case r.Method == http.MethodGet:
			reply = []any{}
		default:
			reply = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(reply)
	}))
	t.Cleanup(g.srv.Close)
	g.certFile = filepath.Join(t.TempDir(), "fake-github.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: g.srv.Certificate().Raw})
	if err := os.WriteFile(g.certFile, pemBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	return g
}

// seamGithubBaseEnv is the knob naming the fake backend's base URL. The real
// launcher admits a literal loopback Route host only through a knob the
// Operator set, so the test names the fake that way rather than in the Routes.
const seamGithubBaseEnv = "SPINDRIFT_SEAM_GITHUB_BASE"

// compiledGithubRow is the compiled github Connector as a backend row whose
// Routes take their host from seamGithubBaseEnv, skipping the test when
// nix/checks/go.nix exported none.
func compiledGithubRow(t *testing.T) map[string]any {
	t.Helper()
	wasm := os.Getenv(connectorWasmEnv)
	if wasm == "" {
		t.Skipf("%s unset: no compiled github Connector to test", connectorWasmEnv)
	}
	var routes []connectorhost.Route
	if err := json.Unmarshal([]byte(os.Getenv(connectorRoutesEnv)), &routes); err != nil {
		t.Fatalf("%s: %v", connectorRoutesEnv, err)
	}
	for i := range routes {
		routes[i].Host = backendrows.BaseHost
	}
	return map[string]any{"name": "github", "module": wasm, "credentialEnv": "GH_TOKEN", "baseEnv": seamGithubBaseEnv, "routes": routes}
}

// The compiled github Connector and its Route table, as nix/checks/go.nix
// exports them.
const (
	connectorWasmEnv   = "SPINDRIFT_BACKEND_CONNECTOR_WASM"
	connectorRoutesEnv = "SPINDRIFT_BACKEND_CONNECTOR_ROUTES"
)

// hostDeathMarkers are what the launcher reports when the Connector host
// dies under a call (issue #7302).
var hostDeathMarkers = []string{"connector host exited", "unexpected EOF"}

// assertSandboxSurvivedSettle fails if out reports the Connector host died
// before the run settled.
func assertSandboxSurvivedSettle(t *testing.T, label, out string) {
	t.Helper()
	for _, died := range hostDeathMarkers {
		if strings.Contains(out, died) {
			t.Errorf("%s carries %q: the Connector host died before the run settled", label, died)
		}
	}
}
