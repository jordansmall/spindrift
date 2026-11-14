package connectorhost_test

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/fixturebuild"
)

// processLoader loads through the real connector-host binary as a child
// process, whose stderr goes to t's log.
func processLoader(t testing.TB) loader {
	return func(ctx context.Context, wasm []byte) (*connectorhost.Connector, error) {
		return connectorhost.LoadRemote(ctx, wasm, fixturebuild.ConnectorHostCmd(t))
	}
}

func networkProcessLoader(t testing.TB) netLoader {
	return func(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
		return connectorhost.LoadNetworkRemote(ctx, wasm, n, fixturebuild.ConnectorHostCmd(t))
	}
}

func TestHostBinaryExitsZeroWhenStdinClosesWithNoFrames(t *testing.T) {
	cmd := fixturebuild.ConnectorHostCmd(t)
	cmd.Stdin = bytes.NewReader(nil)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("connector-host = %v, want exit 0 on stdin EOF", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want no frames", out.Bytes())
	}
}

func TestHostBinaryExitsNonZeroOnAGarbageFrame(t *testing.T) {
	cmd := fixturebuild.ConnectorHostCmd(t)
	// A length of 2^32-1 is over the frame cap, so the host refuses the stream.
	cmd.Stdin = bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff, 1})
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	var exit *exec.ExitError
	if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() == 0 {
		t.Fatalf("connector-host = %v, want a non-zero exit", err)
	}
	if stderr.Len() == 0 {
		t.Error("stderr is empty, want the error the host exited on")
	}
}

func TestProcessConnectorCloseLeavesNoRunningChild(t *testing.T) {
	cmd := fixturebuild.ConnectorHostCmd(t)
	c, err := connectorhost.LoadRemote(context.Background(), fixture(t, "call.wasm"), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Process == nil {
		t.Fatal("no host process after a load")
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v, want a host that exits cleanly", err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("host not reaped after Close")
	}
	if !cmd.ProcessState.Exited() || cmd.ProcessState.ExitCode() != 0 {
		t.Errorf("host state = %v, want exit 0", cmd.ProcessState)
	}
}
