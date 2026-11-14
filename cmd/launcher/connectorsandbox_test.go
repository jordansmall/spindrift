package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/exitcode"
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

// loadViaRow loads a backend row over a module that no engine can compile, so
// an in-process load fails with a compile error and a sandboxed one with the
// sandbox's.
func loadViaRow(t *testing.T) error {
	t.Helper()
	module := filepath.Join(t.TempDir(), "x.wasm")
	if err := os.WriteFile(module, []byte("not wasm"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := backendrows.Load(context.Background(), backendrows.Row{Name: "x", Module: module}, backendrows.Options{Audit: &bytes.Buffer{}})
	return err
}

func TestInstallConnectorSandbox_OffLoadsInProcess(t *testing.T) {
	t.Cleanup(connectorsandbox.Use(nil))
	closeSandbox := installConnectorSandbox(baseConfig(), t.TempDir())
	defer closeSandbox()
	err := loadViaRow(t)
	if err == nil || errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatalf("load = %v, want an in-process compile error", err)
	}
	if got := sandboxVerdict(int(exitcode.Dispatched), &bytes.Buffer{}); got != int(exitcode.Dispatched) {
		t.Fatalf("verdict = %d, want a success left alone", got)
	}
}

func TestInstallConnectorSandbox_StartFailureFailsTheRun(t *testing.T) {
	t.Cleanup(connectorsandbox.Use(nil))
	c := baseConfig()
	c.ConnectorSandbox = true
	c.runtime = "podman"
	c.runnerKind = refusingKind
	closeSandbox := installConnectorSandbox(c, t.TempDir())
	defer closeSandbox()

	// The load site may swallow this error (a read shadow only dirties its
	// summary), so the verdict must not depend on it reaching the run.
	err := loadViaRow(t)
	if !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) || !strings.Contains(err.Error(), runner.ErrFakeNoSandbox.Error()) {
		t.Fatalf("load = %v, want ErrConnectorSandboxStart carrying the runner's refusal, not an in-process compile error", err)
	}
	// RUNTIME=podman is not the runner here: the error names the kind that refused.
	if !strings.Contains(err.Error(), "runner "+refusingKind+":") || strings.Contains(err.Error(), "podman") {
		t.Errorf("load = %v, want it to name runner %s", err, refusingKind)
	}
	var stderr bytes.Buffer
	if got := sandboxVerdict(int(exitcode.Dispatched), &stderr); got != int(exitcode.Failed) {
		t.Fatalf("verdict = %d, want Failed", got)
	}
	if !strings.Contains(stderr.String(), connectorsandbox.ErrConnectorSandboxStart.Error()) {
		t.Errorf("stderr = %q, want the named error", stderr.String())
	}
	if got := sandboxVerdict(int(exitcode.SignalledStop), &bytes.Buffer{}); got != int(exitcode.SignalledStop) {
		t.Errorf("verdict = %d, want a stop left as it was", got)
	}
	for _, clean := range []exitcode.Dispatch{exitcode.QueueEmpty, exitcode.NoneDispatchable} {
		stderr.Reset()
		if got := sandboxVerdict(int(clean), &stderr); got != int(exitcode.Failed) {
			t.Errorf("verdict for exit %d = %d, want Failed", clean, got)
		}
		if !strings.Contains(stderr.String(), connectorsandbox.ErrConnectorSandboxStart.Error()) {
			t.Errorf("stderr for exit %d = %q, want the named error", clean, stderr.String())
		}
	}
}

func TestConnectorSandboxRunnerLabel(t *testing.T) {
	for _, tc := range []struct{ kind, runtime, want string }{
		{"", "podman", "oci (podman)"},
		{"oci", "docker", "oci (docker)"},
		{"bwrap", "podman", "bwrap"},
		{"bwrap", "bwrap", "bwrap"},
	} {
		c := baseConfig()
		c.runnerKind, c.runtime = tc.kind, tc.runtime
		if got := connectorSandboxRunnerLabel(c); got != tc.want {
			t.Errorf("label(%q, %q) = %q, want %q", tc.kind, tc.runtime, got, tc.want)
		}
	}
}
