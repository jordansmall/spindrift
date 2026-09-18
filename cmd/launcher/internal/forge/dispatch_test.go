package forge

import "testing"

// Untriaged must map to the empty label string, so a
// TransitionState(Untriaged, X) promotion never asks an adapter to remove a
// label the issue never had (#646).
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

// Recoverable is a local-only frontmatter marker, never a real GitHub label,
// so Label resolves it but AllLabels must exclude it from the set the local
// adapter's ListLabels reports (#2254).
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
	if len(all) != 5 {
		t.Fatalf("AllLabels len = %d, want 5 (Recoverable excluded, Ambiguous included)", len(all))
	}
	for _, l := range all {
		if l == "agent-recoverable" {
			t.Fatalf("AllLabels = %v, must not contain the Recoverable marker", all)
		}
	}
}

// Unlike Recoverable, Ambiguous is a real issue-tracker label, so AllLabels
// includes it (#2275).
func TestDispatchLabels_Ambiguous_LabelAndAllLabels(t *testing.T) {
	d := DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
		Ambiguous:    "agent-ambiguous-spec",
	}
	if got := d.Label(Ambiguous); got != "agent-ambiguous-spec" {
		t.Fatalf("Label(Ambiguous): got %q, want %q", got, "agent-ambiguous-spec")
	}
	all := d.AllLabels()
	if len(all) != 5 {
		t.Fatalf("AllLabels len = %d, want 5 (Ambiguous included)", len(all))
	}
	found := false
	for _, l := range all {
		if l == "agent-ambiguous-spec" {
			found = true
		}
	}
	if !found {
		t.Fatalf("AllLabels = %v, must contain the Ambiguous label", all)
	}
}

// A claim (to == InProgress) removes the from-state label plus both terminal
// labels, deduplicated. Both github's execClient and forge.Fake call this one
// method, so the two cannot drift apart (#1985).
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

// A transition that does not land on InProgress removes only the from-state
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
