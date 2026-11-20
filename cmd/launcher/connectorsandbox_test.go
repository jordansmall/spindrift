package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
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
// a load that reached an engine fails with a compile error, and a sandboxed one
// with the sandbox's.
func loadViaRow(t *testing.T) error {
	t.Helper()
	module := filepath.Join(t.TempDir(), "x.wasm")
	if err := os.WriteFile(module, []byte("not wasm"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := backendrows.Load(context.Background(), backendrows.Row{Name: "x", Module: module}, backendrows.Options{Audit: &bytes.Buffer{}})
	return err
}

// useRealConnectorSandbox puts back the production Loader builder that
// TestMain swaps for the in-process engine.
func useRealConnectorSandbox(t *testing.T) {
	t.Helper()
	prev := newConnectorSandboxLoader
	newConnectorSandboxLoader = realConnectorSandboxLoader
	t.Cleanup(func() { newConnectorSandboxLoader = prev })
}

// countConnectorSandboxInstalls swaps the Loader builder for one that counts
// its calls and installs the in-process engine.
func countConnectorSandboxInstalls(t *testing.T) *int {
	t.Helper()
	installs := 0
	prev := newConnectorSandboxLoader
	newConnectorSandboxLoader = func(config, string) connectorsandbox.Loader {
		installs++
		return connectorsandboxtest.InProcess()
	}
	t.Cleanup(func() { newConnectorSandboxLoader = prev })
	return &installs
}

// Every command that can load a Connector installs the sandbox itself, once,
// ahead of its first load. Without it a load would fail with
// ErrNoConnectorSandbox on that path alone.
func TestConnectorSandboxInstalledOnEveryLoadingPath(t *testing.T) {
	paths := map[string]func(t *testing.T){
		"newReadContext": func(t *testing.T) {
			rc, err := newReadContext(nil, false)
			if err != nil {
				t.Fatal(err)
			}
			rc.closeSandbox()
		},
		"newGatedContext": func(t *testing.T) {
			gc, err := newGatedContext(io.Discard, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			gc.closeSandbox()
		},
		"doctor":      func(t *testing.T) { cmdDoctor(doctorOptions{}, io.Discard, io.Discard) },
		"reconcile":   func(t *testing.T) { cmdReconcile(io.Discard, io.Discard) },
		"preview":     func(t *testing.T) { _ = preview(io.Discard, nil) },
		"completions": func(t *testing.T) { cmdCompleteIssues() },
	}
	for name, run := range paths {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(connectorsandbox.Use(nil))
			installs := countConnectorSandboxInstalls(t)
			stubPastaOnPath(t)
			setMinimalLocalBootstrapEnv(t, filepath.Join(t.TempDir(), "accum.git"))
			setArtifact(t, "RUNTIME", "echo")
			t.Chdir(t.TempDir())
			run(t)
			if *installs != 1 {
				t.Fatalf("Connector sandbox installed %d times, want 1", *installs)
			}
		})
	}
}

// countSandboxTotals swaps recordSandboxTotals for a call counter.
func countSandboxTotals(t *testing.T) *int {
	t.Helper()
	calls := 0
	prev := recordSandboxTotals
	recordSandboxTotals = func() { calls++ }
	t.Cleanup(func() { recordSandboxTotals = prev })
	return &calls
}

// The closer is also what bootstrap's error-path defer runs, so the totals
// must be recorded from it, not only from the success-path cleanup.
func TestInstallConnectorSandbox_CloserRecordsPrimaryTotals(t *testing.T) {
	useRealConnectorSandbox(t)
	t.Cleanup(connectorsandbox.Use(nil))
	calls := countSandboxTotals(t)
	closeSandbox, err := installConnectorSandbox(baseConfig())
	if err != nil {
		t.Fatal(err)
	}
	closeSandbox()
	if *calls != 1 {
		t.Fatalf("recordSandboxTotals called %d times, want 1", *calls)
	}
}

func TestInstallConnectorSandbox_StartFailureFailsTheRun(t *testing.T) {
	useRealConnectorSandbox(t)
	t.Cleanup(connectorsandbox.Use(nil))
	c := baseConfig()
	c.runtime = "podman"
	c.runnerKind = refusingKind
	closeSandbox, err := installConnectorSandbox(c)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSandbox()

	// The load site may swallow this error (a read shadow only dirties its
	// summary), so the verdict must not depend on it reaching the run.
	err = loadViaRow(t)
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
