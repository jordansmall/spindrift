package connectorhost_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestDeclaredHostsDedupesAndKeepsThePort(t *testing.T) {
	n := &connectorhost.Network{Name: "demo", Routes: []connectorhost.Route{
		{Method: "GET", Host: "api.example.com", Path: "/a"},
		{Method: "POST", Host: "api.example.com", Path: "/a"},
		{Method: "GET", Host: "git.example.com:8443", Path: "/b"},
		{Method: "GET", Host: "API.example.com", Path: "/c"},
	}}
	got := connectorhost.NetworkConnector(n).DeclaredHosts()
	want := []string{"api.example.com", "git.example.com:8443"}
	if !slices.Equal(got, want) {
		t.Fatalf("DeclaredHosts = %v, want %v", got, want)
	}
}

func TestDeclaredHostsNilWithoutANetworkGrant(t *testing.T) {
	if got := (&connectorhost.Connector{}).DeclaredHosts(); got != nil {
		t.Fatalf("DeclaredHosts = %v, want nil", got)
	}
}

func TestAuditRefusalRecordsOneRefusedEntryAndReturnsTheRefusal(t *testing.T) {
	var audit bytes.Buffer
	n := &connectorhost.Network{Name: "demo", Audit: connectorhost.NewAuditLog(&audit)}
	refusal := errors.New("remote url embeds credentials")
	err := connectorhost.NetworkConnector(n).AuditRefusal("remote", refusal)
	if !errors.Is(err, refusal) || errors.Is(err, connectorhost.ErrAudit) {
		t.Fatalf("err = %v, want the refusal alone", err)
	}
	lines := auditLines(t, &audit)
	if len(lines) != 1 {
		t.Fatalf("audit lines = %d, want 1", len(lines))
	}
	if e := lines[0]; e.Connector != "demo" || e.Decision != connectorhost.DecisionRefused || e.Reason != "remote: "+refusal.Error() {
		t.Fatalf("entry = %+v", e)
	}
}

func TestAuditRefusalFailsClosedWhenTheAuditWriteFails(t *testing.T) {
	n := &connectorhost.Network{Name: "demo", Audit: connectorhost.NewAuditLog(failWriter{})}
	refusal := errors.New("host not declared")
	err := connectorhost.NetworkConnector(n).AuditRefusal("remote", refusal)
	if !errors.Is(err, refusal) || !errors.Is(err, connectorhost.ErrAudit) {
		t.Fatalf("err = %v, want refusal and ErrAudit", err)
	}
}

func TestAuditRefusalWithoutANetworkGrantIsUnaudited(t *testing.T) {
	refusal := errors.New("host not declared")
	err := (&connectorhost.Connector{}).AuditRefusal("remote", refusal)
	if !errors.Is(err, refusal) || !errors.Is(err, connectorhost.ErrAudit) {
		t.Fatalf("err = %v, want refusal and ErrAudit", err)
	}
}
