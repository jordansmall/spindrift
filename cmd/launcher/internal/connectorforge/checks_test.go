package connectorforge

import (
	"testing"

	"spindrift.dev/launcher/internal/connectorcall"
	"spindrift.dev/launcher/internal/connectorprotocol"
	"spindrift.dev/launcher/internal/retry"
)

// The forge axis's check bodies are registered by the names the method table
// gives them: a method without a body, or a body no method names, fails here.
func TestForgeChecksAgreeWithTheMethodTable(t *testing.T) {
	t.Parallel()
	p := pushOnlyOver(auditStub{}, nil, retry.Clock{}, testTarget)
	if err := connectorcall.Unregistered(connectorprotocol.AxisForge, forgeChecks(p)); err != nil {
		t.Fatal(err)
	}
}
