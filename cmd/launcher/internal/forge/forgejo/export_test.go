package forgejo

import (
	"context"
	"net/http"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
)

// SetNow swaps the clock behind the undefined-label verdict's expiry.
func SetNow(t forge.IssueTracker, now func() time.Time) { t.(*forgejoClient).now = now }

// SetLoadRowConnector swaps the Connector load behind a forgejo row and
// returns a restore func.
func SetLoadRowConnector(f func(context.Context, backendrows.Row, backendrows.Options) (*connectorhost.Connector, error)) (restore func()) {
	old := loadRowConnector
	loadRowConnector = f
	return func() { loadRowConnector = old }
}

// SetConnectorClient swaps the transport a forgejo row's Connector sends over
// and returns a restore func.
func SetConnectorClient(c *http.Client) (restore func()) {
	old := connectorClient
	connectorClient = c
	return func() { connectorClient = old }
}
