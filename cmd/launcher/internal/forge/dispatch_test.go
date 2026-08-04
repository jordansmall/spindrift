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

// TestDispatchLabels_ClaimRemoveLabels_ClaimStripsStaleTerminals verifies a
// claim (to == InProgress) removes the from-state label plus both terminal
// labels and the fixed SpecMismatchLabel, deduplicated — the single source
// of truth github's execClient and forge.Fake both call, so the two can't
// drift apart (#1985, #2275).
func TestDispatchLabels_ClaimRemoveLabels_ClaimStripsStaleTerminals(t *testing.T) {
	d := DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
	}
	got := d.ClaimRemoveLabels(Dispatchable, InProgress)
	want := []string{"ready-for-agent", "agent-complete", "agent-failed", SpecMismatchLabel}
	if len(got) != len(want) {
		t.Fatalf("ClaimRemoveLabels = %v, want %v", got, want)
	}
	for i, l := range want {
		if got[i] != l {
			t.Errorf("ClaimRemoveLabels[%d] = %q, want %q", i, got[i], l)
		}
	}
}

// TestDispatchLabels_ClaimRemoveLabels_ClaimStripsStaleSpecMismatch verifies
// a claim (to == InProgress) includes the fixed, non-configurable
// SpecMismatchLabel in its removal set, alongside Complete/Failed -- so a
// human re-triggering an issue halted by the #2275 SPEC CHECK gate lands
// cleanly on agent-in-progress without a stale agent-spec-mismatch label
// still attached.
func TestDispatchLabels_ClaimRemoveLabels_ClaimStripsStaleSpecMismatch(t *testing.T) {
	d := DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
	}
	got := d.ClaimRemoveLabels(Dispatchable, InProgress)
	found := false
	for _, l := range got {
		if l == SpecMismatchLabel {
			found = true
		}
	}
	if !found {
		t.Errorf("ClaimRemoveLabels(Dispatchable, InProgress) = %v, want it to include %q", got, SpecMismatchLabel)
	}
}

// TestDispatchLabels_ClaimRemoveLabels_NonClaimOnlyRemovesFrom verifies a
// transition that doesn't land on InProgress removes only the from-state
// label, matching TransitionState's prior one-label contract.
func TestDispatchLabels_ClaimRemoveLabels_NonClaimOnlyRemovesFrom(t *testing.T) {
	d := DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
	}
	got := d.ClaimRemoveLabels(InProgress, Complete)
	want := []string{"agent-in-progress"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("ClaimRemoveLabels = %v, want %v", got, want)
	}
}
