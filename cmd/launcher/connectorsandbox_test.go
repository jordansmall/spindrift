package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/connectorsetup"
	"spindrift.dev/launcher/internal/driver/bundle"
	"spindrift.dev/launcher/internal/driver/driverrows"
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

// loadViaRow loads a backend row through a Connector runtime, as production
// does, over a module that no engine can compile, so a load that reached an
// engine fails with a compile error, and a sandboxed one with the sandbox's.
func loadViaRow(t *testing.T, sb connectorsandbox.Loader) error {
	t.Helper()
	t.Chdir(t.TempDir()) // the audit file opens under the working directory
	module := filepath.Join(t.TempDir(), "x.wasm")
	if err := os.WriteFile(module, []byte("not wasm"), 0o600); err != nil {
		t.Fatal(err)
	}
	row := backendrows.Row{Name: "x", Module: module}
	rt := connectorruntime.New([]backendrows.Row{row}, nil, sb)
	defer rt.Close()
	_, err := rt.Connector(row, connectorruntime.SessionSpec{})
	return err
}

// countedKind is a runner family that counts the runners built for it, so a
// test can tell whether anything started a Connector sandbox on it.
const countedKind = "counted-test"

var countedBuilt atomic.Int32

func init() {
	runner.Register(runner.Strategy{
		Name: countedKind,
		New: func(runner.Config, inputdoc.Artifacts, runner.KnobLookup, string) (runner.Runner, error) {
			countedBuilt.Add(1)
			return runner.NewFake(), nil
		},
	})
}

// countingLoader is a Loader the test hands the runtime and no path could
// install by accident.
type countingLoader struct{ connectorsandbox.Loader }

// Every command that can load a Connector uses the runtime the program built,
// whose sandbox is the only one: none installs one of its own, or a load
// on that path alone would reach a sandbox the runtime's close never stops.
func TestConnectorLoadingPathsUseTheProgramsRuntime(t *testing.T) {
	paths := map[string]func(t *testing.T, rt *connectorruntime.Runtime){
		"newReadContext": func(t *testing.T, rt *connectorruntime.Runtime) {
			if _, err := newReadContext(rt, nil, false); err != nil {
				t.Fatal(err)
			}
		},
		"newGatedContext": func(t *testing.T, rt *connectorruntime.Runtime) {
			if _, err := newGatedContext(rt, io.Discard, nil, false); err != nil {
				t.Fatal(err)
			}
		},
		"doctor": func(t *testing.T, rt *connectorruntime.Runtime) {
			cmdDoctor(rt, doctorOptions{}, io.Discard, io.Discard)
		},
		"reconcile":   func(t *testing.T, rt *connectorruntime.Runtime) { cmdReconcile(rt, io.Discard, io.Discard) },
		"preview":     func(t *testing.T, rt *connectorruntime.Runtime) { _ = preview(rt, io.Discard, nil) },
		"completions": func(t *testing.T, rt *connectorruntime.Runtime) { cmdCompleteIssues(rt) },
		"stats": func(t *testing.T, rt *connectorruntime.Runtime) {
			cmdStats(rt, []string{"--root", t.TempDir()}, io.Discard, io.Discard)
		},
		"recover": func(t *testing.T, rt *connectorruntime.Runtime) {
			verbHandlers["recover"](rt, []string{"999"}, io.Discard, io.Discard)
		},
		"research": func(t *testing.T, rt *connectorruntime.Runtime) {
			verbHandlers["research"](rt, []string{"--no-build"}, io.Discard, io.Discard)
		},
		"butler": func(t *testing.T, rt *connectorruntime.Runtime) {
			verbHandlers["butler"](rt, []string{"--no-build"}, io.Discard, io.Discard)
		},
		"lateMergeSweep": func(t *testing.T, rt *connectorruntime.Runtime) {
			cmdLateMergeSweep(rt, io.Discard, io.Discard)
		},
		// The archive holds a Pass of the named driver, so the replay reaches
		// the runtime's sandbox; the module is not wasm, so it fails there.
		"shadowReplay": func(t *testing.T, rt *connectorruntime.Runtime) {
			logs, wasm := t.TempDir(), filepath.Join(t.TempDir(), "d.wasm")
			if err := os.WriteFile(wasm, []byte("not wasm"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(logs, "issue-1.log"), []byte(shadowReplayArchive("opencode")), 0o600); err != nil {
				t.Fatal(err)
			}
			cmdShadowReplay(rt, []string{"--driver", "opencode", "--connector", wasm, logs}, io.Discard, io.Discard)
		},
	}
	for name, run := range paths {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(connectorsandbox.Use(nil))
			stubPastaOnPath(t)
			setMinimalLocalBootstrapEnv(t, filepath.Join(t.TempDir(), "accum.git"))
			setArtifact(t, "RUNTIME", "echo")
			t.Chdir(mustSeedableCheckout(t)) // the verbs that bootstrap need a git checkout
			in, err := connectorInput()
			if err != nil {
				t.Fatal(err)
			}
			if in.Drivers, err = driverrows.Parse(nil, bundle.Drivers()); err != nil {
				t.Fatal(err)
			}
			l := &countingLoader{connectorsandboxtest.InProcess()}
			rt, err := connectorsetup.New(in, connectorsetup.Options{Sandbox: l})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rt.Close() }()
			run(t, rt)
			if connectorsandbox.Current() != nil {
				t.Fatal("a Connector-loading path installed a process-wide sandbox")
			}
		})
	}
}

