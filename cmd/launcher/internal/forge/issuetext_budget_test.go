package forge

import (
	"testing"

	"spindrift.dev/launcher/internal/signalwire"
)

// signalwire imports only the standard library, so it restates the budget
// instead of importing it (ADR 0052); this pins the two copies together.
func TestIssueTextBudgetMatchesTheSignalReplyBudget(t *testing.T) {
	if maxIssueTextBytes != signalwire.MaxIssueReplyBytes {
		t.Fatalf("maxIssueTextBytes = %d, signalwire.MaxIssueReplyBytes = %d; the ADR says they are one budget", maxIssueTextBytes, signalwire.MaxIssueReplyBytes)
	}
}
