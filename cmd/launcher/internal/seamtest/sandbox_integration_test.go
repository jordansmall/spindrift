//go:build integration

package seamtest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorforge/connectorforgetest"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/sandbox"
	"spindrift.dev/launcher/internal/driver/driverkit/conformance"
	"spindrift.dev/launcher/internal/fixturebuild"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/oci"
)

// podmanSandbox is a Connector sandbox started through the real OCI runner
// against the podman fake, which runs the real connector-host from the sandbox
// argv. It returns the file the fake records its argvs to.
func podmanSandbox(t *testing.T, cfg PodmanConfig) (*sandbox.Sandbox, string) {
	t.Helper()
	cfg.ImagePresent = true
	rec := UsePodman(t, cfg)
	art := inputdoc.OCIArtifacts{
		ImageTag:                 inputdoc.ValueOf("spindrift:test"),
		ConnectorSandboxImageTag: inputdoc.ValueOf("spindrift-connector-sandbox:test"),
	}
	r, err := oci.New(runner.Config{Runtime: "podman"}, art, func(string) (string, bool) { return "", false }, "")
	if err != nil {
		t.Fatal(err)
	}
	sb := sandbox.New(r, "podman")
	t.Cleanup(func() { _ = sb.Close(context.Background()) })
	return sb, rec
}

func TestStrategyCorpus_Sandbox(t *testing.T) {
	dir, err := fixturebuild.ConnectorDir()
	if err != nil {
		t.Fatal(err)
	}
	wasm, err := os.ReadFile(filepath.Join(dir, "seamtest-driver.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	sb, _ := podmanSandbox(t, PodmanConfig{})
	c, err := sb.Load(context.Background(), wasm)
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.DriverStrategy()
	if err != nil {
		t.Fatal(err)
	}
	conformance.Run(t, s, filepath.Join("testdata", "corpus"))
}

func TestComposedForgeCodeForgeContract_Sandbox(t *testing.T) {
	sb, _ := podmanSandbox(t, PodmanConfig{})
	forgetest.RunCodeForgeContract(t, connectorforgetest.NewVia(t, sb.LoadNetwork))
}

func TestComposedForgePRForgeContract_Sandbox(t *testing.T) {
	sb, _ := podmanSandbox(t, PodmanConfig{})
	forgetest.RunPRForgeContract(t, connectorforgetest.NewPRVia(t, sb.LoadNetwork))
}

func TestSandboxStartFailureNamesTheRunner(t *testing.T) {
	sb, rec := podmanSandbox(t, PodmanConfig{RefuseSandbox: "Error: sandbox refused by fake"})
	_, err := sb.Load(context.Background(), []byte("unused"))
	if !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatalf("Load error = %v; want ErrConnectorSandboxStart", err)
	}
	for _, want := range []string{"podman", "sandbox refused by fake"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	if again := sb.Err(); !errors.Is(again, connectorsandbox.ErrConnectorSandboxStart) {
		t.Errorf("Err() = %v; want the start failure remembered", again)
	}
	var runs int
	for _, argv := range ReadRecord(t, rec) {
		if argv[0] == "run" {
			runs++
		}
	}
	if runs != 1 {
		t.Errorf("podman ran %d times; want the one refused sandbox start", runs)
	}
}
