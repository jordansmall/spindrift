package github

import (
	"net/http"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
)

// ConnectorClientForTest points the host-side Connector loads at c, as a real
// host reaches the real API host through the default client. A runtime keeps
// the client its Connector first loaded with, so the caller builds a fresh one
// after this. It is for tests of another package that drive the Strategy
// against an httptest backend, which listens on loopback; the returned func
// restores the default.
func ConnectorClientForTest(c *http.Client) (restore func()) {
	old := connectorOptions
	connectorOptions = backendrows.Options{Client: c, PublicForTest: connectorhost.LoopbackForTest}
	return func() { connectorOptions = old }
}
