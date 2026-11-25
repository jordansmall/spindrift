package github

import (
	"context"
	"fmt"
	"net/http"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectortracker"
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

// introspect loads row's Connector with token as its credential,
// never the host's own, and reads the grant through it. A Connector that does
// not declare forge.tokenGrant cannot introspect: the error wraps
// backendkit.ErrUnsupported.
func introspect(row backendrows.Row, o backendrows.Options, token, repoSlug string) (got forge.TokenIntrospection, err error) {
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
