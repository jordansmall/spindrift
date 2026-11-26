package connectorruntime_test

import (
	"context"
	"errors"
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

// twoRows is a runtime over two rows of one fixture Connector, built with a
// nil sandbox so it resolves the one installed.
func twoRows(t *testing.T, sbs *sandboxes) (rt *connectorruntime.Runtime, a, b backendrows.Row, spec connectorruntime.SessionSpec) {
	t.Helper()
	e := newEnv(t, "tracker.wasm")
	a, b = e.row, e.row
	a.Name, b.Name = "row-a", "row-b"
	t.Cleanup(connectorsandbox.Use(sbs))
	rt = connectorruntime.New([]backendrows.Row{a, b}, map[string]credsource.Row{a.Name: {Secret: credsource.Static("tok")}, b.Name: {Secret: credsource.Static("tok")}}, nil)
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
	if _, err := connectorsandbox.Load(context.Background(), []byte("driver")); !errors.Is(err, errDriverLoad) {
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
