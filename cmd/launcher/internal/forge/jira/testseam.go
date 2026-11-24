package jira

import "net/http"

// ConnectorClientForTest points the host-side Connector loads at c, for tests
// in another package that drive the Strategy against an httptest backend; the
// returned func restores the default.
func ConnectorClientForTest(c *http.Client) (restore func()) {
	old := connectorClient
	connectorClient = c
	return func() { connectorClient = old }
}
