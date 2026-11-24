package forgejo_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/primaryrecord"
)

// primaryLog is the failed-operation record this process left under the host
// log directory of the current working directory, empty when there is none.
func primaryLog(t *testing.T) string {
	t.Helper()
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(hostpaths.LogDir(pwd), primaryrecord.Name("forgejo", ".log")))
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
	useForgejoRow(t, srv.URL, refuseGETs)
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(srv.Client()))
	t.Chdir(t.TempDir())
	tr, err := buildTracker(srv)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tr.Issue("1")

	if err == nil {
		t.Fatal("Issue succeeded, want the refused route")
	}
	if log := primaryLog(t); !strings.Contains(log, `"op":"tracker.issue"`) || !strings.Contains(log, "route") {
		t.Errorf("log = %q, want the tracker.issue failure", log)
	}
}

func TestConnectorTracker_LoadFailureIsRecorded(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(r *backendrows.Row) { r.Module = filepath.Join(t.TempDir(), "missing.wasm") })
	t.Chdir(t.TempDir())

	if _, err := buildTracker(srv); err == nil {
		t.Fatal("want the load error")
	}

	if log := primaryLog(t); !strings.Contains(log, `"op":"load the Connector"`) || !strings.Contains(log, "missing.wasm") {
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
	if log := primaryLog(t); log != "" {
		t.Errorf("log = %q, want none for a lost claim", log)
	}
}

func TestConnectorForge_LoadFailureIsRecorded(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(r *backendrows.Row) { r.Module = filepath.Join(t.TempDir(), "missing.wasm") })
	t.Chdir(t.TempDir())

	if _, err := buildForge(srv, ""); err == nil {
		t.Fatal("want the load error")
	}

	if log := primaryLog(t); !strings.Contains(log, `"op":"load the Connector"`) || !strings.Contains(log, "missing.wasm") {
		t.Errorf("log = %q, want the op and the module read error", log)
	}
}

func TestConnectorForge_FailedCallIsRecordedByMethod(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, refuseGETs)
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(srv.Client()))
	t.Chdir(t.TempDir())
	cf, err := buildForge(srv, "")
	if err != nil {
		t.Fatal(err)
	}
	if log := primaryLog(t); log != "" {
		t.Fatalf("composing recorded %q, want a clean log", log)
	}

	if _, err := cf.Probe(); err == nil {
		t.Fatal("Probe succeeded, want the refused route")
	}

	if log := primaryLog(t); strings.Count(log, "\n") != 1 || !strings.Contains(log, `"op":"forge.probe"`) {
		t.Errorf("log = %q, want one forge.probe failure", log)
	}
}

// A failure Compose itself returns is recorded once, under the compose op.
func TestConnectorForge_ComposeFailureIsRecorded(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	t.Cleanup(srv.Close)
	useForgejoRow(t, srv.URL, func(*backendrows.Row) {})
	t.Setenv(connectorCredentialEnv, connectorToken)
	t.Cleanup(forgejo.SetConnectorClient(srv.Client()))
	t.Cleanup(forgejo.SetLoadRowConnector(func(ctx context.Context, row backendrows.Row, o backendrows.Options) (*connectorhost.Connector, error) {
		c, err := backendrows.Load(ctx, row, o)
		if err == nil {
			c.Capabilities = nil
		}
		return c, err
	}))
	t.Chdir(t.TempDir())

	if _, err := buildForge(srv, ""); !errors.Is(err, connectorforge.ErrNotForge) {
		t.Fatalf("buildForge = %v, want ErrNotForge", err)
	}

	if log := primaryLog(t); strings.Count(log, "\n") != 1 || !strings.Contains(log, `"op":"compose the Code Forge"`) {
		t.Errorf("log = %q, want one compose failure", log)
	}
}
