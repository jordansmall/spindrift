package sandbox

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/runner"
)

func TestPoolDriverLoadsShareOneStart(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = inMemoryHost
	p := NewPool(f, "podman")
	for range 2 {
		_, _ = p.Load(context.Background(), notWasm)
	}
	if len(f.ConnectorSandboxStarts) != 1 {
		t.Fatalf("starts = %v, want one shared start", f.ConnectorSandboxStarts)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPoolOwnSandboxesStartApartFromTheSharedOne(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = inMemoryHost
	p := NewPool(f, "podman")
	_, _ = p.Load(context.Background(), notWasm)
	a, b := p.Own("a"), p.Own("b")
	if len(f.ConnectorSandboxStarts) != 1 {
		t.Fatalf("starts = %v; Own must start nothing until its first load", f.ConnectorSandboxStarts)
	}
	for _, s := range []connectorsandbox.Sandbox{a, b} {
		for range 2 {
			_, _ = s.LoadNetwork(context.Background(), notWasm, connectorhost.Network{})
		}
	}
	seen := map[string]bool{}
	for _, n := range f.ConnectorSandboxStarts {
		seen[n] = true
	}
	if len(f.ConnectorSandboxStarts) != 3 || len(seen) != 3 {
		t.Fatalf("starts = %v, want three distinct sandboxes", f.ConnectorSandboxStarts)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if running, _ := f.ListRunning(); len(running) != 0 {
		t.Errorf("still running after Close: %v", running)
	}
}

func TestPoolRefusesANetworkLoadAndStartsNothing(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = inMemoryHost
	p := NewPool(f, "podman")
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	_, err := p.LoadNetwork(context.Background(), notWasm, connectorhost.Network{})
	if !errors.Is(err, ErrNetworkInShared) || errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatalf("LoadNetwork = %v, want a refusal that is not a start failure", err)
	}
	if len(f.ConnectorSandboxStarts) != 0 {
		t.Fatalf("starts = %v, want none", f.ConnectorSandboxStarts)
	}
	if p.Err() != nil {
		t.Errorf("Err = %v; a refused load is no sandbox failure", p.Err())
	}
}

func TestPoolCloseStopsEveryStartedSandbox(t *testing.T) {
	f := runner.NewFake()
	var starts atomic.Int32
	f.StartConnectorSandboxFunc = func(name string) (runner.ConnectorSandbox, error) {
		// The third sandbox's host exits before its hello, so its load fails
		// with its runner already started.
		if starts.Add(1) == 3 {
			return runner.ConnectorSandbox{
				Stdin:  nopWriteCloser{io.Discard},
				Stdout: strings.NewReader(""),
				Wait:   func() error { return errors.New("exit status 125") },
				Stderr: func() string { return "" },
			}, nil
		}
		return inMemoryHost(name)
	}
	p := NewPool(f, "podman")
	_, _ = p.Load(context.Background(), notWasm)
	_, _ = p.Own("a").LoadNetwork(context.Background(), notWasm, connectorhost.Network{})
	if _, err := p.Own("b").LoadNetwork(context.Background(), notWasm, connectorhost.Network{}); !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatalf("load into the dying host = %v, want a start failure", err)
	}
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if running, _ := f.ListRunning(); len(running) != 0 {
		t.Errorf("still running after Close: %v", running)
	}
	if got := killCount(f); got < 3 {
		t.Errorf("kills = %v, want a stop of each of the three started sandboxes", f.KillCalls)
	}
}

func TestPoolErrSurfacesAnOwnSandboxsStartFailure(t *testing.T) {
	f := runner.NewFake()
	p := NewPool(f, "podman")
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	if p.Err() != nil {
		t.Fatalf("Err = %v before any load", p.Err())
	}
	// The Fake has no StartConnectorSandboxFunc, so the start refuses.
	_, err := p.Own("github").LoadNetwork(context.Background(), notWasm, connectorhost.Network{})
	if !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatalf("load = %v", err)
	}
	if !errors.Is(p.Err(), connectorsandbox.ErrConnectorSandboxStart) || !strings.Contains(p.Err().Error(), "github") {
		t.Fatalf("Err = %v, want the own sandbox's failure naming the Connector", p.Err())
	}
}

