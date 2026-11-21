package jira

import "net/http"

// ConnectorClientForTest points the host-side Connector loads at c, for tests
// in another package that drive the Strategy against an httptest backend; the
// returned func restores the default. Both ends also reset the process's audit
// file, so a test that changes directory gets its own and no handle outlives
// the swap.
func ConnectorClientForTest(c *http.Client) (restore func()) {
	old := connectorClient
	connectorClient = c
	resetPrimaryAudit()
	return func() { connectorClient = old; resetPrimaryAudit() }
}

// resetPrimaryAudit swaps primaryAudit (named for the primaryrecord file family).
func resetPrimaryAudit() {
	primaryAudit.release()
	primaryAudit = &auditFile{}
}
