package connectorforge_test

import (
	"net/http"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// declaredTable is a Connector's own Route table: a host the Connector picked
// and the {repo} placeholder, both rebound by the harness.
func declaredTable(methods ...string) []connectorhost.Route {
	var routes []connectorhost.Route
	for _, m := range methods {
		routes = append(routes, connectorhost.Route{Method: m, Host: "forge.example", Path: "/api/v1/repos/{repo}"})
	}
	return routes
}

func TestNewForRunsTheCodeForgeContractOverADeclaredTable(t *testing.T) {
	c := connectorforgetest.Connector{
		Wasm:   connectorforgetest.FixtureWasm(t, "forge.wasm"),
		Routes: declaredTable(http.MethodGet, http.MethodDelete),
	}
	forgetest.RunCodeForgeContract(t, connectorforgetest.NewFor(t, c))
}

func TestNewPRForRunsThePRForgeContractOverADeclaredTable(t *testing.T) {
	c := connectorforgetest.Connector{
		Wasm:   connectorforgetest.FixtureWasm(t, connectorforgetest.PRWasm),
		Routes: declaredTable(http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete),
	}
	forgetest.RunPRForgeContract(t, connectorforgetest.NewPRFor(t, c))
}

func TestScriptedResponseIsServedBeforeTheModel(t *testing.T) {
	c := connectorforgetest.Connector{
		Wasm:   connectorforgetest.FixtureWasm(t, "forge.wasm"),
		Routes: declaredTable(http.MethodGet, http.MethodDelete),
	}
	h := connectorforgetest.NewFor(t, c)
	if ok, err := h.Forge().BranchExists("agent/issue-404"); err != nil || ok {
		t.Fatalf("BranchExists(unscripted) = (%v, %v), want (false, nil)", ok, err)
	}

	c.Script = []connectorforgetest.Scripted{{
		Method: http.MethodGet, Path: "/api/v1/repos/o/r/branches/agent/issue-404",
		Status: http.StatusOK, Body: `{"protected":false}`,
	}}
	h = connectorforgetest.NewFor(t, c)
	if ok, err := h.Forge().BranchExists("agent/issue-404"); err != nil || !ok {
		t.Fatalf("BranchExists(scripted) = (%v, %v), want (true, nil)", ok, err)
	}
}
