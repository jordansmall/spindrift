package github

import (
	"net/http"
)

// ConnectorClientForTest points the host-side Connector loads at c, as a real
// host reaches the real API host through the default client. A runtime keeps
// the client its Connector first loaded with, so the caller builds a fresh one
// after this. It is for tests of another package that drive the Strategy
// against an httptest backend; the returned func restores the default.
func ConnectorClientForTest(c *http.Client) (restore func()) {
	old := connectorClient
	connectorClient = c
	return func() { connectorClient = old }
}
