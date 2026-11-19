package jira

import "net/http"

// ConnectorClientForTest points the host-side Connector loads at c, as a real
// host reaches the real site through the default client. It is for tests of
// another package that drive the Strategy against an httptest backend; the
// returned func restores the default. Both ends also close the process's
// audit file and let the next load reopen it, so a test that changes directory
// gets its own and no handle outlives the swap.
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
