package forge

import (
	"context"
	"os/exec"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

// UseRemoteSeam points every Fake at a fake Connector run by a host process
// newCmd starts, until the test ends. newCmd is called per load, since an
// exec.Cmd runs once and a failed call retires the Connector for a reload. It
// swaps the package-global seam, so no test in this package may run in parallel
// with one that calls it.
func UseRemoteSeam(t *testing.T, newCmd func() *exec.Cmd) {
	t.Helper()
	restore, err := SwapFakeLoader(func(wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
		return connectorhost.LoadNetworkRemote(context.Background(), wasm, n, newCmd())
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
}
