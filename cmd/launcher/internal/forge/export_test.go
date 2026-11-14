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
	remote := func(wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
		return connectorhost.LoadNetworkRemote(context.Background(), wasm, n, newCmd())
	}
	c, err := loadFakeConnectorVia(remote)
	if err != nil {
		t.Fatal(err)
	}
	seamMu.Lock()
	savedConn, savedLoad := seamConn, fakeLoad
	seamConn, fakeLoad = c, remote
	seamMu.Unlock()
	t.Cleanup(func() {
		seamMu.Lock()
		cur := seamConn
		seamConn, fakeLoad = savedConn, savedLoad
		seamMu.Unlock()
		if cur != nil {
			_ = cur.Close(context.Background())
		}
	})
}
