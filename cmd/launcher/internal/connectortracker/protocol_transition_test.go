package connectortracker

import (
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/connectorprotocol"
)

// TestProtocolTransitionOutcomesAgree fails when the outcomes core accepts of
// tracker.transition drift from the Connector protocol description.
func TestProtocolTransitionOutcomesAgree(t *testing.T) {
	got := []string{transitionMoved, transitionUnavailable, transitionUnmapped}
	if !slices.Equal(got, connectorprotocol.TransitionOutcomes) {
		t.Errorf("transition outcomes = %v, protocol says %v", got, connectorprotocol.TransitionOutcomes)
	}
}
