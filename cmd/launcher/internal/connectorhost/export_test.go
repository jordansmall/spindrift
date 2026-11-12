package connectorhost

import (
	"context"
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
	return n.do(ctx, raw, maxBody)
}

// MaxHTTPResponse is the response cap, exposed so the tests probe it.
const MaxHTTPResponse = maxHTTPResponse
