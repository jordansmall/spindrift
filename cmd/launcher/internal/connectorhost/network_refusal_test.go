package connectorhost_test

import (
	"bytes"
	"errors"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

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
