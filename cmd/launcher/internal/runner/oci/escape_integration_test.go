//go:build integration

package oci

import (
	"os/exec"
	"testing"

	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest/escape"
)

// escapeImageTag is what the escape-probe image archive loads as; it differs
// from the real Connector sandbox image's, so loading it replaces nothing.
const escapeImageTag = "spindrift-connector-sandbox-escapeprobe:latest"

// escapeSandbox returns the real adapter over the escape-probe sandbox image,
// loaded into the runtime, and the host's store directory. It skips without a
// reachable runtime or nix.
func escapeSandbox(t *testing.T) (runner.Runner, string) {
	t.Helper()
	cli := requireRealOCI(t)
	host, skip, err := escape.HostDir()
	if err != nil {
		t.Fatal(err)
	}
	if skip != "" {
		t.Skip(skip)
	}
	archive, skip, err := fixturebuild.EscapeSandboxImage()
	if err != nil {
		t.Fatal(err)
	}
	if skip != "" {
		t.Skip(skip)
	}
	if out, err := exec.Command(cli, "load", "-i", archive).CombinedOutput(); err != nil {
		t.Fatalf("%s load: %v: %s", cli, err, out)
	}
	t.Cleanup(func() { _ = exec.Command(cli, "rmi", "--force", escapeImageTag).Run() })

	art := inputdoc.OCIArtifacts{ConnectorSandboxImageTag: inputdoc.ValueOf(escapeImageTag)}
	r, err := New(runner.Config{Runtime: cli}, art, func(string) (string, bool) { return "", false }, "")
	if err != nil {
		t.Fatal(err)
	}
	return r, host
}

func TestSandboxEscapeReachesNothingOfTheHost(t *testing.T) {
	r, host := escapeSandbox(t)
	escape.Sandbox(t, r, host)
}

func TestSandboxRefusesAndAuditsAnUndeclaredRoute(t *testing.T) {
	r, _ := escapeSandbox(t)
	escape.RouteRefusal(t, r)
}

func TestSandboxRefusesAFrameForAnotherConnectorsSession(t *testing.T) {
	r, _ := escapeSandbox(t)
	escape.Impersonation(t, r)
}
