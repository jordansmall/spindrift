package forgejo_test

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorrecord"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/hostpaths"
)

// connectorLog is the failed-operation record this process left under the host
// log directory of the current working directory, empty when there is none.
func connectorLog(t *testing.T) string {
	t.Helper()
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(hostpaths.LogDir(pwd), connectorrecord.Name("forgejo", ".log")))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(raw)
}

// refuseGETs is a forgejo row whose GET routes name an unrelated path, so a
// read the Connector makes fails with the host's refusal.
func refuseGETs(r *backendrows.Row) {
	r.Routes = append(slicesDeleteMethod(r.Routes, "GET"), connectorhost.Route{Method: "GET", Host: backendrows.BaseHost, Path: "/unrelated"})
}

func TestConnectorTracker_FailedCallIsRecordedByMethod(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	rt := useForgejoRow(t, srv.URL, refuseGETs)
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(srv.Client()))
	t.Chdir(t.TempDir())
	tr, err := buildTracker(t, rt, srv)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tr.Issue("1")

	if err == nil {
		t.Fatal("Issue succeeded, want the refused route")
	}
	if log := connectorLog(t); !strings.Contains(log, `"op":"tracker.issue"`) || !strings.Contains(log, "route") {
		t.Errorf("log = %q, want the tracker.issue failure", log)
	}
}

func TestConnectorTracker_LoadFailureIsRecorded(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	rt := useForgejoRow(t, srv.URL, func(r *backendrows.Row) { r.Module = filepath.Join(t.TempDir(), "missing.wasm") })
	t.Chdir(t.TempDir())

	if _, err := buildTracker(t, rt, srv); err == nil {
		t.Fatal("want the load error")
	}

	if log := connectorLog(t); !strings.Contains(log, `"op":"load the Connector"`) || !strings.Contains(log, "missing.wasm") {
		t.Errorf("log = %q, want the op and the module read error", log)
	}
}

// A lost claim race is core policy, not a Connector fault: an empty log is what
// marks a clean run.
func TestConnectorTracker_LostClaimRaceRecordsNothing(t *testing.T) {
	w := newTrackerHarness(t)
	w.SeedIssue(forge.Issue{Number: "111", Title: "claimed"})
	for _, step := range [][2]forge.DispatchState{{forge.Untriaged, forge.Dispatchable}, {forge.Dispatchable, forge.InProgress}} {
		if err := w.tr.TransitionState("111", step[0], step[1]); err != nil {
			t.Fatal(err)
		}
	}

	err := w.tr.TransitionState("111", forge.Dispatchable, forge.InProgress)

	if !errors.Is(err, forge.ErrAlreadyClaimed) {
		t.Fatalf("second claim = %v, want ErrAlreadyClaimed", err)
	}
	if log := connectorLog(t); log != "" {
		t.Errorf("log = %q, want none for a lost claim", log)
	}
}

func TestConnectorForge_LoadFailureIsRecorded(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	rt := useForgejoRow(t, srv.URL, func(r *backendrows.Row) { r.Module = filepath.Join(t.TempDir(), "missing.wasm") })
	t.Chdir(t.TempDir())

	if _, err := buildForge(t, rt, srv, ""); err == nil {
		t.Fatal("want the load error")
	}

	if log := connectorLog(t); !strings.Contains(log, `"op":"load the Connector"`) || !strings.Contains(log, "missing.wasm") {
		t.Errorf("log = %q, want the op and the module read error", log)
	}
}

func TestConnectorForge_FailedCallIsRecordedByMethod(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	rt := useForgejoRow(t, srv.URL, refuseGETs)
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(srv.Client()))
	t.Chdir(t.TempDir())
	cf, err := buildForge(t, rt, srv, "")
	if err != nil {
		t.Fatal(err)
	}
	if log := connectorLog(t); log != "" {
		t.Fatalf("composing recorded %q, want a clean log", log)
	}

	if _, err := cf.Probe(); err == nil {
		t.Fatal("Probe succeeded, want the refused route")
	}

	if log := connectorLog(t); strings.Count(log, "\n") != 1 || !strings.Contains(log, `"op":"forge.probe"`) {
		t.Errorf("log = %q, want one forge.probe failure", log)
	}
}

// A failure Compose itself returns is recorded once, under the compose op.
func TestConnectorForge_ComposeFailureIsRecorded(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(srv.Client()))
	rt := runtimeOver(t, forgejoRows(t, srv.URL, func(*backendrows.Row) {}), &strippedSandbox{Loader: connectorsandboxtest.InProcess(), drop: []string{connectorforge.CapBranches}})
	t.Chdir(t.TempDir())

	if _, err := buildForgeOn(rt, srv.URL, ""); !errors.Is(err, connectorforge.ErrNotForge) {
		t.Fatalf("buildForge = %v, want ErrNotForge", err)
	}

	if log := connectorLog(t); strings.Count(log, "\n") != 1 || !strings.Contains(log, `"op":"compose the Code Forge"`) {
		t.Errorf("log = %q, want one compose failure", log)
	}
}
