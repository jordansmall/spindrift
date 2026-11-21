package connectorhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// GuestEvents exposes the unexported guestEvents to the external tests.
var GuestEvents = guestEvents

// EncodeRequest exposes the unexported encodeRequest to the external tests.
var EncodeRequest = encodeRequest

// The limits, exposed so the external tests probe them without hand-copying.
const (
	MaxOutput       = maxOutput
	MaxRequest      = maxRequest
	MaxHeaderName   = maxHeaderName
	MemoryCapBytes  = memoryCapBytes
	MaxTableEntries = maxTableEntries
	MaxFrameSlots   = maxFrameSlots
	MaxModuleLocals = maxModuleLocals
	CallBudget      = callBudget
	InitBudget      = initBudget

	MaxHTTPRequests   = maxHTTPRequests
	MaxResponseCredit = maxResponseCredit
)

// The per-request-byte allowances, exposed with LimitsFor.
const (
	BudgetPerMiB         = budgetPerMiB
	MemoryPerRequestByte = memoryPerRequestByte
	OutputPerRequestByte = outputPerRequestByte
)

// LimitsFor exposes limitsFor's three ceilings to the external tests.
func LimitsFor(requestBytes int) (budget time.Duration, memory, output uint64) {
	l := limitsFor(requestBytes)
	return l.budget, l.memory, l.output
}

// LogWindow exposes the unexported logWindow to the external tests.
var LogWindow = logWindow

// Do exposes the unexported do to the external tests.
func (n *Network) Do(ctx context.Context, raw []byte, maxBody int) (int, []byte, error) {
	status, body, _, err := n.do(ctx, raw, maxBody)
	return status, body, err
}

// DoHeader is Do that also returns the response headers the host would hold.
func (n *Network) DoHeader(ctx context.Context, raw []byte, maxBody int) (int, []byte, http.Header, error) {
	return n.do(ctx, raw, maxBody)
}

// MaxHTTPResponse is the response cap, exposed so the tests probe it.
const MaxHTTPResponse = maxHTTPResponse

// RecordFailure exposes the unexported recordFailure to the external tests.
func (n *Network) RecordFailure(d Decision, err error) { n.recordFailure(d, err) }

// SetStreamPiece shrinks the stream filter's write piece for the test and
// returns the restore.
func SetStreamPiece(n int) (restore func()) {
	old := streamPiece
	streamPiece = n
	return func() { streamPiece = old }
}

// ReportedError decodes a raw connector_call response body the way Call does
// and returns the Connector-reported error, or nil when the body holds none. A
// body that does not decode is an error too, so a test expecting "no error"
// cannot pass on malformed input.
func ReportedError(body string) error {
	var r response
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return fmt.Errorf("undecodable response body: %w", err)
	}
	if r.Error == nil {
		return nil
	}
	return r.callError()
}

// NetworkConnector wraps n in a Connector with no guest module, so the host
// helpers that read only the Network are testable without a wasm fixture.
func NetworkConnector(n *Network) *Connector { return &Connector{network: n} }

// HostCalls exposes a Network's Launcher-side Host call handler to the external
// tests.
type HostCalls struct{ h *networkCalls }

// NewHostCalls returns the handler a network Connector over n would serve its
// Host calls with.
func NewHostCalls(n *Network) *HostCalls { return &HostCalls{&networkCalls{n: n}} }

// HTTPRequest exposes the handler's httpRequest.
func (t *HostCalls) HTTPRequest(ctx context.Context, raw []byte, bufCap int) (int, []byte, http.Header, error) {
	return t.h.httpRequest(ctx, raw, bufCap)
}

// Refuse exposes the handler's refuse.
func (t *HostCalls) Refuse(call, why string) error { return t.h.refuse(call, why) }

// Failed reports whether the handler has audited a fatal failure.
func (t *HostCalls) Failed() bool { return t.h.failed }

// The wire codec, exposed so the external tests probe the frame format.
const MaxFrame = maxFrame

// Frame kinds exposed to the codec tests.
const (
	KindCall   = kindCall
	KindResult = kindResult
)

// WriteFrame and ReadFrame expose the frame codec.
var (
	WriteFrame = writeFrame
	ReadFrame  = readFrame
)

