package freshness

import "testing"

// A stale Result with no prior recorded rev is content staleness, a new base
// tip a rebuild will fix, so Classify records the rev for the next run to
// compare against.
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

// A stale Result at the same rev as the prior recorded run means a rebuild
// already happened and did not help, so the guard classifies it HostTainted
// and clears the persisted prior-stale-rev memory.
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

// A stale Result at a different rev than the prior recorded run is a genuinely
// new base tip, so it stays content staleness rather than host taint.
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

// An empty TipTag at the same rev is a stuck eval or tag-derivation failure
// repeating, not host taint: a genuine host-taint divergence always has a
// derived tip tag. The loop has to keep rebuilding and retrying.
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

// Regression test for the launcher-only-stale bug (issue #1364's research
// comment). This Result shape is what Probe produces when the image matches
// but the launcher does not, and two runs stuck at the same launcher-stale tip
// must both return Rebuild. HostTainted here would report a non-converging
// image-tag divergence, exit 5 instead of exit 4.
func TestGuard_Classify_LauncherOnlyStale_RebuildsNotHostTaint(t *testing.T) {
	g := NewGuard(t.TempDir())
	res := Result{Applicable: true, Fresh: false, ImageFresh: true, LauncherFresh: false, Rev: "revA", TipTag: ""}

	first := g.Classify(res)
	if first != Rebuild {
		t.Fatalf("first Classify() = %v, want Rebuild", first)
	}

	second := g.Classify(res)
	if second != Rebuild {
		t.Errorf("second Classify() (same rev) = %v, want Rebuild, not HostTainted", second)
	}
}

// An empty Rev is a transient fetch failure, not a resolved base-tip rev, so
// NonConverging treats it as unknown rather than as the same rev as before,
// even when the prior is also empty.
func TestGuard_Classify_EmptyStaleRev_NeverHostTainted(t *testing.T) {
	g := NewGuard(t.TempDir())

	got := g.Classify(Result{Rev: "", TipTag: "spindrift:hash"})

	if got != Rebuild {
		t.Errorf("Classify() = %v, want Rebuild", got)
	}
}
