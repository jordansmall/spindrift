package connectorruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/driver/driverrows"
	"spindrift.dev/launcher/internal/fixturebuild"
)

func boxRuntime(t *testing.T, rows []map[string]any) *connectorruntime.Runtime {
	t.Helper()
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := driverrows.Parse(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	rt := connectorruntime.InProcess(parsed)
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func TestInProcessRuntimeLoadsFirstPartyDriverConnectorWithoutASandbox(t *testing.T) {
	dir, err := fixturebuild.ConnectorDir()
	if err != nil {
		t.Fatal(err)
	}
	rt := boxRuntime(t, []map[string]any{{"name": "fixture", "module": filepath.Join(dir, "driver.wasm"), "firstParty": true}})
	if rt.HasSandbox() {
		t.Error("the Box's runtime has a sandbox")
	}
	if _, err := rt.Drivers().New("fixture"); err != nil {
		t.Fatalf("New(fixture) = %v, want the first-party Connector loaded in-process", err)
	}
	if _, err := rt.Load(context.Background(), []byte("wasm")); !errors.Is(err, connectorsandbox.ErrNoConnectorSandbox) {
		t.Errorf("Load = %v, want ErrNoConnectorSandbox", err)
	}
}

func TestInProcessRuntimeRefusesDriverConnectorNotFirstParty(t *testing.T) {
	rt := boxRuntime(t, []map[string]any{{"name": "outsider", "module": "/m/outsider.wasm"}})
	if _, err := rt.Drivers().New("outsider"); !errors.Is(err, driverrows.ErrNotFirstParty) {
		t.Fatalf("New(outsider) = %v, want ErrNotFirstParty", err)
	}
}
