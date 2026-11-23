package jira

import (
	"net/http"

	"spindrift.dev/launcher/internal/retry"
)

// ConnectorClientForTest points the host-side Connector loads at c, for tests
// in another package that drive the Strategy against an httptest backend; the
// returned func restores the default. It also swaps the tracker's retry clock
// for one that never sleeps, so a scripted persistent 5xx fails fast.
func ConnectorClientForTest(c *http.Client) (restore func()) {
	old, oldClock := connectorClient, connectorClock
	connectorClient = c
	connectorClock = retry.NoSleepClock()
	return func() { connectorClient, connectorClock = old, oldClock }
}
