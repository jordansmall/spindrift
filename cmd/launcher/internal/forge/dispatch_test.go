package forge

import (
	"slices"
	"testing"
)

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

// A claim (to == InProgress) removes the from-state label plus the Complete,
// Failed, and Ambiguous labels, deduplicated. Both github's execClient and
// forge.Fake call this one method, so the two cannot drift apart (#1985).
func TestDispatchLabels_TransitionRemoveLabels_ClaimStripsStaleTerminals(t *testing.T) {
	d := DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
		Ambiguous:    "agent-ambiguous-spec",
	}
	got := d.transitionRemoveLabels(Dispatchable, InProgress)
	want := []string{"ready-for-agent", "agent-complete", "agent-failed", "agent-ambiguous-spec"}
	if len(got) != len(want) {
		t.Fatalf("TransitionRemoveLabels = %v, want %v", got, want)
	}
	for i, l := range want {
		if got[i] != l {
			t.Errorf("TransitionRemoveLabels[%d] = %q, want %q", i, got[i], l)
		}
	}
}

// A transition that is neither a claim nor a landing (to == Complete) removes
// only the from-state label, matching TransitionState's prior one-label
// contract.
func TestDispatchLabels_TransitionRemoveLabels_NeitherClaimNorLandingOnlyRemovesFrom(t *testing.T) {
	d := DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
	}
	got := d.transitionRemoveLabels(InProgress, Failed)
	want := []string{"agent-in-progress"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("TransitionRemoveLabels = %v, want %v", got, want)
	}
}

func TestDispatchLabels_AlreadyClaimed(t *testing.T) {
	d := DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
	}

	// A genuine second claim: labels already carry InProgress, from is a
	// distinct state.
	if !d.AlreadyClaimed(Dispatchable, InProgress, []string{"agent-in-progress"}) {
		t.Error("AlreadyClaimed(Dispatchable, InProgress, [agent-in-progress]) = false, want true")
	}

	// A normal claim onto an issue that does not yet carry InProgress.
	if d.AlreadyClaimed(Dispatchable, InProgress, []string{"ready-for-agent"}) {
		t.Error("AlreadyClaimed(Dispatchable, InProgress, [ready-for-agent]) = true, want false")
	}

	// The dispatch-workflow re-entry: from's own label equals the InProgress
	// label (the workflow already claimed and handed the launcher
	// --label agent-in-progress), so this must not read as an already-claimed
	// error (#3887).
	if d.AlreadyClaimed(InProgress, InProgress, []string{"agent-in-progress"}) {
		t.Error("AlreadyClaimed(InProgress, InProgress, [agent-in-progress]) = true, want false")
	}

	// A transition that does not land on InProgress is never "already
	// claimed".
	if d.AlreadyClaimed(InProgress, Complete, []string{"agent-in-progress"}) {
		t.Error("AlreadyClaimed(InProgress, Complete, [agent-in-progress]) = true, want false")
	}
}

// A landing (to == Complete) also strips a stale Failed, so a recovered issue
// parked agent-failed never wears both terminal labels (#4651).
func TestDispatchLabels_TransitionRemoveLabels_CompleteStripsStaleFailed(t *testing.T) {
	d := DispatchLabels{
		Dispatchable: "ready-for-agent",
		InProgress:   "agent-in-progress",
		Complete:     "agent-complete",
		Failed:       "agent-failed",
	}
	got := d.transitionRemoveLabels(InProgress, Complete)
	want := []string{"agent-in-progress", "agent-failed"}
	if !slices.Equal(got, want) {
		t.Errorf("TransitionRemoveLabels = %v, want %v", got, want)
	}
}

func TestDispatchLabels_SettledLabels(t *testing.T) {
	cases := []struct {
		name string
		d    DispatchLabels
		want []string
	}{
		{
			name: "default-like family",
			d: DispatchLabels{
				Dispatchable: "ready-for-agent",
				InProgress:   "agent-in-progress",
				Complete:     "agent-complete",
				Failed:       "agent-failed",
				Ambiguous:    "agent-ambiguous-spec",
			},
			want: []string{"agent-complete", "agent-failed", "agent-ambiguous-spec"},
		},
		{
			name: "recoverable is excluded",
			d: DispatchLabels{
				Complete:    "agent-complete",
				Failed:      "agent-failed",
				Ambiguous:   "agent-ambiguous-spec",
				Recoverable: "agent-recoverable",
			},
			want: []string{"agent-complete", "agent-failed", "agent-ambiguous-spec"},
		},
		{
			name: "research-like family with no complete label",
			d: DispatchLabels{
				Failed:    "agent-research-failed",
				Ambiguous: "agent-ambiguous-spec",
			},
			want: []string{"agent-research-failed", "agent-ambiguous-spec"},
		},
		{
			name: "duplicates collapse",
			d:    DispatchLabels{Complete: "x", Failed: "x", Ambiguous: "y"},
			want: []string{"x", "y"},
		},
		{name: "zero value", d: DispatchLabels{}, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.d.SettledLabels()
			if !slices.Equal(got, tc.want) {
				t.Errorf("SettledLabels = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSettledLabels_AppendsVerdictLabelsInOrderDeduplicated(t *testing.T) {
	d := ResearchDispatchLabels()
	v := NewVerdictLabels(
		VerdictLabel{Verdict: Recommend, Label: "agent-research-recommend"},
		VerdictLabel{Verdict: Reject, Label: "agent-research-failed"}, // equals d.Failed
		VerdictLabel{Verdict: Unclear, Label: "agent-research-unclear"},
		VerdictLabel{Verdict: "blank", Label: ""}, // empty label: skipped
	)
	got := SettledLabels(d, v)
	want := []string{"agent-research-failed", "agent-research-recommend", "agent-research-unclear"}
	if !slices.Equal(got, want) {
		t.Errorf("SettledLabels = %v, want %v", got, want)
	}
}

func TestSettledLabels_ZeroVerdictsIsDispatchSettledLabels(t *testing.T) {
	d := DispatchLabels{Complete: "agent-complete", Failed: "agent-failed"}
	if got, want := SettledLabels(d, VerdictLabels{}), d.SettledLabels(); !slices.Equal(got, want) {
		t.Errorf("SettledLabels = %v, want %v", got, want)
	}
}

func TestTransitionRemoveLabels_ClaimStripsVerdictLabels(t *testing.T) {
	d := ResearchDispatchLabels()
	v := ResearchVerdictLabels()
	got := TransitionRemoveLabels(d, v, Dispatchable, InProgress)
	want := []string{
		"agent-research", "agent-research-failed",
		"agent-research-recommend", "agent-research-reject", "agent-research-unclear",
	}
	if !slices.Equal(got, want) {
		t.Errorf("TransitionRemoveLabels = %v, want %v", got, want)
	}
}

func TestTransitionRemoveLabels_NonClaimLeavesVerdictLabels(t *testing.T) {
	d := ResearchDispatchLabels()
	v := ResearchVerdictLabels()
	for _, to := range []DispatchState{Dispatchable, Complete, Failed} {
		got := TransitionRemoveLabels(d, v, InProgress, to)
		if want := d.transitionRemoveLabels(InProgress, to); !slices.Equal(got, want) {
			t.Errorf("to=%v: TransitionRemoveLabels = %v, want %v", to, got, want)
		}
	}
}
