package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
)

// useGithubRow applies a github backend row whose Connector reaches b, as a
// document's row would, and restores the applied set afterwards.
func useGithubRow(t *testing.T, b *tokenBackend) {
	t.Helper()
	useGithubRowAt(t, b.Listener.Addr().String())
}

// useGithubRowAt is useGithubRow for a backend at host.
func useGithubRowAt(t *testing.T, host string) { applyGithubRow(t, host, func(*backendrows.Row) {}) }

// useGithubRowShadow is useGithubRowAt with the row in shadow mode.
func useGithubRowShadow(t *testing.T, host string) {
	applyGithubRow(t, host, func(r *backendrows.Row) { r.Shadow, r.ShadowEvery = true, 1 })
}

func applyGithubRow(t *testing.T, host string, adjust func(*backendrows.Row)) {
	t.Helper()
	path, routes := githubConnectorRoutes(t, host, connectorRepo)
	row := backendrows.Row{Name: "github", Module: path, Routes: routes, CredentialEnv: tokenCredentialEnv}
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
func TestIntrospectViaConnector_ReportsTheGrantAndAuditsTheRequests(t *testing.T) {
	scopes := "read:org, repo"
	b := newTokenBackend(t, "ghp_abc")
	b.scopes = &scopes
	useGithubRow(t, b)
	calls := countGH(t)
	var audit bytes.Buffer

	got, err := introspectViaConnector(githubRow(t), backendrows.Options{Audit: &audit, Client: b.Client()}, "ghp_abc", connectorRepo)

	want := backendkit.TokenIntrospection{Introspectable: true, WriteCapable: true, Scopes: []string{"read:org", "repo"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("introspectViaConnector = (%+v, %v), want %+v", got, err, want)
	}
	if n := calls(); n != 0 {
		t.Errorf("introspection ran gh %d times", n)
	}
	if len(b.seenAuth) == 0 || b.seenAuth[0] != "Bearer ghp_abc" {
		t.Errorf("backend saw Authorization %v, want the token under audit", b.seenAuth)
	}
	if !strings.Contains(audit.String(), "/graphql") || strings.Contains(audit.String(), "ghp_abc") {
		t.Errorf("audit = %q, want the /graphql request and never the token", audit.String())
	}
}

// The host's own credential is never the one audited: the Connector is loaded
// with the token under audit even when the row's env var holds another.
func TestIntrospectViaConnector_IgnoresTheHostsCredential(t *testing.T) {
	t.Setenv(tokenCredentialEnv, "ghp_hosts_own")
	b := newTokenBackend(t, "ghp_abc")
	useGithubRow(t, b)

	if _, err := introspectViaConnector(githubRow(t), backendrows.Options{Audit: &bytes.Buffer{}, Client: b.Client()}, "ghp_abc", connectorRepo); err != nil {
		t.Fatal(err)
	}
	for _, a := range b.seenAuth {
		if a != "Bearer ghp_abc" {
			t.Errorf("backend saw Authorization %q, want the token under audit", a)
		}
	}
}

// A refused read is a failed introspection, not an unsupported one.
func TestIntrospectViaConnector_RefusedReadIsAnError(t *testing.T) {
	b := newTokenBackend(t, "ghp_abc")
	b.userStatus = http.StatusUnauthorized
	useGithubRow(t, b)
	_, err := introspectViaConnector(githubRow(t), backendrows.Options{Audit: &bytes.Buffer{}, Client: b.Client()}, "ghp_abc", connectorRepo)
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

// notIntrospectableReason is the primary Connector write gate's explanation for
// a GitHub token whose grants cannot be determined.
const notIntrospectableReason = "e.g. a fine-grained PAT, whose granted permissions GitHub exposes no endpoint to introspect"
