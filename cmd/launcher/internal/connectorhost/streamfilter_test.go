package connectorhost_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

func TestDriverStreamFilterIsAdvertisedOnlyWithTheCapability(t *testing.T) {
	if strategy(t, "driver.wasm").NewStreamFilter == nil {
		t.Error("driver.wasm: NewStreamFilter is nil")
	}
	s := strategy(t, "seamtest-driver.wasm")
	if s.NewStreamFilter != nil {
		t.Fatal("seamtest-driver.wasm: NewStreamFilter is set without the capability")
	}
	var buf bytes.Buffer
	f := driverkit.New(s).NewStreamFilter(&buf)
	in := "delta x\n\xff partial"
	if _, err := f.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	if buf.String() != in {
		t.Errorf("identity filter wrote %q, want %q", buf.String(), in)
	}
}

// filterOut writes each chunk to a fresh filter, flushes, and returns what
// reached the writer.
func filterOut(t *testing.T, s driverkit.Strategy, chunks ...string) string {
	t.Helper()
	var buf bytes.Buffer
	f := s.NewStreamFilter(&buf)
	for _, c := range chunks {
		if n, err := f.Write([]byte(c)); err != nil || n != len(c) {
			t.Fatalf("Write(%q) = %d, %v", c, n, err)
		}
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestDriverStreamFilterDropsFinishedDeltasAndFlushesOpenOnes(t *testing.T) {
	s := strategy(t, "driver.wasm")
	for _, tc := range []struct{ in, want string }{
		{"a\ndelta 1\ndelta 2\ndone\nb\n", "a\ndone\nb\n"},
		{"delta 1\ndelta 2\n", "delta 1\ndelta 2\n"},
		{"delta 1\ndone\ndelta 2\npart", "done\ndelta 2\npart"},
		{"\xff\xfe\x00\n", "\xff\xfe\x00\n"},
	} {
		if got := filterOut(t, s, tc.in); got != tc.want {
			t.Errorf("filter(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDriverStreamFilterHoldsPartialLinesAcrossWrites(t *testing.T) {
	s := strategy(t, "driver.wasm")
	var buf bytes.Buffer
	f := s.NewStreamFilter(&buf)
	for _, c := range []string{"del", "ta 1\nke", "ep\ndo", "ne\n"} {
		if _, err := f.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if got := buf.String(); got != "keep\ndone\n" {
		t.Errorf("after writes = %q, want %q", got, "keep\ndone\n")
	}
}

func TestDriverStreamFilterChunkingDoesNotChangeTheOutput(t *testing.T) {
	s := strategy(t, "driver.wasm")
	in := "a\ndelta 1\n\xff\ndone\ndelta 2\nlast"
	var bytewise []string
	for i := range len(in) {
		bytewise = append(bytewise, in[i:i+1])
	}
	whole, split := filterOut(t, s, in), filterOut(t, s, bytewise...)
	if whole != split || whole != "a\n\xff\ndone\ndelta 2\nlast" {
		t.Errorf("whole = %q, bytewise = %q", whole, split)
	}
}

func TestDriverStreamFilterFlushIsIdempotent(t *testing.T) {
	s := strategy(t, "driver.wasm")
	var buf bytes.Buffer
	f := s.NewStreamFilter(&buf)
	if _, err := f.Write([]byte("delta 1\npart")); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := f.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if got := buf.String(); got != "delta 1\npart" {
		t.Errorf("after repeated Flush = %q", got)
	}
}

func TestDriverStreamFilterKeepsStatePerInstance(t *testing.T) {
	s := strategy(t, "driver.wasm")
	var a, b bytes.Buffer
	fa, fb := s.NewStreamFilter(&a), s.NewStreamFilter(&b)
	for _, step := range []func() error{
		func() error { _, err := fa.Write([]byte("delta a\n")); return err },
		func() error { _, err := fb.Write([]byte("delta b\ndone\n")); return err },
		fa.Flush,
		fb.Flush,
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if a.String() != "delta a\n" || b.String() != "done\n" {
		t.Errorf("a = %q, b = %q", a.String(), b.String())
	}
}

func TestDriverStreamFilterSplitsAWriteOverThePieceCap(t *testing.T) {
	defer connectorhost.SetStreamPiece(100)()
	s := strategy(t, "driver.wasm")
	// 1300 bytes in 100-byte pieces, so pieces end mid-line.
	in := strings.Repeat("delta y\ndone\n", 100)
	if got, want := filterOut(t, s, in), strings.Repeat("done\n", 100); got != want {
		t.Errorf("split write = %q, want %q", got, want)
	}
}

func TestDriverStreamFilterFailsClosedOnATrap(t *testing.T) {
	s := strategy(t, "driver.wasm")
	var buf bytes.Buffer
	f := s.NewStreamFilter(&buf)
	_, err := f.Write([]byte("ok\nfilter-trap\n"))
	if !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, connectorhost.ErrTrap) {
		t.Fatalf("Write err = %v, want ErrCall and ErrTrap", err)
	}
	if n, err2 := f.Write([]byte("more\n")); n != 0 || err2 == nil || err2.Error() != err.Error() {
		t.Errorf("later Write = %d, %v, want the same error", n, err2)
	}
	if err2 := f.Flush(); err2 == nil || err2.Error() != err.Error() {
		t.Errorf("Flush = %v, want the same error", err2)
	}
	if buf.Len() != 0 {
		t.Errorf("writer received %q after the trap", buf.String())
	}
}

// gateWriter blocks its first Write until released, holding the filter's
// Write mid-piece the way a stalled tee would.
type gateWriter struct {
	entered chan struct{}
	release chan struct{}
	first   atomic.Bool
	mu      sync.Mutex
	buf     bytes.Buffer
}

func (g *gateWriter) Write(p []byte) (int, error) {
	if g.first.CompareAndSwap(false, true) {
		close(g.entered)
		<-g.release
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.buf.Write(p)
}

func (g *gateWriter) String() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.buf.String()
}

// driver-exec flushes from its SIGTERM goroutine while Write is mid-stream:
// Flush must wait for the Write, so the held lines follow its output.
func TestDriverStreamFilterFlushWaitsForAnInFlightWrite(t *testing.T) {
	s := strategy(t, "driver.wasm")
	g := &gateWriter{entered: make(chan struct{}), release: make(chan struct{})}
	f := s.NewStreamFilter(g)
	writeDone, flushDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := f.Write([]byte("delta h\nok\n")); writeDone <- err }()
	<-g.entered
	go func() { flushDone <- f.Flush() }()
	select {
	case err := <-flushDone:
		t.Fatalf("Flush returned (%v) while Write was blocked downstream", err)
	case <-time.After(500 * time.Millisecond):
	}
	close(g.release)
	for _, ch := range []chan error{writeDone, flushDone} {
		if err := <-ch; err != nil {
			t.Fatal(err)
		}
	}
	if got, want := g.String(), "ok\ndelta h\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// The flood is compiled because under the interpreter writing it takes most of
// the 5 s call budget, so a loaded machine fails it on time, not on output.
func TestDriverStreamFilterFailsClosedOnOversizedOutput(t *testing.T) {
	c, err := connectorhost.LoadCompiled(context.Background(), fixture(t, "driver.wasm"), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	s, err := c.DriverStrategy()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	f := s.NewStreamFilter(&buf)
	if _, err := f.Write([]byte("filter-flood\n")); !errors.Is(err, connectorhost.ErrOutputTooLarge) {
		t.Fatalf("Write err = %v, want ErrOutputTooLarge", err)
	}
	if err := f.Flush(); err == nil {
		t.Error("Flush after the failure succeeded")
	}
	if buf.Len() != 0 {
		t.Errorf("writer received %d bytes", buf.Len())
	}
}

// fixtureFloodBytes mirrors floodBytes in nix/fixtures/connector/main.go.
const fixtureFloodBytes = 7 << 20

// The flood fixture only proves the output cap while its base64 result passes
// it; 1 KiB is well above the request the fixture's line encodes to.
func TestFloodFixtureStaysOverTheOutputCap(t *testing.T) {
	_, _, output := connectorhost.LimitsFor(1 << 10)
	if got := uint64(base64.StdEncoding.EncodedLen(fixtureFloodBytes)); got <= output {
		t.Fatalf("flood result of %d bytes no longer passes the %d byte output cap; raise floodBytes", got, output)
	}
}

// failsClosedOn drives the hostile line through a fresh filter and checks it
// fails with want, then stays failed on every later Write and Flush.
func failsClosedOn(t *testing.T, line string, want error) {
	t.Helper()
	s := strategy(t, "driver.wasm")
	var buf bytes.Buffer
	f := s.NewStreamFilter(&buf)
	_, err := f.Write([]byte(line))
	if !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, want) {
		t.Fatalf("Write err = %v, want ErrCall and %v", err, want)
	}
	if n, err2 := f.Write([]byte("more\n")); n != 0 || err2 == nil || err2.Error() != err.Error() {
		t.Errorf("later Write = %d, %v, want the same error", n, err2)
	}
	if err2 := f.Flush(); err2 == nil || err2.Error() != err.Error() {
		t.Errorf("Flush = %v, want the same error", err2)
	}
	if buf.Len() != 0 {
		t.Errorf("writer received %q after the failure", buf.String())
	}
}

func TestDriverStreamFilterFailsClosedOnAnExhaustedBudget(t *testing.T) {
	failsClosedOn(t, "filter-spin\n", connectorhost.ErrBudget)
}

func TestDriverStreamFilterFailsClosedOnTheMemoryCap(t *testing.T) {
	failsClosedOn(t, "filter-hog\n", connectorhost.ErrMemoryCap)
}

func TestDriverStreamFilterBornFailedWhenTheConnectorIsClosed(t *testing.T) {
	c, err := load(t, "driver.wasm")
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.DriverStrategy()
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close(t.Context())
	var buf bytes.Buffer
	f := s.NewStreamFilter(&buf)
	if n, err := f.Write([]byte("x\n")); n != 0 || err == nil {
		t.Errorf("Write = %d, %v, want a failure", n, err)
	}
	if err := f.Flush(); err == nil {
		t.Error("Flush succeeded on a born-failed filter")
	}
	if buf.Len() != 0 {
		t.Errorf("writer received %q", buf.String())
	}
}

type teeFailWriter struct{ err error }

func (w teeFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestDriverStreamFilterFailsClosedOnADownstreamWriteError(t *testing.T) {
	s := strategy(t, "driver.wasm")
	boom := errors.New("tee closed")
	f := s.NewStreamFilter(teeFailWriter{boom})
	if _, err := f.Write([]byte("ok\n")); !errors.Is(err, boom) {
		t.Fatalf("Write err = %v, want boom", err)
	}
	if _, err := f.Write([]byte("more\n")); !errors.Is(err, boom) {
		t.Errorf("Write after a failed tee = %v, want boom", err)
	}
	if err := f.Flush(); !errors.Is(err, boom) {
		t.Errorf("Flush after a failed tee = %v, want boom", err)
	}
}
