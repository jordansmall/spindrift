package github

import (
	"net/http"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/forge"
)

// connectorClient is the transport the host-side Connector loads send with; nil
// means the default client. It is a test seam, as a real host reaches the real
// API host.
var connectorClient *http.Client

// introspectToken is the Strategy's IntrospectToken. The github backend row's
// Connector answers, so the host needs no gh; without a routed row it fails
// with ErrNoRoute, as every read does. A Connector load is short-lived,
// with its audit record in the run's Connector record.
func introspectToken(token, repoSlug string) (forge.TokenIntrospection, error) {
	row, err := routeRow()
	if err != nil {
		return forge.TokenIntrospection{}, err
	}
	got, err := introspect(row, backendrows.Options{Client: connectorClient}, token, repoSlug)
	return got, noteRouted("introspect the token", err)
}

// introspect loads row's Connector with token as its credential,
// never the host's own, and reads the grant through it. The load is a
// short-lived runtime of its own, as a session keeps the Options of the call
// that opened it and the host's would carry the host's credential. A Connector
// that does not declare forge.tokenGrant cannot introspect: the error wraps
// backendkit.ErrUnsupported.
func introspect(row backendrows.Row, o backendrows.Options, token, repoSlug string) (got forge.TokenIntrospection, err error) {
	row, target, err := routeTarget(row, repoSlug)
	if err != nil {
		return got, err
	}
	o.Getenv = func(k string) string {
		if k == row.CredentialEnv {
			return token
		}
		return ""
	}
	rt := connectorruntime.New([]backendrows.Row{row}, nil)
	defer func() { _ = rt.Close() }()
	// introspectToken notes a failure once, under its own verb.
	conn, err := rt.Connector(row, connectorruntime.SessionSpec{Target: target, Record: backend.GitHub.Name, Options: o, NotesOperations: true})
	if err == nil {
		got, err = connectorforge.IntrospectToken(conn, connectorforge.Target{API: target.API, Repo: repoSlug}, token)
	}
	return got, unsupportedWithout(err, "introspect the token", connectorforge.ErrNoTokenGrant)
}
