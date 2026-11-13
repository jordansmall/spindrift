package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/runner"
)

// The files a short-lived Connector load's requests are recorded in, beside the
// shadow Connectors' logs.
const (
	tokenAuditLog = "connector-github-token.log"
	probeAuditLog = "connector-github-probe.log"
)

// connectorClient is the transport the host-side Connector loads send with; nil
// means the default client. It is a test seam, as a real host reaches the real
// API host.
var connectorClient *http.Client

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

// withConnector loads row's Connector bound to repoSlug, hands it and the API
// base URL its first Route names to fn, and closes it after.
func withConnector(row backendrows.Row, o backendrows.Options, repoSlug string, fn func(conn *connectorhost.Connector, api string) error) error {
	if len(row.Routes) == 0 {
		return fmt.Errorf("the %s backend row has no Route, so its API host is unknown", row.Name)
	}
	row.Routes = connectortracker.Routes(row.Routes, repoSlug)
	ctx := context.Background()
	conn, err := backendrows.Load(ctx, row, o)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	return fn(conn, "https://"+row.Routes[0].Host)
}

// introspectViaConnector loads row's Connector with token as its credential,
// never the host's own, and reads the grant through it. A Connector that does
// not declare forge.tokenGrant cannot introspect: the error wraps
// runner.ErrUnsupported.
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
	return got, unsupportedWithoutTokenGrant(err)
}

// probeViaConnector loads row's Connector with the host's own credential and
// resolves repoSlug through it, answering as the native Probe does: auth
// failures, rate limits and every other refusal stay distinguishable, and a
// Connector without forge.probe is unsupported.
func probeViaConnector(row backendrows.Row, o backendrows.Options, repoSlug string) (slug string, err error) {
	err = withConnector(row, o, repoSlug, func(conn *connectorhost.Connector, api string) error {
		var rerr error
		slug, rerr = connectorforge.Probe(conn, connectorforge.Target{API: api, Repo: repoSlug})
		return rerr
	})
	if err != nil {
		return "", classifyProbeErr(unsupportedWithoutProbe(err), repoSlug)
	}
	return slug, nil
}

// classifyProbeErr gives a failed probe the sentinels the native Probe does:
// anything but an auth failure, a rate limit or an unsupported Connector means
// the repository is unreachable.
func classifyProbeErr(err error, repoSlug string) error {
	if errors.Is(err, forge.ErrAuthFailure) || errors.Is(err, forge.ErrRateLimit) || errors.Is(err, runner.ErrUnsupported) {
		return err
	}
	return fmt.Errorf("%w: probe of %s: %w", forge.ErrRepoNotFound, repoSlug, err)
}

// unsupportedWithoutProbe maps a Connector that does not declare forge.probe to
// runner.ErrUnsupported, and passes any other error through.
func unsupportedWithoutProbe(err error) error {
	if errors.Is(err, connectorforge.ErrNoProbe) {
		return fmt.Errorf("the %s connector cannot resolve the repository: %w: %w", backend.GitHub.Name, err, runner.ErrUnsupported)
	}
	return err
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
