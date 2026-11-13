package github

import (
	"net/http"
)

// ConnectorClientForTest points the host-side Connector loads at c, as a real
// host reaches the real API host through the default client, and closes the
// shared Connectors so none keeps the previous transport. It is for tests of
// another package that drive the Strategy against an httptest backend; the
// returned func restores the default.
func ConnectorClientForTest(c *http.Client) (restore func()) {
	closeSharedConnectors()
	old := connectorClient
	connectorClient = c
	return func() {
		closeSharedConnectors()
		connectorClient = old
	}
}
