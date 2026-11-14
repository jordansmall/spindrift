package connectorhost_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

// loader loads a Connector from wasm; the hostile and load-failure tables run
// over each so the in-process and remote engines are held to the same errors.
type loader func(ctx context.Context, wasm []byte) (*connectorhost.Connector, error)

func loadersFor(t testing.TB) map[string]loader {
	return map[string]loader{
		"in-process": connectorhost.Load,
		"process":    processLoader(t),
		"remote": func(ctx context.Context, wasm []byte) (*connectorhost.Connector, error) {
			return loadRemoteOver(ctx, wasm, nil, nil)
		},
	}

}

// loadRemoteOver loads wasm through a connector host run as Serve in a
// goroutine over in-memory pipes. Every byte the Launcher sends the host is also
// written to tap, when non-nil.
func loadRemoteOver(ctx context.Context, wasm []byte, n *connectorhost.Network, tap io.Writer) (*connectorhost.Connector, error) {
	toHostR, toHostW := io.Pipe()
	fromHostR, fromHostW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		err := connectorhost.Serve(context.Background(), toHostR, fromHostW)
		_ = fromHostW.Close()
		done <- err
	}()
	var w io.WriteCloser = toHostW
	if tap != nil {
		w = struct {
			io.Writer
			io.Closer
		}{io.MultiWriter(toHostW, tap), toHostW}
	}
	killed := errors.New("host killed")
	return connectorhost.LoadRemoteOver(ctx, wasm, n, fromHostR, w,
		func() { _ = toHostR.CloseWithError(killed); _ = fromHostR.CloseWithError(killed) },
		func() error { return <-done })
}

func TestFrameRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := connectorhost.WriteFrame(&buf, connectorhost.KindCall, []byte("ab"), nil, []byte("cd")); err != nil {
		t.Fatal(err)
	}
	if want := []byte{0, 0, 0, 5, byte(connectorhost.KindCall), 'a', 'b', 'c', 'd'}; !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("frame = %v, want %v", buf.Bytes(), want)
	}
	if err := connectorhost.WriteFrame(&buf, connectorhost.KindResult); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		kind    byte
		payload string
	}{{connectorhost.KindCall, "abcd"}, {connectorhost.KindResult, ""}} {
		kind, payload, err := connectorhost.ReadFrame(&buf)
		if err != nil || kind != want.kind || string(payload) != want.payload {
			t.Fatalf("ReadFrame = %d, %q, %v, want %d, %q", kind, payload, err, want.kind, want.payload)
		}
	}
	if _, _, err := connectorhost.ReadFrame(&buf); err != io.EOF {
		t.Fatalf("ReadFrame at a frame boundary = %v, want io.EOF", err)
	}
}

func TestFrameWriteRefusesAFrameOverTheCap(t *testing.T) {
	var buf bytes.Buffer
	if err := connectorhost.WriteFrame(&buf, connectorhost.KindCall, make([]byte, connectorhost.MaxFrame)); err == nil {
		t.Fatal("WriteFrame accepted a frame one byte over the cap")
	}
	if buf.Len() != 0 {
		t.Errorf("WriteFrame wrote %d bytes of a refused frame", buf.Len())
	}
}

// failAfter errors if more than n bytes are read, which an allocation of the
// announced length followed by a read would do.
type failAfter struct {
	r io.Reader
	n int
}

func (f *failAfter) Read(p []byte) (int, error) {
	if f.n <= 0 {
		return 0, errors.New("read past the frame header")
	}
	n, err := f.r.Read(p[:min(len(p), f.n)])
	f.n -= n
	return n, err
}

func TestFrameReadRefusesALengthOutsideTheCapBeforeReadingIt(t *testing.T) {
	for _, n := range []uint32{0, connectorhost.MaxFrame + 1, 1 << 31} {
		hdr := binary.BigEndian.AppendUint32(nil, n)
		_, _, err := connectorhost.ReadFrame(&failAfter{bytes.NewReader(append(hdr, make([]byte, 64)...)), 4})
		if !errors.Is(err, connectorhost.ErrMalformedOutput) {
			t.Errorf("length %d: err = %v, want ErrMalformedOutput", n, err)
		}
	}
}

func TestFrameReadReportsATruncatedFrame(t *testing.T) {
	for name, in := range map[string][]byte{
		"partial header":  {0, 0},
		"header only":     {0, 0, 0, 3},
		"partial payload": {0, 0, 0, 3, 1, 'a'},
	} {
		if _, _, err := connectorhost.ReadFrame(bytes.NewReader(in)); err != io.ErrUnexpectedEOF {
			t.Errorf("%s: err = %v, want io.ErrUnexpectedEOF", name, err)
		}
	}
}

