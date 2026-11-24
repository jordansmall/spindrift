package dispatchkind

import (
	"maps"
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol"
)

// TestProtocolRolesAgree fails when the role values or the pass-role table
// drift from the Connector protocol description, which the SDK's
// driver.HostLine is rendered from.
func TestProtocolRolesAgree(t *testing.T) {
	if ImplementorRole != connectorprotocol.RoleImplementor {
		t.Errorf("ImplementorRole = %q, protocol says %q", ImplementorRole, connectorprotocol.RoleImplementor)
	}
	if ReviewerRole != connectorprotocol.RoleReviewer {
		t.Errorf("ReviewerRole = %q, protocol says %q", ReviewerRole, connectorprotocol.RoleReviewer)
	}
	if !maps.Equal(passRoles(), connectorprotocol.PassRoles) {
		t.Errorf("passRoles() = %v, protocol says %v", passRoles(), connectorprotocol.PassRoles)
	}
	for _, d := range All {
		if !maps.Equal(d.PassRoles, connectorprotocol.PassRoles) {
			t.Errorf("%s: PassRoles = %v, protocol says %v", d.Name, d.PassRoles, connectorprotocol.PassRoles)
		}
	}
}
