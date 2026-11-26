package connectorruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorruntime"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/connectorsandbox/connectorsandboxtest"
	"spindrift.dev/launcher/internal/connectortracker"
	"spindrift.dev/launcher/internal/credsource"
	"spindrift.dev/launcher/internal/driver/driverrows"
)

var errDriverLoad = errors.New("driver load reached the shared sandbox")

// ownedSandbox is one per-row sandbox the double handed out.
type ownedSandbox struct {
	connectorsandbox.Loader
	name string
	// failWith, when set before the first load, fails every load.
	failWith error

	mu     sync.Mutex
	loads  int
	closed bool
}

func (o *ownedSandbox) LoadNetwork(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
	o.mu.Lock()
	o.loads++
	fail := o.failWith
	o.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	return o.Loader.LoadNetwork(ctx, wasm, n)
}

func (o *ownedSandbox) Close(context.Context) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	return nil
}

// sandboxes is a connectorsandbox.OwningLoader whose shared sandbox takes Driver
// loads only, like the production Pool's.
type sandboxes struct {
	connectorsandbox.Loader
	// failRow names the rows whose sandbox fails every load.
	failRow map[string]error

	mu            sync.Mutex
	owned         []*ownedSandbox
	driverLoads   int
	sharedNetwork int
}

func newSandboxes() *sandboxes { return &sandboxes{Loader: connectorsandboxtest.InProcess()} }

func (s *sandboxes) Load(context.Context, []byte) (*connectorhost.Connector, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.driverLoads++
	return nil, errDriverLoad
}

func (s *sandboxes) LoadNetwork(context.Context, []byte, connectorhost.Network) (*connectorhost.Connector, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sharedNetwork++
	return nil, errors.New("a network Connector must not load into the shared sandbox")
}

func (s *sandboxes) Own(name string) connectorsandbox.Sandbox {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := &ownedSandbox{Loader: connectorsandboxtest.InProcess(), name: name, failWith: s.failRow[name]}
	s.owned = append(s.owned, o)
	return o
}

func (s *sandboxes) ownedBy(name string) []*ownedSandbox {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*ownedSandbox
	for _, o := range s.owned {
		if o.name == name {
			out = append(out, o)
		}
	}
	return out
}

// twoRows is a runtime over two rows of one fixture Connector, built over the
// sandboxes sbs.
func twoRows(t *testing.T, sbs *sandboxes) (rt *connectorruntime.Runtime, a, b backendrows.Row, spec connectorruntime.SessionSpec) {
	t.Helper()
	e := newEnv(t, "tracker.wasm")
	a, b = e.row, e.row
	a.Name, b.Name = "row-a", "row-b"
	rt = connectorruntime.New([]backendrows.Row{a, b}, map[string]credsource.Row{a.Name: {Secret: credsource.Static("tok")}, b.Name: {Secret: credsource.Static("tok")}}, sbs)
	t.Cleanup(func() { _ = rt.Close() })
	return rt, a, b, e.spec.SessionSpec
}

