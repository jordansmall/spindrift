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

// TestDispatchLabels_Recoverable_LabelAndAllLabels verifies Recoverable maps
// to its configured marker via Label, but is excluded from AllLabels: it is
// a local-only frontmatter marker (never a real GitHub label), so it must
// not appear in the registry-membership set the local adapter's ListLabels
// reports (#2254).
func TestDispatchLabels_Recoverable_LabelAndAllLabels(t *testing.T) {
	d := DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
		Recoverable:  "agent-recoverable",
	}
	if got := d.Label(Recoverable); got != "agent-recoverable" {
		t.Fatalf("Label(Recoverable): got %q, want %q", got, "agent-recoverable")
	}
	all := d.AllLabels()
	if len(all) != 4 {
		t.Fatalf("AllLabels len = %d, want 4 (Recoverable excluded)", len(all))
	}
	for _, l := range all {
		if l == "agent-recoverable" {
			t.Fatalf("AllLabels = %v, must not contain the Recoverable marker", all)
		}
	}
}

// TestDispatchLabels_ClaimRemoveLabels_ClaimStripsStaleTerminals verifies a
// claim (to == InProgress) removes the from-state label plus both terminal
// labels, deduplicated — the single source of truth github's execClient and
// forge.Fake both call, so the two can't drift apart (#1985).
func TestDispatchLabels_ClaimRemoveLabels_ClaimStripsStaleTerminals(t *testing.T) {
	d := DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
	}
	got := d.ClaimRemoveLabels(Dispatchable, InProgress)
	want := []string{"ready-for-agent", "agent-complete", "agent-failed"}
	if len(got) != len(want) {
		t.Fatalf("ClaimRemoveLabels = %v, want %v", got, want)
	}
	for i, l := range want {
		if got[i] != l {
			t.Errorf("ClaimRemoveLabels[%d] = %q, want %q", i, got[i], l)
		}
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
