package freshness

import "testing"

// TestGuard_Classify_ContentDivergence_RecordsAndRebuilds verifies that a
// stale Result with no prior recorded rev is treated as content staleness (a
// new base tip a rebuild will fix): Classify returns Rebuild and records the
// rev for the next run to compare against.
func TestGuard_Classify_ContentDivergence_RecordsAndRebuilds(t *testing.T) {
	g := NewGuard(t.TempDir())

	got := g.Classify(Result{Rev: "revA", TipTag: "spindrift:hash"})

	if got != Rebuild {
		t.Errorf("Classify() = %v, want Rebuild", got)
	}
	if prior := g.Prior(); prior != "revA" {
		t.Errorf("Prior() = %q, want %q", prior, "revA")
	}
}

// TestGuard_Classify_NonConverging_HostTaintedAndClears verifies that a
// stale Result at the SAME rev as the prior recorded run — a rebuild already
// happened and it's still stale — is classified HostTainted, and the
// persisted prior-stale-rev memory is cleared.
func TestGuard_Classify_NonConverging_HostTaintedAndClears(t *testing.T) {
	g := NewGuard(t.TempDir())
	if got := g.Classify(Result{Rev: "revA", TipTag: "spindrift:hash"}); got != Rebuild {
		t.Fatalf("seed Classify() = %v, want Rebuild", got)
	}

	got := g.Classify(Result{Rev: "revA", TipTag: "spindrift:hash"})

	if got != HostTainted {
		t.Errorf("Classify() = %v, want HostTainted", got)
	}
	if prior := g.Prior(); prior != "" {
		t.Errorf("Prior() = %q, want empty (cleared)", prior)
	}
}

// TestGuard_Classify_DifferentRevAfterPrior_RecordsAndRebuilds verifies that
// a stale Result at a DIFFERENT rev than the prior recorded run — a
// genuinely new base tip — is content staleness, not host taint: Classify
// returns Rebuild and records the new rev.
func TestGuard_Classify_DifferentRevAfterPrior_RecordsAndRebuilds(t *testing.T) {
	g := NewGuard(t.TempDir())
	if got := g.Classify(Result{Rev: "revA", TipTag: "spindrift:hash"}); got != Rebuild {
		t.Fatalf("seed Classify() = %v, want Rebuild", got)
	}

	got := g.Classify(Result{Rev: "revB", TipTag: "spindrift:hash"})

	if got != Rebuild {
		t.Errorf("Classify() = %v, want Rebuild", got)
	}
	if prior := g.Prior(); prior != "revB" {
		t.Errorf("Prior() = %q, want %q", prior, "revB")
	}
}

// TestGuard_Classify_SameRevEmptyTipTag_RebuildsNotHostTaint verifies that a
// stale Result at the SAME rev as the prior recorded run, but with an empty
// TipTag, is NOT classified HostTainted: it's a stuck eval/tag-derivation
// failure repeating at the same rev, not a genuine host-taint divergence
// (which always has a derived tip tag). Classify returns Rebuild and records
// the rev so the loop keeps rebuilding and retrying.
func TestGuard_Classify_SameRevEmptyTipTag_RebuildsNotHostTaint(t *testing.T) {
	g := NewGuard(t.TempDir())
	if got := g.Classify(Result{Rev: "revA", TipTag: "spindrift:hash"}); got != Rebuild {
		t.Fatalf("seed Classify() = %v, want Rebuild", got)
	}

	got := g.Classify(Result{Rev: "revA", TipTag: ""})

	if got != Rebuild {
		t.Errorf("Classify() = %v, want Rebuild", got)
	}
	if prior := g.Prior(); prior != "revA" {
		t.Errorf("Prior() = %q, want %q", prior, "revA")
	}
}

// TestGuard_Classify_EmptyStaleRev_NeverHostTainted verifies that an empty
// Rev (a transient fetch failure, not a resolved base-tip rev) is never
// classified HostTainted, even with an empty prior — NonConverging treats ""
// as "unknown", not "same as before".
func TestGuard_Classify_EmptyStaleRev_NeverHostTainted(t *testing.T) {
	g := NewGuard(t.TempDir())

	got := g.Classify(Result{Rev: "", TipTag: "spindrift:hash"})

	if got != Rebuild {
		t.Errorf("Classify() = %v, want Rebuild", got)
	}
}
