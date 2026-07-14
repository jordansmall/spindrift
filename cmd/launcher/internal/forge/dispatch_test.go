package forge

import "testing"

// TestDispatchLabels_Untriaged_HasNoLabel verifies Untriaged maps to the
// empty label string, so a TransitionState(Untriaged, X) promotion call
// never asks an adapter to remove a label the issue never had (#646).
func TestDispatchLabels_Untriaged_HasNoLabel(t *testing.T) {
	d := DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
	}
	if got := d.Label(Untriaged); got != "" {
		t.Fatalf("Label(Untriaged): got %q, want empty", got)
	}
}
