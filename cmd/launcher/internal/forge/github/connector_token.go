package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/runner"
)

// tokenAuditLog is the file the token introspection's Connector requests are
// recorded in, beside the shadow Connectors' logs.
const tokenAuditLog = "connector-github-token.log"

// introspectToken is the Strategy's IntrospectToken. With a github backend row
// the row's Connector answers, so the host needs no gh; backend rows are
// opt-in, so without one the native gh probe does and the read-only token gate
// keeps detecting a write-capable token. A Connector load is short-lived, with
// its audit record in the host's log directory.
func introspectToken(token, repoSlug string) (backendkit.TokenIntrospection, error) {
	row, ok := backendrows.Lookup(backend.GitHub.Name)
	if !ok {
		return nativeIntrospect(token, repoSlug)
	}
	audit, closeAudit := openTokenAudit()
	defer closeAudit()
	return introspectViaConnector(row, backendrows.Options{Audit: audit}, token, repoSlug)
}

var nativeIntrospect = newTokenIntrospector(TokenOAuthScopes, TokenRepoPushPermission)

// openTokenAudit opens the audit file for appending. A record that cannot be
// written must not fail the check, so an unopenable file discards, as the
// shadow logs do.
func openTokenAudit() (io.Writer, func()) {
	pwd, err := os.Getwd()
	if err != nil {
		return io.Discard, func() {}
	}
	dir := hostpaths.LogDir(pwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return io.Discard, func() {}
	}
	f, err := os.OpenFile(filepath.Join(dir, tokenAuditLog), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return io.Discard, func() {}
	}
	return f, func() { _ = f.Close() }
}

// introspectViaConnector loads row's Connector with token as its credential,
// never the host's own, and reads the grant through it. A Connector that does
// not declare forge.tokenGrant cannot introspect: the error wraps
// runner.ErrUnsupported.
func introspectViaConnector(row backendrows.Row, o backendrows.Options, token, repoSlug string) (backendkit.TokenIntrospection, error) {
	name := backend.GitHub.Name
	if len(row.Routes) == 0 {
		return backendkit.TokenIntrospection{}, fmt.Errorf("the %s backend row has no Route, so its API host is unknown", name)
	}
	row.Routes = connectortracker.Routes(row.Routes, repoSlug)
	o.Getenv = func(k string) string {
		if k == row.CredentialEnv {
			return token
		}
		return ""
	}
	ctx := context.Background()
	conn, err := backendrows.Load(ctx, row, o)
	if err != nil {
		return backendkit.TokenIntrospection{}, err
	}
	defer func() { _ = conn.Close(ctx) }()
	got, err := connectorforge.IntrospectToken(conn, connectorforge.Target{API: "https://" + row.Routes[0].Host, Repo: repoSlug}, token)
	return got, unsupportedWithoutTokenGrant(err)
}

// unsupportedWithoutTokenGrant maps a Connector that does not declare
// forge.tokenGrant to runner.ErrUnsupported, and passes any other error
// through: a refused read is a failed introspection, not an unsupported one.
func unsupportedWithoutTokenGrant(err error) error {
	if errors.Is(err, connectorforge.ErrNoTokenGrant) {
		return fmt.Errorf("the %s connector cannot introspect the token: %w: %w", backend.GitHub.Name, err, runner.ErrUnsupported)
	}
	return err
}
