package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
)

// refusingKind is a runner family whose Connector sandbox never starts: its
// Fake has no StartConnectorSandboxFunc, so every start fails closed.
const refusingKind = "refusing-test"

func init() {
	runner.Register(runner.Strategy{
		Name: refusingKind,
		New: func(runner.Config, inputdoc.Artifacts, runner.KnobLookup, string) (runner.Runner, error) {
			return runner.NewFake(), nil
		},
	})
}

// useRealConnectorSandbox puts back the production Loader builder that
// TestMain swaps for the in-process engine.
func useRealConnectorSandbox(t *testing.T) {
	t.Helper()
	prev := newConnectorSandboxLoader
	newConnectorSandboxLoader = realConnectorSandboxLoader
	t.Cleanup(func() { newConnectorSandboxLoader = prev })
}

func refusingDocT(t *testing.T, settings map[string]string) *inputdoc.Document {
	t.Helper()
	doc := demandDocT(settings)
	if err := json.Unmarshal([]byte(`{"RUNNER_KIND":"`+refusingKind+`","RUNTIME":"podman"}`), &doc.Artifacts); err != nil {
		t.Fatal(err)
	}
	return doc
}

// The Daemon counts demand through a Connector row in its own process, so that
// load must go to the sandbox it installs, never to an engine it hosts itself.
func TestInstallConnectorSandbox_DemandTrackerLoadGoesToTheSandbox(t *testing.T) {
	clearKnobEnvT(t)
	useRealConnectorSandbox(t)
	t.Cleanup(connectorsandbox.Use(nil))
	t.Cleanup(backendrows.Snapshot())

	module := filepath.Join(t.TempDir(), "x.wasm")
	if err := os.WriteFile(module, []byte("not wasm"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]backendrows.Row{{
		Name: "github", Module: module, CredentialEnv: ghTokenEnv,
		Routes: []connectorhost.Route{{Method: "GET", Host: "api.github.com", Path: "/repos/{repo}/issues"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := backendrows.Use(raw, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
	doc := refusingDocT(t, map[string]string{"ISSUE_TRACKER": "github", "REPO_SLUG": "o/r", "LABEL": "ready-for-agent"})
	t.Setenv(ghTokenEnv, "tok")

	defer installConnectorSandbox(doc, t.TempDir())()
	sources, missing := buildDemandSources(doc, workKinds)
	if len(missing) != 0 {
		t.Fatalf("missing sources: %v", missing)
	}
	// The tracker loads its Connector on its first call.
	_, err = sources[workKinds[0]].CountReady(false)
	if err == nil || errors.Is(err, connectorsandbox.ErrNoConnectorSandbox) {
		t.Fatalf("CountReady = %v, want a sandbox start failure, not a missing sandbox", err)
	}
	if !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) || !strings.Contains(err.Error(), runner.ErrFakeNoSandbox.Error()) {
		t.Errorf("CountReady = %v, want ErrConnectorSandboxStart carrying the runner refusal", err)
	}
}

func TestInstallConnectorSandbox_CloserStopsTheSandbox(t *testing.T) {
	useRealConnectorSandbox(t)
	t.Cleanup(connectorsandbox.Use(nil))
	installConnectorSandbox(refusingDocT(t, nil), t.TempDir())()
	if _, err := connectorsandbox.Load(t.Context(), []byte("x")); err == nil || errors.Is(err, connectorsandbox.ErrNoConnectorSandbox) {
		t.Fatalf("load after close = %v, want the closed sandbox's error", err)
	}
}
