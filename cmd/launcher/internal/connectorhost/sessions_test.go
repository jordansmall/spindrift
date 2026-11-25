package connectorhost_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/fixturebuild"
)

// sessionsHost runs ServeSessions in a goroutine over in-memory pipes and
// returns the Launcher's Sessions over its far end.
func sessionsHost(t *testing.T) *connectorhost.Sessions {
	t.Helper()
	toHostR, toHostW := io.Pipe()
	fromHostR, fromHostW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := connectorhost.ServeSessions(context.Background(), toHostR, fromHostW)
		_ = fromHostW.Close()
		done <- err
	}()
	wait := sync.OnceValue(func() error { return <-done })
	killed := errors.New("host killed")
	s, err := connectorhost.NewSessions(context.Background(), fromHostR, toHostW,
		func() { _ = toHostR.CloseWithError(killed); _ = fromHostR.CloseWithError(killed) }, wait)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

func echoes(t *testing.T, c *connectorhost.Connector, msg string) {
	t.Helper()
	var out string
	if err := c.Call(context.Background(), "echo", msg, &out); err != nil || out != msg {
		t.Errorf("echo %q = %q, %v", msg, out, err)
	}
}

func TestSessionsTwoConnectorsShareOneHost(t *testing.T) {
	s := sessionsHost(t)
	a, err := s.Load(context.Background(), fixture(t, "call.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Load(context.Background(), fixture(t, "call.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, c := range []*connectorhost.Connector{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				echoes(t, c, "ping")
			}
		}()
	}
	wg.Wait()
	for _, c := range []*connectorhost.Connector{a, b} {
		if err := c.Close(context.Background()); err != nil {
			t.Errorf("Connector Close = %v", err)
		}
	}
	if err := s.Close(context.Background()); err != nil {
		t.Errorf("Sessions Close = %v, want a host that exits cleanly", err)
	}
}

