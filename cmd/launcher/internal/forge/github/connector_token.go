package github

import (
	"fmt"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/credsource"
	"spindrift.dev/launcher/internal/forge"
)

// connectorOptions carries the transport the host-side Connector loads send
// with and its PublicForTest. It is a test seam, zero in production: a real
// host reaches the real API host through the default client, and a literal
// route host must resolve public.
var connectorOptions backendrows.Options

// introspectToken is the Strategy's IntrospectToken. The github backend row's
// Connector answers, so the host needs no gh; without a routed row it fails
// with ErrNoRoute, as every read does. A Connector load is short-lived,
// with its audit record in the run's Connector record.
func introspectToken(rt *connectorruntime.Runtime, token, repoSlug string) (forge.TokenIntrospection, error) {
	if rt == nil {
		return forge.TokenIntrospection{}, fmt.Errorf("github: %w", connectorruntime.ErrNoRuntime)
	}
	row, err := routeRow(rt)
	if err != nil {
		return forge.TokenIntrospection{}, err
	}
	got, err := introspect(rt.Sandbox(), row, connectorOptions, token, repoSlug)
	return got, noteRouted("introspect the token", err)
}

// introspect loads row's Connector with token as its credential, never the
// host's own, and reads the grant through it. The load is a short-lived runtime
// of its own over sandbox, as a session keeps the Options of the call that
// opened it and the host's would carry the host's credential. A Connector that
// does not declare forge.tokenGrant cannot introspect: the error wraps
// backendkit.ErrUnsupported.
func introspect(sandbox connectorsandbox.Loader, row backendrows.Row, o backendrows.Options, token, repoSlug string) (got forge.TokenIntrospection, err error) {
	row, target, err := routeTarget(row, repoSlug)
	if err != nil {
		return got, err
	}
	rt := connectorruntime.New(backendrows.NewSet(row), map[string]credsource.Row{row.Name: {Secret: credsource.Static(token)}}, sandbox)
	defer func() { _ = rt.Close() }()
	// introspectToken notes a failure once, under its own verb.
	conn, err := rt.Connector(row, connectorruntime.SessionSpec{Target: target, Record: backend.GitHub.Name, Options: o, NotesOperations: true})
	if err == nil {
		got, err = connectorforge.IntrospectToken(conn, connectorforge.Target{API: target.API, Repo: repoSlug}, token)
	}
	return got, unsupportedWithout(err, "introspect the token", connectorforge.ErrNoTokenGrant)
}