func TestPoolStatsSumEverySandbox(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = inMemoryHost
	p := NewPool(f, "podman")
	if _, ok := p.Stats(); ok {
		t.Fatal("Stats ok before any load")
	}
	_, _ = p.Load(context.Background(), notWasm)
	one, ok := p.Stats()
	if !ok || one.Calls <= 0 {
		t.Fatalf("Stats = %+v, %v", one, ok)
	}
	_, _ = p.Own("a").Load(context.Background(), notWasm)
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	two, ok := p.Stats()
	if !ok || two.Calls <= one.Calls || two.Start <= one.Start {
		t.Fatalf("Stats after an own sandbox served = %+v, want more than %+v", two, one)
	}
}

func TestPoolRetiresAClosedOwnSandboxButKeepsItsCost(t *testing.T) {
	f := runner.NewFake()
	p := NewPool(f, "podman")
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	// The Fake has no StartConnectorSandboxFunc, so the own sandbox's start refuses.
	o := p.Own("github")
	_, _ = o.LoadNetwork(context.Background(), notWasm, connectorhost.Network{})
	f.StartConnectorSandboxFunc = inMemoryHost
	served := p.Own("served")
	_, _ = served.Load(context.Background(), notWasm)
	if len(p.owns) != 2 {
		t.Fatalf("owns = %d before Close, want 2", len(p.owns))
	}
	for range 2 {
		if err := o.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := served.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(p.owns) != 0 {
		t.Fatalf("owns = %d after Close, want the closed sandboxes retired", len(p.owns))
	}
	if err := p.Err(); !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) || !strings.Contains(err.Error(), "github") {
		t.Errorf("Err = %v, want the retired start failure", err)
	}
	st, ok := p.Stats()
	if !ok || st.Calls <= 0 || st.Start <= 0 {
		t.Errorf("Stats = %+v, %v; want the retired sandbox's, counted once", st, ok)
	}
	again, _ := p.Stats()
	if again != st {
		t.Errorf("Stats moved from %+v to %+v with nothing running", st, again)
	}
}

func TestPoolCloseRetriesAnOwnSandboxWhoseStopFailed(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = inMemoryHost
	p := NewPool(f, "podman")
	t.Cleanup(func() { _ = p.Close(context.Background()) })
	o := p.Own("github")
	_, _ = o.LoadNetwork(context.Background(), notWasm, connectorhost.Network{})
	f.KillErr = errors.New("kill timed out")
	if err := o.Close(context.Background()); err == nil {
		t.Fatal("Close = nil, want the failed stop")
	}
	if len(p.owns) != 1 {
		t.Fatalf("owns = %d after a failed stop, want it kept for Pool.Close", len(p.owns))
	}
	f.KillErr = nil
	before := killCount(f)
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := killCount(f); got <= before {
		t.Errorf("kills = %d, want Pool.Close to retry the failed stop", got)
	}
	if running, _ := f.ListRunning(); len(running) != 0 {
		t.Errorf("still running after Close: %v", running)
	}
	if len(p.owns) != 0 {
		t.Errorf("owns = %d after the stop succeeded, want it retired", len(p.owns))
	}
}

func TestPoolOwnAfterCloseStartsNothing(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = inMemoryHost
	p := NewPool(f, "podman")
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := p.Own("late")
	if _, err := s.LoadNetwork(context.Background(), notWasm, connectorhost.Network{}); err == nil {
		t.Fatal("LoadNetwork on a sandbox owned after Pool.Close succeeded")
	}
	if len(f.ConnectorSandboxStarts) != 0 {
		t.Fatalf("starts = %v, want none after Pool.Close", f.ConnectorSandboxStarts)
	}
}
