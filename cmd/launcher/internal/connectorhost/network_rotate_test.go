package connectorhost_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/connectorhost"
)

// envBearer wires a Network's credential to an env var, read on every request
// the way a backend row's credential is.
func envBearer(name string) func(*connectorhost.Network) {
	return func(n *connectorhost.Network) {
		n.Authorize = func(r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+os.Getenv(name))
			return nil
		}
	}
}

func TestNetworkFixtureCarriesARotatedTokenOnTheNextRequest(t *testing.T) {
	const env = "NETWORK_ROTATE_TOKEN"
	t.Setenv(env, "token-A")
	c, b, audit, params := loadNetworkFixtureWith(t, "network.wasm", envBearer(env))

	want := []string{"Bearer token-A", "Bearer token-A", "Bearer token-B", "Bearer token-B"}
	for i, w := range want {
		if i == 2 {
			t.Setenv(env, "token-B")
		}
		var out fetched
		if err := c.Call(context.Background(), "fetch", params, &out); err != nil || out.Status != 200 {
			t.Fatalf("call %d = %+v, %v: a rotation dropped a request", i, out, err)
		}
		if got := b.last.Load().Header.Get("Authorization"); got != w {
			t.Errorf("call %d Authorization = %q, want %q", i, got, w)
		}
	}
	if b.hits.Load() != int32(len(want)) {
		t.Errorf("backend saw %d requests, want %d", b.hits.Load(), len(want))
	}
	lines := auditLines(t, audit)
	if len(lines) != len(want) {
		t.Fatalf("audit = %+v, want one entry per call", lines)
	}
	for i, l := range lines {
		if l.Decision != connectorhost.DecisionSent || l.Status != 200 {
			t.Errorf("audit[%d] = %+v, want a sent 200", i, l)
		}
	}
	if strings.Contains(audit.String(), "token-") {
		t.Error("credential in the audit log")
	}
}
