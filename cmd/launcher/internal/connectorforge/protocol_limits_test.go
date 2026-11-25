package connectorforge

import (
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol"
)

// The agreement test for the token-grant bounds; the constants are unexported,
// so the test is internal.
func TestProtocolLimitsAgree(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"MaxScopeLen", connectorprotocol.MaxScopeLen, maxScopeLen},
		{"MaxReasonLen", connectorprotocol.MaxReasonLen, maxReasonLen},
	} {
		if c.got != c.want {
			t.Errorf("connectorprotocol.%s = %d, core's constant is %d", c.name, c.got, c.want)
		}
	}
}
