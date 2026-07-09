package main

import "testing"

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
