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

func TestStatsCountTheStartAndTheExchanges(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = inMemoryHost
	s := New(f, "podman")
	if _, ok := s.Stats(); ok {
		t.Fatal("Stats ok before any load")
	}
	// The load fails to compile, but its exchange with the host still crossed.
	_, _ = s.Load(context.Background(), notWasm)
	st, ok := s.Stats()
	if !ok || st.Start <= 0 || st.Calls <= 0 {
		t.Fatalf("Stats = %+v, %v; want a start time and counted exchanges", st, ok)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after, ok := s.Stats(); !ok || after.Start != st.Start || after.Calls != st.Calls {
		t.Fatalf("Stats after Close = %+v, %v; want %+v", after, ok, st)
	}
}

func TestFailedStartHasNoStats(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = func(string) (runner.ConnectorSandbox, error) {
		return runner.ConnectorSandbox{}, errors.New("podman: no such image")
	}
	s := New(f, "podman")
	_, _ = s.Load(context.Background(), notWasm)
	if st, ok := s.Stats(); ok {
		t.Fatalf("Stats = %+v, true after a failed start", st)
	}
}

func killCount(f *runner.Fake) int {
	return len(f.KillCalls)
}

func TestCloseStopsAStartedSandboxExactlyOnce(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = inMemoryHost
	s := New(f, "podman")
	if _, err := s.Load(context.Background(), notWasm); errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatal(err)
	}
	for range 2 {
		if err := s.Close(context.Background()); err != nil {
			t.Fatalf("Close = %v", err)
		}
	}
	if got := killCount(f); got != 1 {
		t.Fatalf("Kill calls = %v, want exactly one though the host exited on its stdin closing", f.KillCalls)
	}
}

func TestHelloFailureThenCloseStopsOnce(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = func(string) (runner.ConnectorSandbox, error) {
		return runner.ConnectorSandbox{
			Stdin:  nopWriteCloser{io.Discard},
			Stdout: strings.NewReader(""),
			Wait:   func() error { return errors.New("exit status 125") },
			Stderr: func() string { return "" },
		}, nil
	}
	s := New(f, "podman")
	if _, err := s.Load(context.Background(), notWasm); !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatalf("Load = %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if got := killCount(f); got != 1 {
		t.Fatalf("Kill calls = %v, want exactly one", f.KillCalls)
	}
}

func TestRefusedStartIsNeverStopped(t *testing.T) {
	f := runner.NewFake()
	s := New(f, "podman")
	_, _ = s.Load(context.Background(), notWasm)
	_ = s.Close(context.Background())
	if got := killCount(f); got != 0 {
		t.Fatalf("Kill calls = %v, want none for a sandbox that never started", f.KillCalls)
	}
}

func TestNoLoadNeverStops(t *testing.T) {
	f := runner.NewFake()
	s := New(f, "podman")
	_ = s.Close(context.Background())
	if got := killCount(f); got != 0 {
		t.Fatalf("Kill calls = %v, want none", f.KillCalls)
	}
}

// killClosesHost is a Starter whose Kill is what ends the host: the sandbox
// ignores its stdin closing.
type killClosesHost struct {
	*runner.Fake
	stdin io.Closer
}

func (k *killClosesHost) Kill(name string) error {
	_ = k.stdin.Close()
	return k.Fake.Kill(name)
}

func TestHostKilledBySessionsIsNotStoppedTwice(t *testing.T) {
	f := runner.NewFake()
	k := &killClosesHost{Fake: f}
	f.StartConnectorSandboxFunc = func(name string) (runner.ConnectorSandbox, error) {
		sb, err := inMemoryHost(name)
		k.stdin = sb.Stdin
		sb.Stdin = nopWriteCloser{sb.Stdin}
		return sb, err
	}
	s := New(k, "podman")
	if _, err := s.Load(context.Background(), notWasm); errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatal(err)
	}
	// A cancelled ctx makes Sessions.Close kill the host at once rather than
	// after closeGrace.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = s.Close(ctx)
	if got := killCount(f); got != 1 {
		t.Fatalf("Kill calls = %v, want exactly one", f.KillCalls)
	}
}

func TestFailedStopIsRetriedByCloseAndReported(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = inMemoryHost
	f.KillErr = errors.New("podman rm -f: boom")
	var out strings.Builder
	prev := logOut
	logOut = &out
	t.Cleanup(func() { logOut = prev })
	s := New(f, "podman")
	if _, err := s.Load(context.Background(), notWasm); errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatal(err)
	}
	// A cancelled ctx makes Sessions.Close kill the host through its killer,
	// which discards the error; Close's own stop must still surface it. The
	// host may exit on its stdin closing before the killer runs, so only
	// Close's own Kill is certain.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Close(ctx); !errors.Is(err, f.KillErr) {
		t.Fatalf("Close = %v, want the Kill error", err)
	}
	before := killCount(f)
	if before < 1 {
		t.Fatalf("Kill calls = %v, want at least Close's own", f.KillCalls)
	}
	warn := "warning: stopping Connector sandbox " + s.name + ": " + f.KillErr.Error()
	if !strings.Contains(out.String(), warn) {
		t.Fatalf("log = %q, want %q", out.String(), warn)
	}
	if strings.Contains(out.String(), "==> stopped") {
		t.Fatalf("log = %q, want no stopped line before a stop succeeds", out.String())
	}

	f.KillErr = nil
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close after a successful retry = %v", err)
	}
	if got := killCount(f); got != before+1 {
		t.Fatalf("Kill calls = %v, want one more", f.KillCalls)
	}
	for range 3 {
		if err := s.Close(context.Background()); err != nil {
			t.Fatalf("Close = %v", err)
		}
	}
	if got := killCount(f); got != before+1 {
		t.Fatalf("Kill calls = %v, want no more once a stop succeeded", f.KillCalls)
	}
	if got := strings.Count(out.String(), "==> stopped Connector sandbox: "+s.name+"\n"); got != 1 {
		t.Fatalf("stopped lines = %d in %q, want exactly one", got, out.String())
	}
}
