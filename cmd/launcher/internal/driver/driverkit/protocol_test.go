package driverkit

import (
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol"
)

// TestProtocolReasonsAgree fails when AllReasons drifts from the Connector
// protocol description, which the SDK's driver.Reason is rendered from.
func TestProtocolReasonsAgree(t *testing.T) {
	var got []string
	for _, r := range AllReasons {
		got = append(got, string(r))
	}
	if !slices.Equal(got, connectorprotocol.Reasons) {
		t.Errorf("AllReasons = %v, protocol says %v", got, connectorprotocol.Reasons)
	}
}

// TestProtocolRolesAgree fails when the default, implementor and reviewer roles
// drift from the Connector protocol description, which the SDK's driver roles
// are rendered from.
func TestProtocolRolesAgree(t *testing.T) {
	if DefaultRole != connectorprotocol.RoleSubagent {
		t.Errorf("DefaultRole = %q, protocol says %q", DefaultRole, connectorprotocol.RoleSubagent)
	}
	if ImplementorRole != connectorprotocol.RoleImplementor || ReviewerRole != connectorprotocol.RoleReviewer {
		t.Errorf("roles %q, %q, protocol says %q, %q", ImplementorRole, ReviewerRole,
			connectorprotocol.RoleImplementor, connectorprotocol.RoleReviewer)
	}
}
