package connectorsandbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
)

type failingLoader struct{ err error }

func (f failingLoader) Load(context.Context, []byte) (*connectorhost.Connector, error) {
	return nil, f.err
}

func (f failingLoader) LoadNetwork(context.Context, []byte, connectorhost.Network) (*connectorhost.Connector, error) {
	return nil, f.err
}

func (f failingLoader) Err() error { return f.err }

func (f failingLoader) Stats() (Stats, bool) { return Stats{}, false }

type statsLoader struct {
	failingLoader
	stats Stats
}

func (l statsLoader) Stats() (Stats, bool) { return l.stats, true }

// notWasm fails to compile in-process with an error that is not the loader's,
// so a load that falls back shows.
var notWasm = []byte("not a wasm module")

func TestUseRoutesLoadsToTheInstalledLoader(t *testing.T) {
	if Failure() != nil {
		t.Fatal("Failure with no loader installed")
	}
	restore := Use(failingLoader{ErrConnectorSandboxStart})
	t.Cleanup(restore)
	if _, err := Load(context.Background(), notWasm); !errors.Is(err, ErrConnectorSandboxStart) {
		t.Fatalf("Load = %v, want the installed loader's error", err)
	}
	if _, err := LoadNetwork(context.Background(), notWasm, connectorhost.Network{}); !errors.Is(err, ErrConnectorSandboxStart) {
		t.Fatalf("LoadNetwork = %v, want the installed loader's error", err)
	}
	if !errors.Is(Failure(), ErrConnectorSandboxStart) {
		t.Fatalf("Failure = %v", Failure())
	}
	restore()
	if _, err := Load(context.Background(), notWasm); !errors.Is(err, ErrNoConnectorSandbox) {
		t.Fatalf("Load after restore = %v, want ErrNoConnectorSandbox", err)
	}
	if Failure() != nil {
		t.Fatal("Failure after restore")
	}
}

func TestReportIsTheInstalledLoadersStats(t *testing.T) {
	if _, ok := Report(); ok {
		t.Fatal("Report ok with no loader installed")
	}
	restore := Use(failingLoader{ErrConnectorSandboxStart})
	t.Cleanup(restore)
	if _, ok := Report(); ok {
		t.Fatal("Report ok for a loader whose sandbox did not start")
	}
	want := Stats{Start: time.Second, HopStats: connectorhost.HopStats{Calls: 3, HostCalls: 2, Hop: time.Millisecond}}
	Use(statsLoader{stats: want})
	if got, ok := Report(); !ok || got != want {
		t.Fatalf("Report = %v, %v; want %v, true", got, ok, want)
	}
}

// notWasm would fail with a compile error, not ErrNoConnectorSandbox, if the
// seam ever reached the in-process engine.
func TestLoadsFailClosedWithNoLoaderInstalled(t *testing.T) {
	t.Cleanup(Use(nil))
	if _, err := Load(context.Background(), notWasm); !errors.Is(err, ErrNoConnectorSandbox) {
		t.Fatalf("Load = %v, want ErrNoConnectorSandbox", err)
	}
	if _, err := LoadNetwork(context.Background(), notWasm, connectorhost.Network{}); !errors.Is(err, ErrNoConnectorSandbox) {
		t.Fatalf("LoadNetwork = %v, want ErrNoConnectorSandbox", err)
	}
}
