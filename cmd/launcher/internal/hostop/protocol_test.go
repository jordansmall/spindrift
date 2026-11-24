package hostop_test

import (
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol"
	"spindrift.dev/launcher/internal/hostop"
)

// TestProtocolHostLineAgrees fails when the host-line tokens, or the payload's
// member key, drift from the Connector protocol description, which the SDK's
// driver.HostLine is rendered from.
func TestProtocolHostLineAgrees(t *testing.T) {
	f, ok := reflect.TypeOf(hostop.Event{}).FieldByName("SpindriftOp")
	if !ok {
		t.Fatal("hostop.Event has no SpindriftOp field")
	}
	if key, _, _ := strings.Cut(f.Tag.Get("json"), ","); key != connectorprotocol.HostLineType {
		t.Errorf("Event.SpindriftOp json key = %q, protocol says %q", key, connectorprotocol.HostLineType)
	}
	if hostop.TypeSpindriftOp != connectorprotocol.HostLineType {
		t.Errorf("TypeSpindriftOp = %q, protocol says %q", hostop.TypeSpindriftOp, connectorprotocol.HostLineType)
	}
	if hostop.OpPassStart != connectorprotocol.HostLineOpPassStart {
		t.Errorf("OpPassStart = %q, protocol says %q", hostop.OpPassStart, connectorprotocol.HostLineOpPassStart)
	}
}
