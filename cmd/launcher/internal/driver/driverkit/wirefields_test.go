package driverkit

import (
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol/wiretest"
)

// TestWireShapesAgree holds Event and Tokens to the shapes the protocol
// description gives them, which the SDK's event package renders its copies from.
// HostOp is core's own payload; a guest never emits it, so the description
// leaves it out.
func TestWireShapesAgree(t *testing.T) {
	wiretest.Agree(t, "event", Event{}, "HostOp")
	wiretest.Agree(t, "tokens", Tokens{})
}