func TestSessionsAbortInOneConnectorLeavesTheOtherWorking(t *testing.T) {
	t.Parallel()
	s := sessionsHost(t)
	bad, err := s.Load(context.Background(), fixture(t, "hostile-loop.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	good, err := s.Load(context.Background(), fixture(t, "call.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- bad.Call(context.Background(), "misbehave", nil, nil) }()
	time.Sleep(200 * time.Millisecond)
	echoes(t, good, "during")
	bad.Abort()
	select {
	case err := <-errc:
		if !errors.Is(err, connectorhost.ErrCall) {
			t.Errorf("aborted call err = %v, want ErrCall", err)
		}
	case <-time.After(connectorhost.CallBudget):
		t.Fatal("aborted call still running")
	}
	echoes(t, good, "after")
	// The abort ended one session, not the host: a new Connector still loads.
	again, err := s.Load(context.Background(), fixture(t, "call.wasm"))
	if err != nil {
		t.Fatalf("Load after an abort: %v", err)
	}
	echoes(t, again, "again")
}

func TestSessionsBudgetOverrunInOneConnectorLeavesTheOtherWorking(t *testing.T) {
	useShortCallBudget(t)
	s := sessionsHost(t)
	bad, err := s.Load(context.Background(), fixture(t, "hostile-loop.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	good, err := s.Load(context.Background(), fixture(t, "call.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	err = bad.Call(context.Background(), "misbehave", nil, nil)
	if !errors.Is(err, connectorhost.ErrBudget) {
		t.Errorf("err = %v, want ErrBudget", err)
	}
	echoes(t, good, "after")
}

// fakeHost is a Sessions peer the test scripts: it says hello, then run drives
// the stream. kills counts how often the Launcher killed it.
func fakeHost(t *testing.T, run func(fromLauncher io.Reader, toLauncher io.Writer)) (*connectorhost.Sessions, *atomic.Int32) {
	t.Helper()
	toHostR, toHostW := io.Pipe()
	fromHostR, fromHostW := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer fromHostW.Close()
		_ = connectorhost.WriteChunk(fromHostW, 0, nil)
		run(toHostR, fromHostW)
	}()
	var kills atomic.Int32
	s, err := connectorhost.NewSessions(context.Background(), fromHostR, toHostW,
		func() {
			kills.Add(1)
			_ = toHostR.Close()
			_ = fromHostR.Close()
		},
		func() error { <-done; return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s, &kills
}

// loadsFail starts two loads against s and returns their errors once both end.
func loadsFail(t *testing.T, s *connectorhost.Sessions) []error {
	t.Helper()
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			_, errs[i] = s.Load(ctx, []byte("\x00asm"))
		}()
	}
	wg.Wait()
	return errs
}

func TestSessionsHostFloodingASessionFailsEverySessionAndIsKilled(t *testing.T) {
	defer connectorhost.SetSessionBufCap(1 << 10)()
	s, kills := fakeHost(t, func(in io.Reader, out io.Writer) {
		if !awaitSessions(in, 2) {
			return
		}
		// One chunk over the bound, whatever the engine has read.
		_ = connectorhost.WriteChunk(out, 1, make([]byte, 2<<10))
	})
	for i, err := range loadsFail(t, s) {
		if !errors.Is(err, connectorhost.ErrTrap) {
			t.Errorf("load %d err = %v, want ErrTrap", i, err)
		}
	}
	if kills.Load() == 0 {
		t.Error("host not killed after flooding a session")
	}
}

func TestSessionsHostEndingItsStreamFailsEverySessionAndIsKilled(t *testing.T) {
	s, kills := fakeHost(t, func(in io.Reader, _ io.Writer) {
		if !awaitSessions(in, 2) {
			return
		}
	})
	for i, err := range loadsFail(t, s) {
		if !errors.Is(err, connectorhost.ErrTrap) {
			t.Errorf("load %d err = %v, want ErrTrap", i, err)
		}
	}
	if kills.Load() == 0 {
		t.Error("host not killed after its stream ended")
	}
}

func TestSessionsHostSendingAMalformedChunkFailsEverySession(t *testing.T) {
	s, kills := fakeHost(t, func(in io.Reader, out io.Writer) {
		if !awaitSessions(in, 2) {
			return
		}
		// A length over the cap.
		_, _ = out.Write([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 1})
	})
	for i, err := range loadsFail(t, s) {
		if err == nil {
			t.Errorf("load %d succeeded", i)
		}
	}
	if kills.Load() == 0 {
		t.Error("host not killed after a malformed chunk")
	}
}

func TestSessionsFrameForASessionNeverOpenedFailsThePipe(t *testing.T) {
	s, kills := fakeHost(t, func(in io.Reader, out io.Writer) {
		if !awaitSessions(in, 2) {
			return
		}
		// Two sessions are open, so id 3 was never issued.
		_ = connectorhost.WriteChunk(out, 3, []byte("noise"))
	})
	for i, err := range loadsFail(t, s) {
		if err == nil {
			t.Errorf("load %d succeeded", i)
		}
	}
	if err := s.Dead(); !errors.Is(err, connectorhost.ErrMalformedOutput) || !strings.Contains(err.Error(), "session 3") {
		t.Errorf("Dead = %v, want ErrMalformedOutput naming session 3", err)
	}
	if kills.Load() == 0 {
		t.Error("host not killed after a frame for a session never opened")
	}
}

func TestSessionsFrameForAnEndedSessionIsDropped(t *testing.T) {
	s, kills := fakeHost(t, func(in io.Reader, out io.Writer) {
		_, _, _ = connectorhost.ReadChunk(in)
		// The host ends session 1, then a late frame for it arrives.
		_ = connectorhost.WriteChunk(out, 1, nil)
		_ = connectorhost.WriteChunk(out, 1, []byte("late"))
		// Stay up until the Launcher closes the pipe.
		_, _ = io.Copy(io.Discard, in)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, _ = s.Load(ctx, []byte("\x00asm"))
	if err := s.Dead(); err != nil {
		t.Errorf("Dead = %v, want a pipe that still serves", err)
	}
	if kills.Load() > 1 {
		t.Errorf("kills = %d, want at most the load's own", kills.Load())
	}
}

func TestNewSessionsErrorsWhenTheStreamEndsBeforeHello(t *testing.T) {
	var killed atomic.Bool
	_, err := connectorhost.NewSessions(context.Background(), bytes.NewReader(nil), nopWC{io.Discard},
		func() { killed.Store(true) }, func() error { return nil })
	if err == nil {
		t.Fatal("NewSessions succeeded without a hello")
	}
	if !killed.Load() {
		t.Error("host not killed")
	}
}

func TestNewSessionsErrorsWhenHelloDoesNotComeInTime(t *testing.T) {
	r, w := io.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := connectorhost.NewSessions(ctx, r, nopWC{io.Discard},
		func() { _ = r.Close(); _ = w.Close() }, func() error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's deadline", err)
	}
}

type nopWC struct{ io.Writer }

func (nopWC) Close() error { return nil }

func TestServeSessionsSaysHelloAndEndsCleanlyOnEOF(t *testing.T) {
	var out bytes.Buffer
	if err := connectorhost.ServeSessions(context.Background(), bytes.NewReader(nil), &out); err != nil {
		t.Fatal(err)
	}
	if want := []byte{0, 0, 0, 4, 0, 0, 0, 0}; !bytes.Equal(out.Bytes(), want) {
		t.Errorf("stdout = %v, want the hello chunk %v", out.Bytes(), want)
	}
}

func TestServeSessionsRefusesAMalformedChunk(t *testing.T) {
	err := connectorhost.ServeSessions(context.Background(), bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 1}), io.Discard)
	if err == nil {
		t.Fatal("ServeSessions accepted a chunk over the cap")
	}
}

func TestServeSessionsRefusesTheReservedSession(t *testing.T) {
	var in bytes.Buffer
	_ = connectorhost.WriteChunk(&in, 0, []byte("x"))
	if err := connectorhost.ServeSessions(context.Background(), &in, io.Discard); err == nil {
		t.Fatal("ServeSessions accepted a chunk for the hello session")
	}
}

// Two Connectors in one real connector-host process.
func TestSessionsOverTheHostBinary(t *testing.T) {
	t.Parallel()
	cmd := fixturebuild.ConnectorHostCmd(t)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s, err := connectorhost.NewSessions(context.Background(), out, in,
		func() { _ = cmd.Process.Kill() }, cmd.Wait)
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Load(context.Background(), fixture(t, "call.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Load(context.Background(), fixture(t, "hostile-trap.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Call(context.Background(), "misbehave", nil, nil); !errors.Is(err, connectorhost.ErrTrap) {
		t.Errorf("trap err = %v, want ErrTrap", err)
	}
	echoes(t, a, "still here")
	_ = a.Close(context.Background())
	_ = b.Close(context.Background())
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 0 {
		t.Errorf("host state = %v, want exit 0", cmd.ProcessState)
	}
}

// awaitSessions reads chunks from the Launcher until n distinct sessions have
// sent one, which is when n loads are in flight.
func awaitSessions(in io.Reader, n int) bool {
	seen := map[uint32]bool{}
	for len(seen) < n {
		id, _, err := connectorhost.ReadChunk(in)
		if err != nil {
			return false
		}
		seen[id] = true
	}
	return true
}

func TestSessionsStatsStartAtZero(t *testing.T) {
	if got := sessionsHost(t).Stats(); got != (connectorhost.HopStats{}) {
		t.Errorf("fresh Stats = %+v, want zero", got)
	}
}

func TestSessionsStatsCountEveryExchange(t *testing.T) {
	s := sessionsHost(t)
	c, err := s.Load(context.Background(), fixture(t, "call.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	loaded := s.Stats()
	if loaded.Calls == 0 || loaded.Hop <= 0 {
		t.Fatalf("Stats after Load = %+v, want the load and handshake counted and timed", loaded)
	}
	const calls = 5
	for range calls {
		echoes(t, c, "ping")
	}
	got := s.Stats()
	if got.Calls != loaded.Calls+calls || got.Hop <= loaded.Hop || got.HostCalls != 0 {
		t.Errorf("Stats = %+v after %+v and %d calls, want %d more Calls, more Hop, no HostCalls", got, loaded, calls, calls)
	}
	_ = c.Close(context.Background())
	_ = s.Close(context.Background())
	if after := s.Stats(); after.Calls < got.Calls {
		t.Errorf("Stats after Close = %+v, want at least %+v", after, got)
	}
}

func TestSessionsStatsCountHostCalls(t *testing.T) {
	s := sessionsHost(t)
	c, _, _, params := loadNetworkFixtureVia(t, s.LoadNetwork, "network.wasm", func(*connectorhost.Network) {})
	before := s.Stats()
	if err := c.Call(context.Background(), "fetch", params, new(fetched)); err != nil {
		t.Fatal(err)
	}
	got := s.Stats()
	if got.HostCalls != 1 || got.Calls != before.Calls+1 {
		t.Errorf("Stats = %+v after %+v and one fetch, want one Host call over one more exchange", got, before)
	}
}

// A Connector killed by a fatal call must not cost the host: its session is
// released and the same Sessions loads a replacement that answers calls.
func TestSessionsFatalCallReleasesTheSessionAndStillLoadsAReplacement(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		fixture string
		want    error
	}{
		{"hostile-trap.wasm", connectorhost.ErrTrap},
		{"hostile-malformed.wasm", connectorhost.ErrMalformedOutput},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			s := sessionsHost(t)
			dead, err := s.Load(context.Background(), fixture(t, tc.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if err := dead.Call(context.Background(), "misbehave", nil, nil); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if dead.Err() == nil {
				t.Error("Err = nil after a fatal call, want the failure")
			}
			deadline := time.Now().Add(5 * time.Second)
			for s.OpenSessions() != 0 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if n := s.OpenSessions(); n != 0 {
				t.Errorf("open sessions = %d after the Connector died, want 0", n)
			}
			fresh, err := s.Load(context.Background(), fixture(t, "call.wasm"))
			if err != nil {
				t.Fatalf("Load after a fatal call: %v", err)
			}
			echoes(t, fresh, "replacement")
		})
	}
}

func TestSessionsDeadReportsAHostThatEnded(t *testing.T) {
	toHostR, toHostW := io.Pipe()
	fromHostR, fromHostW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := connectorhost.ServeSessions(context.Background(), toHostR, fromHostW)
		_ = fromHostW.Close()
		done <- err
	}()
	s, err := connectorhost.NewSessions(context.Background(), fromHostR, toHostW, func() {}, sync.OnceValue(func() error { return <-done }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if err := s.Dead(); err != nil {
		t.Fatalf("Dead = %v on a serving host", err)
	}
	_ = toHostW.Close()
	deadline := time.Now().Add(5 * time.Second)
	for s.Dead() == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.Dead() == nil {
		t.Fatal("Dead = nil after the host ended")
	}
}
