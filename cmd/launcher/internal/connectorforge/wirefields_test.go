package connectorforge

import (
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol/wiretest"
)

// TestWireShapesAgree holds core's forge wire structs to the shapes the
// protocol description gives them, which the SDK's twins are rendered from.
func TestWireShapesAgree(t *testing.T) {
	for key, v := range map[string]any{
		"target": Target{},
		"pr":     wirePR{},
		"check":  wireCheck{},
		"rollup": wireRollup{},
		"grant":  wireGrant{},
	} {
		wiretest.Agree(t, key, v)
	}
}
