// In-package, like fuzz_test.go: it drives Serve and the remote engine's
// unexported decode paths directly over fixed byte streams, without a process
// or a wasm instantiation per input.

package connectorhost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

// FuzzServe feeds arbitrary bytes to the connector host as the Launcher's
// stream: a garbage frame, a lying length, a frame out of turn and a load frame
// with garbage module bytes must each end the serve, never panic or hang, and
// everything the host wrote back must be whole frames of host-to-Launcher kinds
// with payloads the Launcher can decode. The seeds in testdata/fuzz/FuzzServe
// are real Launcher streams plus the malformed ones above. See FuzzHandshake for
// the -fuzzminimizetime=0 advice on long sessions.
func FuzzServe(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		var out bytes.Buffer
		_ = Serve(context.Background(), bytes.NewReader(data), &out)
		for r := bytes.NewReader(out.Bytes()); ; {
			kind, p, err := readFrame(r)
			if err == io.EOF {
				return
			}
			if err != nil {
				t.Fatalf("Serve wrote a malformed frame for %q: %v", data, err)
			}
			switch kind {
			case kindResult:
			case kindFailure:
				if len(p) < 1 || int(p[0]) >= len(failureSentinels) {
					t.Fatalf("failure frame %q carries no known code, for %q", p, data)
				}
			case kindHTTPRequest:
				_, _, err = decodeHTTPRequest(p)
			case kindRefuse:
				_, _, err = decodeRefuse(p)
			default:
				t.Fatalf("Serve wrote a frame of Launcher-bound kind %d for %q", kind, data)
			}
			if err != nil {
				t.Fatalf("Serve wrote an undecodable frame %d for %q: %v", kind, data, err)
			}
		}
	})
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// FuzzRemoteReply feeds arbitrary bytes to the Launcher as a hostile host's
// stream, answering a load, a handshake or a call (op mod 3) of a remote
// engine whose Host-call grant has no routes, so no request it serves can be
// sent. The seeds in testdata/fuzz/FuzzRemoteReply are real host streams plus
// failures per code, Host-call frames, truncated frames and an over-cap length.
func FuzzRemoteReply(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte, op uint8) {
		var audit bytes.Buffer
		e := &remoteEngine{
			link: hostLink{
				r: bytes.NewReader(data), w: nopWriteCloser{io.Discard},
				kill: func() {}, wait: func() error { return nil },
			},
			host: &networkCalls{n: &Network{Name: "fuzz", Audit: NewAuditLog(&audit)}},
		}
		ctx := context.Background()
		var out []byte
		var err error
		req := []byte(`{"method":"m"}`)
		switch op % 3 {
		case 0:
			// A load failure is the host's own, which loadWith wraps in ErrHandshake.
			if err = e.load(ctx, nil); err != nil && errors.As(err, new(*remoteError)) {
				return
			}
		case 1:
			out, err = e.handshake(ctx)
		default:
			out, err = e.call(ctx, req)
		}
		if err == nil {
			// With no routes, every Host call the engine served or was told of
			// failed closed and was audited; a success after one is a host that
			// talked its way past the refusal.
			if audit.Len() != 0 {
				t.Fatalf("accepted a success after a failed Host call for op=%d data=%q: %q", op, data, audit.String())
			}
			if op%3 == 0 {
				return
			}
			lim := limitsFor(0)
			if op%3 == 2 {
				lim = limitsFor(len(req))
			}
			if lerr := checkOutputLen("fuzz", uint64(len(out)), lim.output); lerr != nil {
				t.Fatalf("accepted %d bytes over the output cap for %q", len(out), data)
			}
			if jerr := checkOutputJSON("fuzz", out); jerr != nil {
				t.Fatalf("accepted a result that is not UTF-8 JSON for %q: %q", data, out)
			}
			return
		}
		// The host's failure sentinels, plus ErrRouteRefused, which only the
		// Launcher's own Host-call handler returns.
		for _, s := range append([]error{ErrRouteRefused}, failureSentinels...) {
			if s != nil && s != errRemoteHostCall && errors.Is(err, s) {
				return
			}
		}
		t.Fatalf("unnamed failure for op=%d data=%q: %v", op, data, err)
	})
}
