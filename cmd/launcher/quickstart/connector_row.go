package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge/github"
)

// The env vars nix/quickstart.nix's wrapper sets to the github Connector's
// compiled wasm and its Route table (JSON), the two facts a Consumer flake's
// backend row would carry. Quickstart runs before any Consumer flake, so it
// has no input document to read them from.
const (
	connectorWasmEnv   = "SPINDRIFT_BACKEND_CONNECTOR_WASM"
	connectorRoutesEnv = "SPINDRIFT_BACKEND_CONNECTOR_ROUTES"
)

// useBakedGithubRow applies the github backend row the build baked in, so the
// wizard's identity probe and token scope audit go through the Connector and
// never reach gh. With neither var set (a plain `go run`) there is no compiled
// Connector to route through, so it applies none and the probe and audit fail
// with ErrNoReadRoute.
// The wizard's token is the row's credential: runQuickstart exports it as
// GH_TOKEN before the probe. The baked routes pin api.github.com, so under any
// other GH_HOST it applies none either and they fail the same way.
func useBakedGithubRow(getenv func(string) string) error {
	wasm, routes := getenv(connectorWasmEnv), getenv(connectorRoutesEnv)
	if wasm == "" && routes == "" {
		return nil
	}
	for _, v := range []struct{ name, val string }{{connectorWasmEnv, wasm}, {connectorRoutesEnv, routes}} {
		if v.val == "" {
			return fmt.Errorf("%s is unset while the other baked-in Connector var is set", v.name)
		}
	}
	var table []connectorhost.Route
	if err := json.Unmarshal([]byte(routes), &table); err != nil {
		return fmt.Errorf("%s: %w", connectorRoutesEnv, err)
	}
	// After the wiring checks, so a broken bake errors on every host.
	if !strings.EqualFold(github.HostFrom(getenv), github.DefaultHost) {
		return nil
	}
	doc, err := json.Marshal([]backendrows.Row{{
		Name:          backend.GitHub.Name,
		Module:        wasm,
		Routes:        table,
		CredentialEnv: backend.GitHub.TokenEnvVar,
	}})
	if err != nil {
		return err
	}
	return backendrows.Use(doc, getenv)
}
