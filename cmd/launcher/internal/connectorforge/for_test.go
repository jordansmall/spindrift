package connectorforge_test

import (
	"net/http"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/connectortest"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

func TestNewForRunsTheCodeForgeContractOverADeclaredTable(t *testing.T) {
	t.Parallel()
	c := connectortest.Connector{
		Wasm:   connectortest.FixtureWasm(t, "forge.wasm"),
		Routes: connectortest.RoutePerMethod("forge.example", http.MethodGet, http.MethodDelete),
	}
	forgetest.RunCodeForgeContract(t, connectorforgetest.NewFor(t, c))
}

func TestNewPRForRunsThePRForgeContractOverADeclaredTable(t *testing.T) {
	t.Parallel()
	c := connectortest.Connector{
		Wasm:   connectortest.FixtureWasm(t, connectorforgetest.PRWasm),
		Routes: connectortest.RoutePerMethod("forge.example", http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete),
	}
	forgetest.RunPRForgeContract(t, connectorforgetest.NewPRFor(t, c))
}

func TestScriptedResponseIsServedBeforeTheModel(t *testing.T) {
	t.Parallel()
	c := connectortest.Connector{
		Wasm:   connectortest.FixtureWasm(t, "forge.wasm"),
		Routes: connectortest.RoutePerMethod("forge.example", http.MethodGet, http.MethodDelete),
	}
	h := connectorforgetest.NewFor(t, c)
	if ok, err := h.Forge().BranchExists("agent/issue-404"); err != nil || ok {
		t.Fatalf("BranchExists(unscripted) = (%v, %v), want (false, nil)", ok, err)
	}

	c.Script = []connectortest.Scripted{{
		Method: http.MethodGet, Path: "/api/v1/repos/o/r/branches/agent/issue-404",
		Status: http.StatusOK, Body: `{"protected":false}`,
	}}
	h = connectorforgetest.NewFor(t, c)
	if ok, err := h.Forge().BranchExists("agent/issue-404"); err != nil || !ok {
		t.Fatalf("BranchExists(scripted) = (%v, %v), want (true, nil)", ok, err)
	}
}