func TestEachRowLoadsIntoItsOwnSandboxAndDriversIntoTheSharedOne(t *testing.T) {
	sbs := newSandboxes()
	rt, a, b, spec := twoRows(t, sbs)

	if _, err := rt.Connector(a, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Connector(b, spec); err != nil {
		t.Fatal(err)
	}
	other := spec
	other.Target = connectortracker.Target{API: spec.Target.API, Repo: "o/other"}
	if _, err := rt.Connector(a, other); err != nil {
		t.Fatal(err)
	}
	// The Runtime never loads a Driver, so the shared sandbox is reached directly.
	if _, err := sbs.Load(context.Background(), []byte("driver")); !errors.Is(err, errDriverLoad) {
		t.Fatalf("Driver load = %v, want the shared sandbox's", err)
	}

	for name, wantLoads := range map[string]int{"row-a": 2, "row-b": 1} {
		got := sbs.ownedBy(name)
		if len(got) != 1 {
			t.Fatalf("sandboxes started for %s = %d, want 1, shared by its Targets", name, len(got))
		}
		if got[0].loads != wantLoads {
			t.Errorf("%s loads = %d, want %d", name, got[0].loads, wantLoads)
		}
	}
	if len(sbs.owned) != 2 {
		t.Errorf("owned sandboxes = %d, want 2", len(sbs.owned))
	}
	if sbs.driverLoads != 1 || sbs.sharedNetwork != 0 {
		t.Errorf("shared sandbox saw %d Driver and %d network loads, want 1 and 0", sbs.driverLoads, sbs.sharedNetwork)
	}
}

func TestCloseStopsEverySandboxIncludingAfterALoadFailure(t *testing.T) {
	loadErr := errors.New("row-b's guest would not load")
	sbs := newSandboxes()
	sbs.failRow = map[string]error{"row-b": loadErr}
	rt, a, b, spec := twoRows(t, sbs)

	if _, err := rt.Connector(a, spec); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Connector(b, spec); !errors.Is(err, loadErr) {
		t.Fatalf("row-b load = %v, want %v", err, loadErr)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if len(sbs.owned) != 2 {
		t.Fatalf("owned sandboxes = %d, want 2", len(sbs.owned))
	}
	for _, o := range sbs.owned {
		if !o.closed {
			t.Errorf("sandbox of %s still running after Close", o.name)
		}
	}

	if _, err := rt.Connector(a, spec); !errors.Is(err, connectorruntime.ErrClosed) {
		t.Errorf("load after Close = %v, want ErrClosed", err)
	}
	if len(sbs.owned) != 2 {
		t.Errorf("a load after Close started a sandbox: %d owned", len(sbs.owned))
	}
}

func TestReloadAfterCloseStartsNoSandbox(t *testing.T) {
	sbs := newSandboxes()
	rt, a, _, spec := twoRows(t, sbs)
	c, err := rt.Connector(a, spec)
	if err != nil {
		t.Fatal(err)
	}
	c.Abort() // the next request reloads
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := rt.Connector(a, spec); !errors.Is(err, connectorruntime.ErrClosed) {
		t.Fatalf("reload after Close = %v, want ErrClosed", err)
	}
	if got := sbs.ownedBy("row-a"); len(got) != 1 || !got[0].closed || got[0].loads != 1 {
		t.Errorf("row-a sandbox = %+v, want one, closed, loaded once", got)
	}
}

func TestOnCloseRunsOnceInOrderAfterTheSandboxesStopped(t *testing.T) {
	sbs := newSandboxes()
	rt, a, _, spec := twoRows(t, sbs)
	if _, err := rt.Connector(a, spec); err != nil {
		t.Fatal(err)
	}
	var order []string
	hookErr := errors.New("hook failed")
	rt.OnClose(func() error {
		if o := sbs.ownedBy("row-a"); len(o) != 1 || !o[0].closed {
			t.Error("OnClose ran before the row's sandbox stopped")
		}
		order = append(order, "first")
		return hookErr
	})
	rt.OnClose(func() error { order = append(order, "second"); return nil })

	if err := rt.Close(); !errors.Is(err, hookErr) {
		t.Errorf("Close = %v, want the hook's error joined", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "first,second" {
		t.Errorf("OnClose ran %q, want each once in registration order", got)
	}
}

// A runtime built with no sandbox fails every backend and Driver load with
// ErrNoConnectorSandbox, never loading in-process.
func TestNilSandboxFailsBackendAndDriverLoads(t *testing.T) {
	e := newEnv(t, "tracker.wasm")
	rt := connectorruntime.New([]backendrows.Row{e.row}, nil, nil)
	t.Cleanup(func() { _ = rt.Close() })
	rt.UseDriverRows(hostedDriverRows(t))

	if rt.HasSandbox() {
		t.Error("HasSandbox = true for a runtime built with none")
	}
	if _, err := rt.Connector(e.row, e.spec.SessionSpec); !errors.Is(err, connectorsandbox.ErrNoConnectorSandbox) {
		t.Fatalf("Connector err = %v, want ErrNoConnectorSandbox", err)
	}
	if _, err := rt.Drivers().New("outsider"); !errors.Is(err, connectorsandbox.ErrNoConnectorSandbox) {
		t.Fatalf("Drivers().New err = %v, want ErrNoConnectorSandbox", err)
	}
	if err := rt.SandboxFailure(); err != nil {
		t.Errorf("SandboxFailure = %v, want nil with no sandbox", err)
	}
}

// startFailingSandbox is a sandbox whose runner did not start. Loads fall to
// the embedded in-process engine only if the runtime bypasses it.
type startFailingSandbox struct{ connectorsandbox.Loader }

var errRunnerRefused = fmt.Errorf("%w: runner refused", connectorsandbox.ErrConnectorSandboxStart)

func (startFailingSandbox) Load(context.Context, []byte) (*connectorhost.Connector, error) {
	return nil, errRunnerRefused
}

func (startFailingSandbox) Err() error { return errRunnerRefused }

// hostedDriverRows is one Driver Connector row whose module is not wasm, so a
// load that reached the in-process engine would fail with a compile error.
func hostedDriverRows(t *testing.T) driverrows.Rows {
	t.Helper()
	module := filepath.Join(t.TempDir(), "outsider.wasm")
	if err := os.WriteFile(module, []byte("not wasm"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]map[string]any{{"name": "outsider", "module": module}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := driverrows.Parse(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// A Launcher runtime whose sandbox cannot start fails a Driver Connector's
// load closed with the sandbox-start error, not by loading it in-process.
func TestDriverConnectorLoadFailsClosedWhenTheSandboxCannotStart(t *testing.T) {
	t.Parallel()
	sb := startFailingSandbox{connectorsandboxtest.InProcess()}
	rt := connectorruntime.FromRegistries(nil, sb)
	t.Cleanup(func() { _ = rt.Close() })
	rt.UseDriverRows(hostedDriverRows(t))

	_, err := rt.Drivers().New("outsider")
	if !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatalf("Drivers().New = %v, want ErrConnectorSandboxStart", err)
	}
	if got := rt.SandboxFailure(); !errors.Is(got, connectorsandbox.ErrConnectorSandboxStart) {
		t.Errorf("SandboxFailure = %v, want the start failure", got)
	}
}

// failingSandbox reports a start failure, as a runner that did not start does.
type failingSandbox struct {
	connectorsandbox.Loader
	err error
}

func (s failingSandbox) Err() error { return s.err }

func TestSandboxFailureIsTheRuntimesSandboxStartFailure(t *testing.T) {
	t.Parallel()
	startErr := errors.New("runner did not start")
	sb := failingSandbox{Loader: connectorsandboxtest.InProcess(), err: startErr}
	rt := connectorruntime.FromRegistries(nil, sb)
	t.Cleanup(func() { _ = rt.Close() })

	if !rt.HasSandbox() {
		t.Error("HasSandbox = false for a runtime built with one")
	}
	if got := rt.SandboxFailure(); !errors.Is(got, startErr) {
		t.Errorf("SandboxFailure = %v, want %v", got, startErr)
	}
	if rt.Sandbox() != connectorsandbox.Loader(sb) {
		t.Error("Sandbox is not the one the runtime was built with")
	}
}
