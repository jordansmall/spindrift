package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
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

func noGithubRow(t *testing.T) {
	t.Helper()
	t.Cleanup(backendrows.Snapshot())
	if err := backendrows.Use(nil, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}

// With no github row configured the introspection is the native gh probe, so a
// host without a Connector keeps detecting a write-capable token.
func TestIntrospectToken_NoRowFallsBackToNativeGh(t *testing.T) {
	noGithubRow(t)
	str := func(s string) *string { return &s }
	for _, tc := range []struct {
		name, token string
		scopes      *string
		repoBody    string
		want        backendkit.TokenIntrospection
	}{
		{name: "classic PAT with repo", token: "ghp_abc", scopes: str("read:org, repo"),
			want: backendkit.TokenIntrospection{Introspectable: true, WriteCapable: true, Scopes: []string{"read:org", "repo"}}},
		{name: "classic PAT read-only", token: "gho_abc", scopes: str("read:org"),
			want: backendkit.TokenIntrospection{Introspectable: true, Scopes: []string{"read:org"}}},
		{name: "installation token with push", token: "ghs_abc", repoBody: `{"permissions":{"push":true}}`,
			want: backendkit.TokenIntrospection{Introspectable: true, WriteCapable: true}},
		{name: "fine-grained PAT", token: "github_pat_abc",
			want: backendkit.TokenIntrospection{Reason: notIntrospectableReason}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ghTokenStub(t, 0, tc.scopes, tc.repoBody)
			got, err := introspectToken(tc.token, connectorRepo)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Errorf("introspectToken = (%+v, %v), want %+v", got, err, tc.want)
			}
		})
	}
}

// ghTokenStub scripts the gh the native introspector runs: `api -i user`
// answers with scopes (or userStatus 401), `api repos/<slug>` with repoBody.
func ghTokenStub(t *testing.T, userStatus int, scopes *string, repoBody string) {
	t.Helper()
	header := ""
	if scopes != nil {
		header = "X-OAuth-Scopes: " + *scopes + `\n`
	}
	prependFakeGH(t, `case "$*" in
"api -i user")
	[ `+strconv.Itoa(userStatus)+` = 401 ] && { echo "HTTP 401: Bad credentials" >&2; exit 1; }
	printf 'HTTP/2.0 200 OK\n`+header+`\n{}' ;;
"api repos/`+connectorRepo+`")
	printf '%s' '`+repoBody+`' ;;
esac`)
}

// A shadow github row never routes the token gate: gh stays authoritative, and
// the shadow compares gh against the Connector.
func TestIntrospectToken_ShadowRowRunsNative(t *testing.T) {
	b := newTokenBackend(t, "ghp_abc")
	useGithubRowShadow(t, b.Listener.Addr().String())
	var native bool
	old := nativeIntrospect
	nativeIntrospect = func(string, string) (backendkit.TokenIntrospection, error) {
		native = true
		return backendkit.TokenIntrospection{}, nil
	}
	t.Cleanup(func() { nativeIntrospect = old })

	_, _ = introspectToken("ghp_abc", connectorRepo)

	if !native || len(b.seenAuth) != 0 {
		t.Errorf("native introspection ran = %v, backend requests = %d; want gh to answer, not the Connector", native, len(b.seenAuth))
	}
}
