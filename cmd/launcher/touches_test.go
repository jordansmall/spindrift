package main

import (
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestTouchSetsOverlap_LiteralPathHit verifies that two touch-sets sharing an
// identical literal path are reported as overlapping.
func TestTouchSetsOverlap_LiteralPathHit(t *testing.T) {
	if !touchSetsOverlap([]string{"lib/env-schema.nix"}, []string{"lib/env-schema.nix", "README.md"}) {
		t.Error("expected overlap on lib/env-schema.nix")
	}
}

// TestTouchSetsOverlap_Disjoint verifies unrelated path globs never collide.
func TestTouchSetsOverlap_Disjoint(t *testing.T) {
	if touchSetsOverlap([]string{"cmd/launcher/*.go"}, []string{"docs/*.md"}) {
		t.Error("expected no overlap between cmd/launcher/*.go and docs/*.md")
	}
}

// TestTouchSetsOverlap_SingleSegmentWildcard verifies a single-segment "*"
// glob overlaps a literal file in the same directory.
func TestTouchSetsOverlap_SingleSegmentWildcard(t *testing.T) {
	if !touchSetsOverlap([]string{"cmd/launcher/*.go"}, []string{"cmd/launcher/main.go"}) {
		t.Error("expected cmd/launcher/*.go to overlap cmd/launcher/main.go")
	}
}

// TestTouchSetsOverlap_DoubleStarAnyDepth verifies "**" overlaps a path
// nested arbitrarily deep, mirroring MERGE_GUARD_PATHS' doublestar semantics.
func TestTouchSetsOverlap_DoubleStarAnyDepth(t *testing.T) {
	if !touchSetsOverlap([]string{"lib/**"}, []string{"lib/nested/dir/file.nix"}) {
		t.Error("expected lib/** to overlap lib/nested/dir/file.nix")
	}
}

// TestTouchSetsOverlap_DifferentDepthNoWildcardNoOverlap verifies that
// without a "**" a mismatched segment count never overlaps.
func TestTouchSetsOverlap_DifferentDepthNoWildcardNoOverlap(t *testing.T) {
	if touchSetsOverlap([]string{"cmd/launcher/*.go"}, []string{"cmd/launcher/internal/forge/exec.go"}) {
		t.Error("expected no overlap: extra path segment with no ** present")
	}
}

// TestOverlapsInProgress_CollidingTouches verifies a candidate's declared
// touch-set overlapping an InProgress issue's declared touch-set is reported,
// naming the colliding issue.
func TestOverlapsInProgress_CollidingTouches(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Body: "## Touches\n- lib/env-schema.nix", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- lib/env-schema.nix", State: "OPEN", Labels: []string{"agent-in-progress"}})

	inProgress, err := fc.ListIssues(forge.InProgress)
	if err != nil {
		t.Fatalf("ListIssues: %v", err)
	}
	collider, held := overlapsInProgress(fc, "10", inProgress)
	if !held || collider != "20" {
		t.Errorf("overlapsInProgress = (%q, %v), want (\"20\", true)", collider, held)
	}
}

// TestOverlapsInProgress_DisjointTouches verifies disjoint touch-sets never
// hold a candidate.
func TestOverlapsInProgress_DisjointTouches(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Body: "## Touches\n- lib/env-schema.nix", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- docs/reference.md", State: "OPEN", Labels: []string{"agent-in-progress"}})

	inProgress, _ := fc.ListIssues(forge.InProgress)
	if _, held := overlapsInProgress(fc, "10", inProgress); held {
		t.Error("expected no hold: disjoint touch-sets")
	}
}

// TestBatchHasTouchOverlap_DetectsOverlap verifies a batch containing an
// issue whose declared touches overlap an in-progress issue's is reported.
func TestBatchHasTouchOverlap_DetectsOverlap(t *testing.T) {
	c := config{overlapGate: "defer", inProgressLabel: "agent-in-progress"}
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Body: "## Touches\n- lib/env-schema.nix", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- lib/env-schema.nix", State: "OPEN", Labels: []string{c.inProgressLabel}})

	if !batchHasTouchOverlap(c, fc, []issue{{number: "10"}}) {
		t.Error("expected batch overlap to be detected")
	}
}

// TestBatchHasTouchOverlap_GateOffNeverOverlaps verifies OVERLAP_GATE=off
// short-circuits the check regardless of declared touches.
func TestBatchHasTouchOverlap_GateOffNeverOverlaps(t *testing.T) {
	c := config{overlapGate: "off", inProgressLabel: "agent-in-progress"}
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Body: "## Touches\n- lib/env-schema.nix", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- lib/env-schema.nix", State: "OPEN", Labels: []string{c.inProgressLabel}})

	if batchHasTouchOverlap(c, fc, []issue{{number: "10"}}) {
		t.Error("expected OVERLAP_GATE=off to disable the check")
	}
}

// TestOverlapsInProgress_NoDeclaredTouches verifies a candidate with no
// ## Touches section is never held, matching the "dispatched exactly as
// today" acceptance criterion.
func TestOverlapsInProgress_NoDeclaredTouches(t *testing.T) {
	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: "10", Body: "no touches section here", Labels: []string{"ready-for-agent"}})
	fc.SetIssue(forge.Issue{Number: "20", Body: "## Touches\n- lib/env-schema.nix", State: "OPEN", Labels: []string{"agent-in-progress"}})

	inProgress, _ := fc.ListIssues(forge.InProgress)
	if _, held := overlapsInProgress(fc, "10", inProgress); held {
		t.Error("expected no hold: candidate declared no touches")
	}
}