// A sandbox starts on the first load, not when the program builds its runtime,
// so a command that loads no Connector starts none.
func TestMainRun_NoConnectorLoadStartsNoSandbox(t *testing.T) {
	prev := newConnectorRuntime
	newConnectorRuntime = connectorsetup.New
	t.Cleanup(connectorsandbox.Use(nil))
	t.Cleanup(func() { newConnectorRuntime = prev })
	setRunner(t, "echo", countedKind)
	t.Chdir(t.TempDir())
	countedBuilt.Store(0)

	var stdout, stderr bytes.Buffer
	mainRunT(t, []string{"stats", "--root", t.TempDir()}, &stdout, &stderr)
	if got := countedBuilt.Load(); got != 0 {
		t.Fatalf("%d runners built for a command that loads no Connector, want 0; stderr=%q", got, stderr.String())
	}

	// The same input does start one on a load.
	in, err := connectorInput()
	if err != nil {
		t.Fatal(err)
	}
	rt, err := connectorsetup.New(in, connectorsetup.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Close() }()
	_ = loadViaRow(t, rt.Sandbox())
	if got := countedBuilt.Load(); got != 1 {
		t.Fatalf("%d runners built after a load, want 1", got)
	}
}

// A primary run still writes its totals line: closing the runtime mainRun
// builds runs each backend's totals hook, after the sandbox stops.
func TestConnectorRuntimeClose_RecordsSandboxTotals(t *testing.T) {
	calls := 0
	t.Cleanup(backendkit.ReplaceRegistryForTest([]backendkit.Strategy{
		{RecordSandboxTotals: func(connectorsandbox.Stats) { calls++ }},
	}))
	t.Cleanup(connectorsandbox.Use(nil))
	in, err := connectorInput()
	if err != nil {
		t.Fatal(err)
	}
	rt, err := newConnectorRuntime(in, connectorsetup.Options{Sandbox: servedSandbox{connectorsandboxtest.InProcess()}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("totals recorded %d times before the runtime closed", calls)
	}
	_ = rt.Close()
	if calls != 1 {
		t.Fatalf("totals recorded %d times, want 1", calls)
	}
}

func TestConnectorRuntime_StartFailureFailsTheRun(t *testing.T) {
	t.Cleanup(connectorsandbox.Use(nil))
	in, err := connectorInput()
	if err != nil {
		t.Fatal(err)
	}
	in.Runtime, in.RunnerKind = "podman", refusingKind
	rt, err := connectorsetup.New(in, connectorsetup.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Close() }()

	// The load site may swallow this error, so the verdict must not depend on
	// it reaching the run.
	err = loadViaRow(t, rt.Sandbox())
	if !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) || !strings.Contains(err.Error(), runner.ErrFakeNoSandbox.Error()) {
		t.Fatalf("load = %v, want ErrConnectorSandboxStart carrying the runner's refusal, not an in-process compile error", err)
	}
	// RUNTIME=podman is not the runner here: the error names the kind that refused.
	if !strings.Contains(err.Error(), "runner "+refusingKind+" for Connector x:") || strings.Contains(err.Error(), "podman") {
		t.Errorf("load = %v, want it to name runner %s", err, refusingKind)
	}
	var stderr bytes.Buffer
	if got := sandboxVerdict(int(exitcode.Dispatched), rt, &stderr); got != int(exitcode.Failed) {
		t.Fatalf("verdict = %d, want Failed", got)
	}
	if !strings.Contains(stderr.String(), connectorsandbox.ErrConnectorSandboxStart.Error()) {
		t.Errorf("stderr = %q, want the named error", stderr.String())
	}
	if got := sandboxVerdict(int(exitcode.SignalledStop), rt, &bytes.Buffer{}); got != int(exitcode.SignalledStop) {
		t.Errorf("verdict = %d, want a stop left as it was", got)
	}
	for _, clean := range []exitcode.Dispatch{exitcode.QueueEmpty, exitcode.NoneDispatchable} {
		stderr.Reset()
		if got := sandboxVerdict(int(clean), rt, &stderr); got != int(exitcode.Failed) {
			t.Errorf("verdict for exit %d = %d, want Failed", clean, got)
		}
		if !strings.Contains(stderr.String(), connectorsandbox.ErrConnectorSandboxStart.Error()) {
			t.Errorf("stderr for exit %d = %q, want the named error", clean, stderr.String())
		}
	}
}

// mainRun closes the runtime it built on every exit, so a primary run writes
// its totals line without any verb closing the runtime itself.
func TestMainRun_ClosesTheRuntimeItBuilt(t *testing.T) {
	calls := 0
	t.Cleanup(backendkit.ReplaceRegistryForTest([]backendkit.Strategy{
		{RecordSandboxTotals: func(connectorsandbox.Stats) { calls++ }},
	}))
	prev := newConnectorRuntime
	t.Cleanup(func() { newConnectorRuntime = prev })
	newConnectorRuntime = func(in connectorsetup.Input, opts connectorsetup.Options) (*connectorruntime.Runtime, error) {
		opts.Sandbox = servedSandbox{connectorsandboxtest.InProcess()}
		return prev(in, opts)
	}
	t.Cleanup(connectorsandbox.Use(nil))
	t.Chdir(t.TempDir())

	var stdout, stderr bytes.Buffer
	mainRunT(t, []string{"stats", "--root", t.TempDir()}, &stdout, &stderr)
	if calls != 1 {
		t.Fatalf("totals recorded %d times after mainRun returned, want 1; stderr=%q", calls, stderr.String())
	}
}

// servedSandbox is the in-process engine reporting that it served the run, so
// the runtime's close records its totals.
type servedSandbox struct{ connectorsandbox.Loader }

func (servedSandbox) Stats() (connectorsandbox.Stats, bool) {
	return connectorsandbox.Stats{Start: 1}, true
}
