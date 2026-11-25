package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestStartRefusedFailsClosedAndRetries(t *testing.T) {
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
	if len(f.ConnectorSandboxStarts) != 2 {
		t.Fatalf("starts = %v, want two: a failed start is retried by the next use", f.ConnectorSandboxStarts)
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
	warn := "warning: stopping Connector sandbox " + s.cur.name + ": " + f.KillErr.Error()
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
	if got := strings.Count(out.String(), "==> stopped Connector sandbox: "+s.cur.name+"\n"); got != 1 {
		t.Fatalf("stopped lines = %d in %q, want exactly one", got, out.String())
	}
}

// killableHosts starts inMemoryHost sandboxes and keeps each one's stdin, so a
// test can end a host the way a crash would, and whether its Wait was called.
type killableHosts struct {
	mu     sync.Mutex
	stdins []io.Closer
	waited []*atomic.Bool
}

func (h *killableHosts) start(name string) (runner.ConnectorSandbox, error) {
	sb, err := inMemoryHost(name)
	waited := new(atomic.Bool)
	inner := sb.Wait
	sb.Wait = func() error { waited.Store(true); return inner() }
	h.mu.Lock()
	h.stdins = append(h.stdins, sb.Stdin)
	h.waited = append(h.waited, waited)
	h.mu.Unlock()
	return sb, err
}

// reaped is whether the i'th host's Wait has been called.
func (h *killableHosts) reaped(i int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.waited[i].Load()
}

// kill ends the i'th host and waits for its Sandbox's Sessions to notice.
func (h *killableHosts) kill(t *testing.T, i int, ss *connectorhost.Sessions) {
	t.Helper()
	h.mu.Lock()
	_ = h.stdins[i].Close()
	h.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for ss.Dead() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if ss.Dead() == nil {
		t.Fatal("the host's death was not noticed")
	}
}

// reachedHost is whether a load of notWasm got as far as the host, which
// rejects it: a dead host or a failed start fails earlier.
func reachedHost(err error) bool {
	return err != nil && !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) && !strings.Contains(err.Error(), "connector host exited")
}

func TestDeadSandboxRestartsOnNextUse(t *testing.T) {
	f := runner.NewFake()
	h := &killableHosts{}
	f.StartConnectorSandboxFunc = h.start
	s := New(f, "podman")
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if _, err := s.Load(context.Background(), notWasm); !reachedHost(err) {
		t.Fatalf("first Load = %v, want the host's rejection", err)
	}
	old := s.cur.sessions
	first, _ := s.Stats()
	h.kill(t, 0, old)

	if _, err := s.Load(context.Background(), notWasm); !reachedHost(err) {
		t.Fatalf("Load after the host died = %v, want the restarted host's rejection", err)
	}
	if got := f.ConnectorSandboxStarts; len(got) != 2 || got[0] == got[1] {
		t.Fatalf("starts = %v, want two differently named", got)
	}
	if s.Err() != nil {
		t.Fatalf("Err = %v after a successful restart", s.Err())
	}
	// A session the dead host held is not served by its successor.
	if _, err := old.Load(context.Background(), notWasm); !strings.Contains(fmt.Sprint(err), "connector host exited") {
		t.Fatalf("Load on the dead generation = %v, want connector host exited", err)
	}
	st, ok := s.Stats()
	if !ok || st.Calls <= s.cur.sessions.Stats().Calls || st.Start <= s.cur.start {
		t.Fatalf("Stats = %+v, %v; want both generations totalled over the new one's %+v, start %v", st, ok, s.cur.sessions.Stats(), s.cur.start)
	}
	if st.Calls < first.Calls || st.Start < first.Start {
		t.Fatalf("Stats = %+v, want at least the first generation's %+v", st, first)
	}
}

func TestRestartReapsTheDeadGeneration(t *testing.T) {
	f := runner.NewFake()
	h := &killableHosts{}
	f.StartConnectorSandboxFunc = h.start
	s := New(f, "podman")
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	_, _ = s.Load(context.Background(), notWasm)
	h.kill(t, 0, s.cur.sessions)
	if h.reaped(0) {
		t.Fatal("the dead host was reaped before any restart")
	}
	if _, err := s.Load(context.Background(), notWasm); !reachedHost(err) {
		t.Fatalf("Load after the host died = %v, want the restarted host's rejection", err)
	}
	if !h.reaped(0) {
		t.Error("the restart left the dead generation unreaped until Close")
	}
	if h.reaped(1) {
		t.Error("the live generation was reaped")
	}
	if got := killCount(f); got < 1 {
		t.Errorf("Kill calls = %v, want the dead generation stopped on restart", f.KillCalls)
	}
}

