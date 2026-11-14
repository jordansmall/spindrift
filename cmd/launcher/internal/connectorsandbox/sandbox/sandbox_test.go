package sandbox

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/connectorsandbox"
	"spindrift.dev/launcher/internal/runner"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	dir := os.Getenv("SPINDRIFT_CONNECTOR_FIXTURES_DIR")
	if dir == "" {
		t.Skip("SPINDRIFT_CONNECTOR_FIXTURES_DIR is unset")
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// inMemoryHost is a StartConnectorSandboxFunc whose sandbox is ServeSessions
// over in-memory pipes.
func inMemoryHost(string) (runner.ConnectorSandbox, error) {
	toHostR, toHostW := io.Pipe()
	fromHostR, fromHostW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := connectorhost.ServeSessions(context.Background(), toHostR, fromHostW)
		_ = fromHostW.Close()
		done <- err
	}()
	return runner.ConnectorSandbox{
		Stdin:  toHostW,
		Stdout: fromHostR,
		Wait:   sync.OnceValue(func() error { return <-done }),
		Stderr: func() string { return "" },
	}, nil
}

func TestTwoLoadsShareOneSandbox(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = inMemoryHost
	s := New(f, "podman")
	wasm := fixture(t, "call.wasm")
	for range 2 {
		c, err := s.Load(context.Background(), wasm)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close(context.Background()) })
	}
	if got := f.ConnectorSandboxStarts; len(got) != 1 || !strings.HasPrefix(got[0], runner.ConnectorSandboxPrefix) {
		t.Fatalf("starts = %v, want exactly one %s name", got, runner.ConnectorSandboxPrefix)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if running, _ := f.ListRunning(); len(running) != 0 {
		t.Errorf("still running after Close: %v", running)
	}
}

func TestNoLoadStartsNothing(t *testing.T) {
	f := runner.NewFake()
	s := New(f, "podman")
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if len(f.ConnectorSandboxStarts) != 0 {
		t.Fatalf("starts = %v, want none", f.ConnectorSandboxStarts)
	}
	if s.Err() != nil {
		t.Fatalf("Err = %v, want nil", s.Err())
	}
}

// notWasm would fail to compile in-process with an error that is not
// connectorsandbox.ErrConnectorSandboxStart, so a load that falls back shows.
var notWasm = []byte("not a wasm module")

func TestStartRefusedFailsClosedAndSticks(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = func(string) (runner.ConnectorSandbox, error) {
		return runner.ConnectorSandbox{}, errors.New("podman: no such image")
	}
	s := New(f, "podman")
	_, err := s.Load(context.Background(), notWasm)
	if !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatalf("Load = %v, want connectorsandbox.ErrConnectorSandboxStart", err)
	}
	for _, want := range []string{"podman", "no such image"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	_, err2 := s.LoadNetwork(context.Background(), notWasm, connectorhost.Network{})
	if !errors.Is(err2, connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatalf("second load = %v, want connectorsandbox.ErrConnectorSandboxStart", err2)
	}
	if len(f.ConnectorSandboxStarts) != 1 {
		t.Fatalf("starts = %v, want one: a failed start is not retried", f.ConnectorSandboxStarts)
	}
	if !errors.Is(s.Err(), connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatalf("Err = %v", s.Err())
	}
}

func TestFirstLoadersCancelledContextDoesNotFailTheRun(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = inMemoryHost
	s := New(f, "oci (podman)")
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.Load(ctx, notWasm)
	if errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) || s.Err() != nil {
		t.Fatalf("Load under a cancelled ctx = %v, Err = %v; want the sandbox started, its hello is not the loader's to cancel", err, s.Err())
	}
}

func TestSandboxThatDiesBeforeHelloNamesItsStderr(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = func(string) (runner.ConnectorSandbox, error) {
		return runner.ConnectorSandbox{
			Stdin:  nopWriteCloser{io.Discard},
			Stdout: strings.NewReader(""),
			Wait:   func() error { return errors.New("exit status 125") },
			Stderr: func() string { return "Error: image not known\n" },
		}, nil
	}
	s := New(f, "podman")
	_, err := s.Load(context.Background(), notWasm)
	if !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatalf("Load = %v, want connectorsandbox.ErrConnectorSandboxStart", err)
	}
	if !strings.Contains(err.Error(), "image not known") {
		t.Errorf("error %q does not carry the sandbox's stderr", err)
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
