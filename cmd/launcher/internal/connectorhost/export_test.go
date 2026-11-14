package connectorhost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// GuestEvents exposes the unexported guestEvents to the external tests.
var GuestEvents = guestEvents

// EncodeRequest exposes the unexported encodeRequest to the external tests.
var EncodeRequest = encodeRequest

// The limits, exposed so the external tests probe them without hand-copying.
const (
	MaxOutput       = maxOutput
	MaxRequest      = maxRequest
	MemoryCapBytes  = memoryCapBytes
	MaxTableEntries = maxTableEntries
	MaxFrameSlots   = maxFrameSlots
	MaxModuleLocals = maxModuleLocals
	CallBudget      = callBudget
	InitBudget      = initBudget
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
