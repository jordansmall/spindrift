// In-package, unlike the export_test.go bridge the other tests use: it needs
// the unexported handshake, response and wirePattern decoders and types.

package connectorhost

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
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
		&newDecoderResult{}, &newStreamFilterResult{}, &streamDataResult{}, &toolPhaseResult{}, &toolKindResult{}, &modelFamilyResult{}, nil,
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

// FuzzStreamFilterResult runs what the host reads back from driver.streamWrite
// and driver.streamFlush: the {result, error} envelope and the base64 data it
// decodes into the bytes the filter writes to the log. A guest cannot make it
// panic or return bytes other than the decoding of its own data string.
func FuzzStreamFilterResult(f *testing.F) {
	f.Add([]byte(`{"result":{"data":""}}`))
	f.Add([]byte(`{"result":{}}`))
	f.Add([]byte(`{"result":{"data":null}}`))
	f.Add([]byte(`{"result":null}`))
	f.Add([]byte(`{"result":{"data":"/wD+gA=="}}`))
	f.Add([]byte(`{"result":{"data":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("line\xff\n"), 4096)) + `"}}`))
	f.Add([]byte(`{"result":{"data":"not base64!"}}`))
	f.Add([]byte(`{"result":{"data":5}}`))
	f.Add([]byte(`{"result":{"data":[104,105]}}`))
	f.Add([]byte(`{"result":"data"}`))
	f.Add([]byte(`{"error":"stream filter lost"}`))
	f.Add([]byte(`{"error":"x","result":{"data":"aGk="}}`))
	f.Add([]byte(`{"result":{"filter":7}}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		resp, err := decodeResponse(raw)
		if err != nil {
			if !errors.Is(err, ErrMalformedOutput) {
				t.Fatalf("decodeResponse(%q) failed without ErrMalformedOutput: %v", raw, err)
			}
			return
		}
		var filter newStreamFilterResult
		if err := decodeResult(resp, &filter); !allowedGuestErr(err) {
			t.Fatalf("unnamed failure decoding newStreamFilterResult from %q: %v", resp.Result, err)
		}
		var got streamDataResult
		if err := decodeResult(resp, &got); err != nil {
			if !errors.Is(err, ErrMalformedOutput) {
				t.Fatalf("decoding streamDataResult from %q failed without ErrMalformedOutput: %v", resp.Result, err)
			}
			return
		}
		// decodeResult ignores the result beside a Connector error, and so
		// does the host: nothing reaches the log, so there is no data to check.
		if resp.Error != nil {
			return
		}
		// Independent oracle: a string data field must decode to exactly the
		// bytes the host would write to the log, and an absent or null one to none.
		var probe struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(resp.Result, &probe) != nil {
			return
		}
		switch d := bytes.TrimSpace(probe.Data); {
		case len(d) == 0 || string(d) == "null":
			if len(got.Data) != 0 {
				t.Fatalf("absent data produced %q", got.Data)
			}
		case d[0] == '"':
			var s string
			if err := json.Unmarshal(d, &s); err != nil {
				t.Fatalf("decoded result but data string %q does not unmarshal: %v", d, err)
			}
			want, err := base64.StdEncoding.DecodeString(s)
			if err != nil {
				t.Fatalf("accepted non-base64 data %q", s)
			}
			if !bytes.Equal(got.Data, want) {
				t.Fatalf("data %q decoded to %q, want %q", s, got.Data, want)
			}
		}
	})
}
