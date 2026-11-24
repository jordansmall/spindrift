package connectortracker

import (
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol"
	"spindrift.dev/launcher/internal/forge"
)

// Same agreement as connectorhost's TestProtocolLimitsAgree, for the bounds a
// tracker Connector meets; these constants are unexported, so the test is
// internal.
func TestProtocolLimitsAgree(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"LabeledMaxPages", connectorprotocol.LabeledMaxPages, labeledMaxPages},
		{"LabeledMaxBytes", connectorprotocol.LabeledMaxBytes, labeledMaxBytes},
		{"MaxCountIntervalSeconds", connectorprotocol.MaxCountIntervalSeconds, int(maxCountInterval.Seconds())},
		{"ResultPageLimit", connectorprotocol.ResultPageLimit, forge.ResultPageLimit},
	} {
		if c.got != c.want {
			t.Errorf("connectorprotocol.%s = %d, core's constant is %d", c.name, c.got, c.want)
		}
	}
}
