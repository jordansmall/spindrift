package contract

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
)

// SandboxRuntimes are the operator-facing runtimes an OCI adapter renders a
// Connector sandbox for: the oci family's own runtime row.
var SandboxRuntimes = runner.OCI.Runtimes

// SandboxRender asserts the isolation of the argv an OCI adapter renders to
// start a Connector sandbox for each runtime in SandboxRuntimes. argvFor
// returns the argv after the CLI name.
func SandboxRender(t *testing.T, argvFor func(t *testing.T, runtime string) []string) {
	for _, rt := range SandboxRuntimes {
		t.Run(rt, func(t *testing.T) {
			argv := argvFor(t, rt)
			if len(argv) == 0 || argv[0] != "run" {
				t.Fatalf("argv = %v, want it to start with run", argv)
			}
			for _, v := range runner.SandboxViolations(argv) {
				t.Errorf("sandbox argv %v: %s", argv, v)
			}
		})
	}
}

// SandboxHarness builds the Runner SandboxLifecycle starts a Connector sandbox
// on.
type SandboxHarness interface {
	// Sandbox returns a Runner whose StartConnectorSandbox of name runs the real
	// connector-host with its stdio attached, and lists name as running until
	// Kill(name) or the host's exit.
	Sandbox(t *testing.T, name string) runner.Runner
}

// SandboxLifecycle runs the Connector sandbox's start, call and stop scenario
// against h: a Connector loaded into the sandboxed host answers a call, and
// stopping the sandbox ends the host and removes the name.
func SandboxLifecycle(t *testing.T, h SandboxHarness) {
	name := runner.ConnectorSandboxPrefix + "contract"
	r := h.Sandbox(t, name)
	assertSandboxListed(t, r, name, false)

	sb, err := r.StartConnectorSandbox(name)
	if err != nil {
		t.Fatalf("StartConnectorSandbox: %v", err)
	}
	runnertest.Eventually(t, settleTimeout, "sandbox listed as running", func() bool {
		names, err := r.ListRunning()
		return err == nil && slices.Contains(names, name)
	})

	ctx, cancel := context.WithTimeout(context.Background(), runReturnTimeout)
	defer cancel()
	s, err := connectorhost.NewSessions(ctx, sb.Stdout, sb.Stdin, func() { _ = r.Kill(name) }, sb.Wait)
	if err != nil {
		t.Fatalf("NewSessions over the sandbox: %v (stderr: %s)", err, sb.Stderr())
	}
	dir, err := fixturebuild.ConnectorDir()
	if err != nil {
		t.Fatal(err)
	}
	wasm, err := os.ReadFile(filepath.Join(dir, "call.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Load(ctx, wasm)
	if err != nil {
		t.Fatalf("Load into the sandbox: %v", err)
	}
	var out string
	if err := c.Call(ctx, "echo", "ping", &out); err != nil || out != "ping" {
		t.Errorf("echo through the sandbox = %q, %v, want ping", out, err)
	}

	if err := s.Close(ctx); err != nil {
		t.Errorf("Sessions Close = %v, want a sandbox that exits cleanly", err)
	}
	if err := r.Kill(name); err != nil {
		t.Errorf("Kill of a stopped sandbox: got %v, want nil", err)
	}
	runnertest.Eventually(t, settleTimeout, "sandbox gone from ListRunning", func() bool {
		names, err := r.ListRunning()
		return err == nil && !slices.Contains(names, name)
	})
}

func assertSandboxListed(t *testing.T, r runner.Runner, name string, want bool) {
	t.Helper()
	names, err := r.ListRunning()
	if err != nil {
		t.Fatalf("ListRunning: %v", err)
	}
	if got := slices.Contains(names, name); got != want {
		t.Errorf("ListRunning = %v: contains %q = %v, want %v", names, name, got, want)
	}
}
