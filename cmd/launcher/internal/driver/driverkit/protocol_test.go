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

// TestProtocolProgressAgrees fails when IsProgress answers differently from the
// description's progress kinds, which the SDK's event.IsProgress is rendered from.
func TestProtocolProgressAgrees(t *testing.T) {
	for _, k := range connectorprotocol.EventKinds {
		want := slices.Contains(connectorprotocol.ProgressEventKinds, k)
		if got := IsProgress(Kind(k)); got != want {
			t.Errorf("IsProgress(%q) = %t, protocol says %t", k, got, want)
		}
	}
}
