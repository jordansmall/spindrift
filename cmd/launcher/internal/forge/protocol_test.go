package forge_test

import (
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol"
	"spindrift.dev/launcher/internal/forge"
)

// TestProtocolStatusCategoryAgrees fails when StatusCategoryDone drifts from the
// Connector protocol description, which the SDK's tracker.StatusCategoryDone is
// rendered from.
func TestProtocolStatusCategoryAgrees(t *testing.T) {
	if forge.StatusCategoryDone != connectorprotocol.StatusCategoryDone {
		t.Errorf("StatusCategoryDone = %q, protocol says %q", forge.StatusCategoryDone, connectorprotocol.StatusCategoryDone)
	}
}
