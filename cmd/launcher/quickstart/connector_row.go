package main

import (
	"encoding/json"
	"fmt"

	"spindrift.dev/launcher/internal/backend"
	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
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
// never reach gh. With neither var set (a plain `go run`) it applies none and
// the native gh answers, as backend rows are opt-in. The wizard's token is the
// row's credential: runQuickstart exports it as GH_TOKEN before the probe.
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