func TestFailuresKeepTheirSentinelOverTheWire(t *testing.T) {
	for _, want := range sentinels {
		if want == connectorhost.ErrRequestTooLarge || want == connectorhost.ErrRouteRefused || want == connectorhost.ErrResponseTooLarge {
			continue // the Launcher's own: they never cross
		}
		sent := errors.Join(errors.New("connector_call: detail"), want)
		got := connectorhost.FailureOverTheWire(sent)
		assertOnlySentinel(t, got, want)
		if got.Error() != sent.Error() {
			t.Errorf("message = %q, want %q", got, sent)
		}
	}
	if got := connectorhost.FailureOverTheWire(errors.New("compile: bad magic")); errors.Is(got, connectorhost.ErrHandshake) {
		t.Errorf("an error with no sentinel matched %v", got)
	} else {
		assertOnlySentinel(t, got, nil)
	}
	assertOnlySentinel(t, connectorhost.FailureFrameWithCode(250, "?"), connectorhost.ErrMalformedOutput)
}

func TestRemoteHostCloseIsCleanAndIdempotent(t *testing.T) {
	c, err := loadersFor(t)["remote"](context.Background(), fixture(t, "call.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	var out string
	if err := c.Call(context.Background(), "echo", "ok", &out); err != nil || out != "ok" {
		t.Fatalf("echo = %q, %v", out, err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("Close = %v, want a host that exits cleanly once its stdin closes", err)
	}
	if err := c.Close(context.Background()); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}

func TestRemoteNetworkRequestsCrossBackWithoutTheCredential(t *testing.T) {
	var sent bytes.Buffer
	c, b, audit, params := loadNetworkFixtureVia(t, remoteNetLoader(&sent), "network.wasm", func(*connectorhost.Network) {})
	var out fetched
	if err := c.Call(context.Background(), "fetch", params, &out); err != nil {
		t.Fatal(err)
	}
	if out.Status != 200 || out.Body != "scripted /repos/x" {
		t.Errorf("fetched %+v, want the backend's answer", out)
	}
	if got := b.last.Load().Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Errorf("backend saw Authorization %q, want the Launcher's credential", got)
	}
	if lines := auditLines(t, audit); len(lines) != 1 || lines[0].Decision != connectorhost.DecisionSent {
		t.Errorf("audit = %+v, want one sent entry", lines)
	}
	if sent.Len() == 0 || strings.Contains(sent.String(), "s3cret") {
		t.Errorf("the Launcher sent the host %d bytes; a credential must not be among them", sent.Len())
	}
}

func TestRemoteRouteRefusalComesBackAuditedOnce(t *testing.T) {
	c, b, audit, params := loadNetworkFixtureVia(t, remoteNetLoader(nil), "network-host.wasm", func(*connectorhost.Network) {})
	err := c.Call(context.Background(), "fetch", params, new(fetched))
	if !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, connectorhost.ErrRouteRefused) {
		t.Fatalf("err = %v, want ErrCall and ErrRouteRefused", err)
	}
	if b.hits.Load() != 0 {
		t.Errorf("backend saw %d requests, want none", b.hits.Load())
	}
	if lines := auditLines(t, audit); len(lines) != 1 || lines[0].Decision != connectorhost.DecisionRefused {
		t.Errorf("audit = %+v, want one refused entry", lines)
	}
	if err := c.Call(context.Background(), "fetch", params, new(fetched)); !errors.Is(err, connectorhost.ErrUnusable) {
		t.Errorf("second call err = %v, want ErrUnusable", err)
	}
}

func TestRemoteOversizedWriteResponseIsAuditedOnce(t *testing.T) {
	c, _, audit, params := loadNetworkFixtureVia(t, remoteNetLoader(nil), "network-write-oversize.wasm", func(n *connectorhost.Network) {
		n.Routes = append(n.Routes, connectorhost.Route{Method: "PUT", Host: n.Routes[0].Host, Path: "/repos"})
	})
	err := c.Call(context.Background(), "fetch", params, new(fetched))
	if !errors.Is(err, connectorhost.ErrCall) || !errors.Is(err, connectorhost.ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrCall and ErrResponseTooLarge", err)
	}
	if lines := auditLines(t, audit); len(lines) != 1 || lines[0].Decision != connectorhost.DecisionFailed {
		t.Errorf("audit = %+v, want one failed entry", lines)
	}
}

// remoteNetLoader loads a network Connector through the remote engine, tapping
// the Launcher's stream into tap when non-nil.
func remoteNetLoader(tap io.Writer) netLoader {
	return func(ctx context.Context, wasm []byte, n connectorhost.Network) (*connectorhost.Connector, error) {
		return loadRemoteOver(ctx, wasm, &n, tap)
	}
}