// FailureOverTheWire returns err as the Launcher rebuilds it after the host sent
// it as a failure frame.
func FailureOverTheWire(err error) error {
	var buf bytes.Buffer
	if werr := writeFrame(&buf, kindFailure, failureParts(err)...); werr != nil {
		return werr
	}
	_, payload, rerr := readFrame(&buf)
	if rerr != nil {
		return rerr
	}
	return decodeFailure(payload)
}

// FailureFrameWithCode returns the error the Launcher rebuilds from a failure
// frame carrying code and msg.
func FailureFrameWithCode(code byte, msg string) error {
	return decodeFailure(append([]byte{code}, msg...))
}

// LoadRemoteOver is LoadRemote or, for a non-nil n, LoadNetworkRemote over a
// host the caller runs: r and w are the Launcher's ends of its stream, kill
// stops the host and wait returns once it has exited.
func LoadRemoteOver(ctx context.Context, wasm []byte, n *Network, r io.Reader, w io.WriteCloser, kill func(), wait func() error) (*Connector, error) {
	l := hostLink{r: r, w: w, kill: kill, wait: wait}
	mk := func(ctx context.Context, wasm []byte, host hostCalls) (engine, error) {
		return newRemoteEngine(ctx, wasm, host, l)
	}
	if n == nil {
		return loadWith(ctx, wasm, mk, nil)
	}
	if err := checkGrant(*n); err != nil {
		return nil, err
	}
	return loadWith(ctx, wasm, mk, n)
}

// WriteChunk and ReadChunk expose the session-layer codec.
var (
	WriteChunk = writeChunk
	ReadChunk  = readChunk
)

// SetSessionBufCap shrinks the per-session buffer bound so a flood test needs
// no 64 MiB of traffic.
func SetSessionBufCap(n int) (restore func()) {
	old := sessionBufCap
	sessionBufCap = n
	return func() { sessionBufCap = old }
}

// SetBatchCap shrinks the decodeBatch request cap for the test and returns the
// restore.
func SetBatchCap(n int) (restore func()) {
	old := batchCap
	batchCap = n
	return func() { batchCap = old }
}

// SetMemoryCap sets the in-process engine's base memory cap and returns the
// restore. A remote engine's child keeps its own cap, which applyLimits only
// raises.
func SetMemoryCap(n uint64) (restore func()) {
	old := memoryCap
	memoryCap, initLimits.memory = n, n
	return func() { memoryCap, initLimits.memory = old, old }
}

// ShortCallBudget is the budget tests that exhaust it load under, via
// SetCallBudget, instead of waiting out CallBudget.
const ShortCallBudget = 500 * time.Millisecond

// SetCallBudget sets the base call budget every engine's limitsFor starts from
// and returns the restore. The process and remote engines enforce the budget on
// the launcher side, so it bounds them too. A process engine's child keeps its
// own 5 s budget; only the launcher-side budget is lowered.
func SetCallBudget(d time.Duration) (restore func()) {
	old := callBudgetBase
	callBudgetBase = d
	return func() { callBudgetBase = old }
}

// SetGuestEnv sets the environment of Connectors the in-process engine loads
// after it and returns the restore; a remote engine's child keeps its own.
func SetGuestEnv(env map[string]string) (restore func()) {
	old := guestEnv
	guestEnv = env
	return func() { guestEnv = old }
}

// SetRequestTimeout sets the Network's per-http_request timeout, which is
// unexported so only these tests shorten it.
func (n *Network) SetRequestTimeout(d time.Duration) { n.requestTimeout = d }

// OpenSessions is how many sessions the Sessions still holds open.
func (s *Sessions) OpenSessions() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.open)
}

// RawBatchDecoderAnswering is the raw batch decoder with every request
// answered by answer instead of the guest, so a test can feed it a malformed one.
func (c *Connector) RawBatchDecoderAnswering(answer string) driverkit.BatchDecoder {
	cd := c.rawCodec()
	cd.batch = func(_ context.Context, _ uint32, _ []string, result *json.RawMessage) error {
		*result = json.RawMessage(answer)
		return nil
	}
	return c.newBatchDecoder(cd)
}

// SetClock replaces the wall clock the Connector hands a guest decoder.
func (c *Connector) SetClock(now func() time.Time) { c.now = now }
