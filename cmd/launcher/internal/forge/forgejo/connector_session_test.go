package forgejo_test

import (
	"net/http/httptest"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/forge/forgejo"
)

// sessionEnv is a runtime over a counting sandbox and a fake forgejo that
// answers Probe, for the tests of what the forge and the tracker share.
func sessionEnv(t *testing.T) (*strippedSandbox, *connectorruntime.Runtime, *httptest.Server) {
	t.Helper()
	f := startFakeForgejo(t, httptest.NewTLSServer)
	f.token = connectorToken
	sb := &strippedSandbox{Loader: connectorsandboxtest.InProcess()}
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(f.srv.Client()))
	t.Chdir(t.TempDir())
	return sb, runtimeOver(t, forgejoRows(t, f.srv.URL, func(*backendrows.Row) {}), sb), f.srv
}

// A guest that dies does not end the forge: the next operation
// runs on a reloaded Connector, as a daemon needs.
func TestConnectorForge_ReloadsAfterGuestDeath(t *testing.T) {
	sb, rt, srv := sessionEnv(t)
	cf, err := buildForgeOn(rt, srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cf.Probe(); err != nil {
		t.Fatal(err)
	}

	sb.loaded[0].Abort()
	if _, err := cf.Probe(); err == nil {
		t.Fatal("the call on a killed guest succeeded, want it to fail")
	}

	if _, err := cf.Probe(); err != nil {
		t.Fatalf("Probe after the guest died: %v, want a reloaded Connector", err)
	}
	if n := len(sb.loaded); n != 2 {
		t.Errorf("loads = %d, want one reload", n)
	}
}

// A row that serves both axes loads one Connector for the tracker and the
// Code Forge together, and one reload serves both after the guest dies.
func TestConnectorForgeAndTracker_ShareOneConnector(t *testing.T) {
	sb, rt, srv := sessionEnv(t)
	cf, err := buildForgeOn(rt, srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := buildTrackerOn(rt, srv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cf.Probe(); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Probe(); err != nil {
		t.Fatal(err)
	}
	if n := len(sb.loaded); n != 1 {
		t.Fatalf("loads = %d, want one Connector for both axes", n)
	}

	sb.loaded[0].Abort()
	if _, err := cf.Probe(); err == nil {
		t.Fatal("the call on a killed guest succeeded, want it to fail")
	}
	if _, err := tr.Probe(); err != nil {
		t.Fatalf("tracker Probe after the forge's call saw the guest die: %v, want the reloaded Connector", err)
	}
	if n := len(sb.loaded); n != 2 {
		t.Errorf("loads = %d, want one reload for both axes", n)
	}
}
