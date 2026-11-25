package forgejo

import "net/http"

// SetConnectorClient swaps the transport a forgejo row's Connector sends over
// and returns a restore func.
func SetConnectorClient(c *http.Client) (restore func()) {
	old := connectorClient
	connectorClient = c
	return func() { connectorClient = old }
}
