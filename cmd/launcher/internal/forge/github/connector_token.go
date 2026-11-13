package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/hostpaths"
)

// tokenAuditLog is the file a short-lived Connector load's requests are
// recorded in, beside the shadow Connectors' logs.
const tokenAuditLog = "connector-github-token.log"

// connectorClient is the transport the host-side Connector loads send with; nil
// means the default client. It is a test seam, as a real host reaches the real
// API host.
var connectorClient *http.Client

// introspectToken is the Strategy's IntrospectToken. With a github backend row
// the row's Connector answers, so the host needs no gh; backend rows are
// opt-in, so without one (or under a shadow row) the native gh probe does and
// the read-only token gate keeps detecting a write-capable token. A Connector
// load is short-lived, with its audit record in the host's log directory.
func introspectToken(token, repoSlug string) (backendkit.TokenIntrospection, error) {
	row, ok := routedRow()
	if !ok {
		return nativeIntrospect(token, repoSlug)
	}
	audit, closeAudit := openAudit(tokenAuditLog)
	defer closeAudit()
	return introspectViaConnector(row, backendrows.Options{Audit: audit, Client: connectorClient}, token, repoSlug)
}

var nativeIntrospect = newTokenIntrospector(TokenOAuthScopes, TokenRepoPushPermission)

// openAudit opens the named audit file for appending. A record that cannot be
// written must not fail the check, so an unopenable file discards, as the
// shadow logs do.
func openAudit(name string) (io.Writer, func()) {
	pwd, err := os.Getwd()
	if err != nil {
		return io.Discard, func() {}
	}
	dir := hostpaths.LogDir(pwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return io.Discard, func() {}
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return io.Discard, func() {}
	}
	return f, func() { _ = f.Close() }
}

// loadConnector loads row's Connector bound to repoSlug, and returns it with
// the API base URL its first Route names.
func loadConnector(row backendrows.Row, o backendrows.Options, repoSlug string) (*connectorhost.Connector, string, error) {
	if len(row.Routes) == 0 {
		return nil, "", fmt.Errorf("the %s backend row has no Route, so its API host is unknown", row.Name)
	}
	row.Routes = connectortracker.Routes(row.Routes, repoSlug)
	conn, err := loadRowConnector(context.Background(), row, o)
	if err != nil {
		return nil, "", err
	}
	return conn, "https://" + row.Routes[0].Host, nil
}

// withConnector loads row's Connector, hands it and its API base URL to fn, and
// closes it after.
func withConnector(row backendrows.Row, o backendrows.Options, repoSlug string, fn func(conn *connectorhost.Connector, api string) error) error {
	conn, api, err := loadConnector(row, o, repoSlug)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	return fn(conn, api)
}

// introspectViaConnector loads row's Connector with token as its credential,
// never the host's own, and reads the grant through it. A Connector that does
// not declare forge.tokenGrant cannot introspect: the error wraps
// backendkit.ErrUnsupported.
func introspectViaConnector(row backendrows.Row, o backendrows.Options, token, repoSlug string) (got backendkit.TokenIntrospection, err error) {
	o.Getenv = func(k string) string {
		if k == row.CredentialEnv {
			return token
		}
		return ""
	}
	err = withConnector(row, o, repoSlug, func(conn *connectorhost.Connector, api string) error {
		var rerr error
		got, rerr = connectorforge.IntrospectToken(conn, connectorforge.Target{API: api, Repo: repoSlug}, token)
		return rerr
	})
	return got, unsupportedWithout(err, "introspect the token", connectorforge.ErrNoTokenGrant)
}
