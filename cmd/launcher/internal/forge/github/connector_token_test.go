package github

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostpaths"
)

// useGithubRow applies a github backend row whose Connector reaches b, as a
// document's row would, and restores the applied set afterwards.
func useGithubRow(t *testing.T, b *tokenBackend) {
	t.Helper()
	useGithubRowAt(t, b.Listener.Addr().String())
}

// useGithubRowAt is useGithubRow for a backend at host.
func useGithubRowAt(t *testing.T, host string) { applyGithubRow(t, host, func(*backendrows.Row) {}) }

func applyGithubRow(t *testing.T, host string, adjust func(*backendrows.Row)) {
	t.Helper()
	path, routes := githubConnectorRoutes(t, host, connectorRepo)
	row := backendrows.Row{Name: "github", Module: path, Routes: routes, CredentialEnv: tokenCredentialEnv, GitRemote: "https://" + host}
	adjust(&row)
	doc, err := json.Marshal([]backendrows.Row{row})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backendrows.Snapshot())
	if err := backendrows.Use(doc, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}

// The Strategy's introspection loads the backend row's Connector with the
// token under audit as its credential, records the requests it made, and never
// runs gh.
func TestIntrospect_ReportsTheGrantAndAuditsTheRequests(t *testing.T) {
	scopes := "read:org, repo"
	b := newTokenBackend(t, "ghp_abc")
	b.scopes = &scopes
	useGithubRow(t, b)
	calls := countGH(t)
	auditPath := chdirAuditPath(t)

	got, err := introspect(testSandbox(), githubRow(t), backendrows.Options{Client: b.Client(), PublicForTest: connectorhost.LoopbackForTest}, "ghp_abc", connectorRepo)

	want := forge.TokenIntrospection{Introspectable: true, WriteCapable: true, Scopes: []string{"read:org", "repo"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("introspect = (%+v, %v), want %+v", got, err, want)
	}
	if n := calls(); n != 0 {
		t.Errorf("introspection ran gh %d times", n)
	}
	if len(b.seenAuth) == 0 || b.seenAuth[0] != "Bearer ghp_abc" {
		t.Errorf("backend saw Authorization %v, want the token under audit", b.seenAuth)
	}
	raw, rerr := os.ReadFile(auditPath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	audit := string(raw)
	if !strings.Contains(audit, "/graphql") || strings.Contains(audit, "ghp_abc") {
		t.Errorf("audit = %q, want the /graphql request and never the token", audit)
	}
}

// The host's own credential is never the one audited: the Connector is loaded
// with the token under audit even when the row's env var holds another.
func TestIntrospect_IgnoresTheHostsCredential(t *testing.T) {
	t.Setenv(tokenCredentialEnv, "ghp_hosts_own")
	chdirAuditPath(t)
	b := newTokenBackend(t, "ghp_abc")
	useGithubRow(t, b)

	if _, err := introspect(testSandbox(), githubRow(t), backendrows.Options{Client: b.Client(), PublicForTest: connectorhost.LoopbackForTest}, "ghp_abc", connectorRepo); err != nil {
		t.Fatal(err)
	}
	for _, a := range b.seenAuth {
		if a != "Bearer ghp_abc" {
			t.Errorf("backend saw Authorization %q, want the token under audit", a)
		}
	}
}

// A refused read is a failed introspection, not an unsupported one.
func TestIntrospect_RefusedReadIsAnError(t *testing.T) {
	chdirAuditPath(t)
	b := newTokenBackend(t, "ghp_abc")
	b.userStatus = http.StatusUnauthorized
	useGithubRow(t, b)
	_, err := introspect(testSandbox(), githubRow(t), backendrows.Options{Client: b.Client(), PublicForTest: connectorhost.LoopbackForTest}, "ghp_abc", connectorRepo)
	if err == nil || errors.Is(err, backendkit.ErrUnsupported) {
		t.Fatalf("err = %v, want a non-unsupported error", err)
	}
}

// githubRow is the github row useGithubRow applied.
func githubRow(t *testing.T) backendrows.Row {
	t.Helper()
	row, ok := backendrows.Lookup("github")
	if !ok {
		t.Fatal("no github backend row applied")
	}
	return row
}

// ghTokenWriteScopes are the classic/OAuth scopes that grant repo write access;
// the Connector carries its own copy, which TestConnector_TokenGrantWriteScopes
// pins.
var ghTokenWriteScopes = map[string]bool{
	"repo":        true,
	"public_repo": true,
}

// notIntrospectableReason is the Connector write gate's explanation for
// a GitHub token whose grants cannot be determined.
const notIntrospectableReason = "e.g. a fine-grained PAT, whose granted permissions GitHub exposes no endpoint to introspect"

// chdirAuditPath runs the test in a fresh directory, where the Connector load
// opens its audit file, and returns that file's path.
func chdirAuditPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	return filepath.Join(hostpaths.LogDir(dir), connectorrecord.Name(backend.GitHub.Name, ".audit.jsonl"))
}

// A Connector never runs unrecorded: an audit file that will not open fails the
// load with its path, and nothing reaches the backend.
func TestIntrospect_UnopenableAuditFileFailsTheLoad(t *testing.T) {
	b := newTokenBackend(t, "ghp_abc")
	useGithubRow(t, b)
	auditPath := chdirAuditPath(t)
	logDir := filepath.Dir(auditPath)
	if err := os.MkdirAll(filepath.Dir(logDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logDir, nil, 0o644); err != nil { // a file where the log directory belongs
		t.Fatal(err)
	}

	_, err := introspect(testSandbox(), githubRow(t), backendrows.Options{Client: b.Client(), PublicForTest: connectorhost.LoopbackForTest}, "ghp_abc", connectorRepo)

	if err == nil || !strings.Contains(err.Error(), auditPath) {
		t.Fatalf("err = %v, want one naming %s", err, auditPath)
	}
	if len(b.seenAuth) != 0 {
		t.Errorf("backend saw %d requests, want none", len(b.seenAuth))
	}
}
