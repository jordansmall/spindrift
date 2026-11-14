package bwrap

import (
	"testing"

	"spindrift.dev/launcher/internal/inputdoc"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/runner/runnertest"
	"spindrift.dev/launcher/internal/runner/runnertest/contract"
)

const sandboxConnectorHost = "/nix/store/aaa-connector-host"

func sandboxAdapter(t *testing.T) *bwrapAdapter {
	t.Helper()
	art := inputdoc.BwrapArtifacts{
		ImageTag:         inputdoc.ValueOf("spindrift:test"),
		ConnectorHost:    inputdoc.ValueOf(sandboxConnectorHost),
		ConnectorHostDrv: inputdoc.ValueOf("/nix/store/aaa-connector-host.drv"),
	}
	r, err := New(runner.Config{}, art, runnertest.Lookup(nil), t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r.(*bwrapAdapter)
}

func TestSandboxRenderingContract(t *testing.T) {
	contract.BwrapSandboxRender(t, sandboxAdapter(t).buildSandboxArgs(), sandboxConnectorHost)
}
