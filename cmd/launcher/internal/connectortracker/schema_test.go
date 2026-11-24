package connectortracker

import (
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol/schematest"
)

// requireConforms is the same check over a writeGuest. Only the replies it
// returns as JSON are validated; its bare nil answers (editLabels, comment)
// carry no result to check.
func (g *writeGuest) requireConforms(t *testing.T) {
	t.Helper()
	replies := map[string]string{}
	for method, reply := range map[string]string{"tracker.issue": g.issue, "tracker.fileIssue": g.fileURL} {
		if reply != "" {
			replies[method] = reply
		}
	}
	schematest.RequireConforms(t, g.calls, replies)
}
