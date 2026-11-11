// In-package, unlike the export_test.go bridge the other tests use: it needs
// the unexported handshake, response and wirePattern decoders and types.

package connectorhost

import (
	"errors"
	"testing"
)

// fakeMemory stands in for guest memory: what the host reads back is whatever
// bytes the fuzzer picked, without a wasm instantiation per input.
type fakeMemory []byte

func (m fakeMemory) Read(offset, byteCount uint32) ([]byte, bool) {
	if uint64(offset)+uint64(byteCount) > uint64(len(m)) {
		return nil, false
	}
	return m[offset : offset+byteCount : offset+byteCount], true
}

// allowedGuestErr reports whether err is nil or one of the sentinels a
// guest-derived failure is allowed to carry.
func allowedGuestErr(err error) bool {
	return err == nil || errors.Is(err, ErrMalformedOutput) || errors.Is(err, ErrOutputTooLarge)
}

// FuzzHandshake runs what the host reads back from connector_handshake. The
// seeds in testdata/fuzz/FuzzHandshake are the fixture Connectors' real output
// plus hand-picked edge cases. Go's fuzz minimizer can stall a worker at 0
// execs/sec on these targets: pass -fuzzminimizetime=0 for a long session (a
// hung target then trips Go's per-input hang detector instead), and lower
// -parallel if the default worker count dies with "fuzzing process hung or
// terminated unexpectedly".
func FuzzHandshake(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		raw, err := readOutput(fakeMemory(data), "connector_handshake", uint64(len(data)), limitsFor(0).output)
		if err == nil {
			var h *handshake
			if h, err = decodeHandshake(raw); err == nil {
				if h.ProtocolVersion == "" || h.Capabilities == nil {
					t.Fatalf("accepted handshake %q lacks a required field", raw)
				}
				if perr := CheckProtocol(h.ProtocolVersion); perr != nil && !errors.Is(perr, ErrProtocolUnsupported) {
					t.Fatalf("CheckProtocol(%q) failed without ErrProtocolUnsupported: %v", h.ProtocolVersion, perr)
				}
			}
		}
		if !allowedGuestErr(err) {
			t.Fatalf("unnamed failure for %q: %v", data, err)
		}
	})
}

// FuzzCall runs what the host reads back from connector_call: the output
// buffer, the {result, error} envelope, and the result shapes the Driver
// Strategy decodes it into. The seeds in testdata/fuzz/FuzzCall are the
// fixture Connectors' real responses plus hand-picked edge cases.
func FuzzCall(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte, ptr, n uint32) {
		raw, err := readOutput(fakeMemory(data), "connector_call", uint64(ptr)<<32|uint64(n), limitsFor(0).output)
		var resp *response
		if err == nil {
			resp, err = decodeResponse(raw)
		}
		if !allowedGuestErr(err) {
			t.Fatalf("unnamed failure for data=%q ptr=%d n=%d: %v", data, ptr, n, err)
		}
		if err == nil {
			checkDriverShapes(t, resp)
		}
	})
}

// checkDriverShapes decodes a successful envelope into each result shape the
// Driver Strategy uses, independently: a result that is not an object (an
// events array) must still reach guestEvents.
func checkDriverShapes(t *testing.T, resp *response) {
	t.Helper()
	var describe describeResult
	describeErr := decodeResult(resp, &describe)
	if !allowedGuestErr(describeErr) {
		t.Fatalf("unnamed failure decoding describeResult from %q: %v", resp.Result, describeErr)
	}
	for _, into := range []any{
		&resultEventResult{}, &sessionFlagsResult{}, &resolveExitResult{},
		&newDecoderResult{}, &toolPhaseResult{}, &toolKindResult{}, &modelFamilyResult{}, nil,
	} {
		if err := decodeResult(resp, into); !allowedGuestErr(err) {
			t.Fatalf("unnamed failure decoding %T from %q: %v", into, resp.Result, err)
		}
	}
	if resp.Error != nil {
		return
	}
	evs, ok := guestEvents(resp.Result)
	if !ok && evs != nil {
		t.Fatalf("guestEvents(%q) returned events alongside a refusal", resp.Result)
	}
	if describeErr != nil {
		return
	}
	// An unknown reason is a refusal (ErrCall), never a panic.
	for _, ws := range [][]wirePattern{describe.TransientPatterns, describe.TerminalPatterns} {
		if _, err := patterns(ws); err != nil && !errors.Is(err, ErrCall) {
			t.Fatalf("patterns(%v) failed without ErrCall: %v", ws, err)
		}
	}
}