func TestConnectorHeldAcrossARestartIsNotServedByTheNewGeneration(t *testing.T) {
	f := runner.NewFake()
	h := &killableHosts{}
	f.StartConnectorSandboxFunc = h.start
	s := New(f, "podman")
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	wasm := fixture(t, "call.wasm")
	held, err := s.Load(context.Background(), wasm)
	if err != nil {
		t.Fatal(err)
	}
	h.kill(t, 0, s.cur.sessions)
	fresh, err := s.Load(context.Background(), wasm)
	if err != nil {
		t.Fatalf("Load after the host died = %v", err)
	}
	var out string
	if err := fresh.Call(context.Background(), "echo", "new", &out); err != nil || out != "new" {
		t.Fatalf("echo on the restarted generation = %q, %v", out, err)
	}
	if err := held.Call(context.Background(), "echo", "old", &out); err == nil {
		t.Fatalf("a Connector of the dead generation was served by the new one: %q", out)
	}
}

func TestFailedRestartFailsTheCallAndTheNextUseTriesAgain(t *testing.T) {
	f := runner.NewFake()
	h := &killableHosts{}
	var refuse atomic.Bool
	f.StartConnectorSandboxFunc = func(name string) (runner.ConnectorSandbox, error) {
		if refuse.Load() {
			return runner.ConnectorSandbox{}, errors.New("podman: no such image")
		}
		return h.start(name)
	}
	s := New(f, "oci (podman)")
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	_, _ = s.Load(context.Background(), notWasm)
	h.kill(t, 0, s.cur.sessions)

	refuse.Store(true)
	for range 2 {
		_, err := s.Load(context.Background(), notWasm)
		if !errors.Is(err, connectorsandbox.ErrConnectorSandboxStart) || !strings.Contains(err.Error(), "runner oci (podman)") {
			t.Fatalf("Load = %v, want connectorsandbox.ErrConnectorSandboxStart naming the runner oci (podman)", err)
		}
	}
	if got := len(f.ConnectorSandboxStarts); got != 3 {
		t.Fatalf("starts = %v, want one try per use", f.ConnectorSandboxStarts)
	}
	if !errors.Is(s.Err(), connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatalf("Err = %v", s.Err())
	}

	refuse.Store(false)
	if _, err := s.Load(context.Background(), notWasm); !reachedHost(err) {
		t.Fatalf("Load once the runner recovers = %v, want the host's rejection", err)
	}
	if !errors.Is(s.Err(), connectorsandbox.ErrConnectorSandboxStart) {
		t.Fatalf("Err = %v after a later successful start, want the earlier start failure kept", s.Err())
	}
}

func TestLoadAfterCloseFailsWithoutStarting(t *testing.T) {
	f := runner.NewFake()
	f.StartConnectorSandboxFunc = inMemoryHost
	s := New(f, "podman")
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(context.Background(), notWasm); !errors.Is(err, errClosed) {
		t.Fatalf("Load after Close = %v, want errClosed", err)
	}
	if len(f.ConnectorSandboxStarts) != 0 {
		t.Fatalf("starts = %v, want none", f.ConnectorSandboxStarts)
	}
}

func TestClosedSandboxIsNotRestartedAfterItsHostDied(t *testing.T) {
	f := runner.NewFake()
	h := &killableHosts{}
	f.StartConnectorSandboxFunc = h.start
	s := New(f, "podman")
	_, _ = s.Load(context.Background(), notWasm)
	h.kill(t, 0, s.cur.sessions)
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if _, err := s.LoadNetwork(context.Background(), notWasm, connectorhost.Network{}); !errors.Is(err, errClosed) {
		t.Fatalf("LoadNetwork after Close = %v, want errClosed", err)
	}
	if len(f.ConnectorSandboxStarts) != 1 {
		t.Fatalf("starts = %v, want the one", f.ConnectorSandboxStarts)
	}
}

func TestTransitionRejectsIllegalMoves(t *testing.T) {
	for _, tc := range []struct{ from, to state }{
		{closed, running},
		{closed, dead},
		{closed, idle},
		{running, running},
		{running, idle},
		{dead, idle},
		{idle, idle},
	} {
		s := &Sandbox{state: tc.from}
		panicked := func() (p any) {
			defer func() { p = recover() }()
			s.transition(tc.to)
			return nil
		}()
		if panicked == nil {
			t.Errorf("%s -> %s did not panic", tc.from, tc.to)
		}
		if s.state != tc.from {
			t.Errorf("%s -> %s left state %s, want it unchanged", tc.from, tc.to, s.state)
		}
	}
}
