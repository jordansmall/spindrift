//go:build integration

package forge_test

import (
	"context"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorsandbox/sandbox"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/oci"
	"spindrift.dev/launcher/internal/seamtest"
)

// The test binary doubles as the podman fake, which runs the real connector-host
// from the sandbox argv.
func TestMain(m *testing.M) { seamtest.Main(m) }

// useSandboxSeam loads every Fake's Connector into a Connector sandbox started
// through the real OCI runner against the podman fake, until the test ends.
func useSandboxSeam(t *testing.T) {
	t.Helper()
	seamtest.UsePodman(t, seamtest.PodmanConfig{ImagePresent: true})
	art := inputdoc.OCIArtifacts{
		ImageTag:                 inputdoc.ValueOf("spindrift:test"),
		ConnectorSandboxImageTag: inputdoc.ValueOf("spindrift-connector-sandbox:test"),
	}
	r, err := oci.New(runner.Config{Runtime: "podman"}, art, func(string) (string, bool) { return "", false }, "")
	if err != nil {
		t.Fatal(err)
	}
	sb := sandbox.New(r, "podman")
	restore, err := forge.SwapFakeLoader(func(wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
		return sb.LoadNetwork(context.Background(), wasm, n)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		restore()
		_ = sb.Close(context.Background())
	})
}

func TestFake_TrackerContract_Sandbox(t *testing.T) {
	useSandboxSeam(t)
	forgetest.RunTrackerContract(t, newFakeHarness())
}

func TestFake_CodeForgeContract_Sandbox(t *testing.T) {
	useSandboxSeam(t)
	forgetest.RunCodeForgeContract(t, newFakeCodeForgeHarness())
}

func TestFake_PRForgeContract_Sandbox(t *testing.T) {
	useSandboxSeam(t)
	forgetest.RunPRForgeContract(t, newFakePRForgeHarness())
}
