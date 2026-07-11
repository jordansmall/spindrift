package glob

import (
	"strings"
	"testing"
	"time"
)

// TestMatch_DoubleStarDirectory verifies that a "dir/**" pattern matches any
// path nested under dir, the shape MERGE_GUARD_PATHS uses for .github/**.
func TestMatch_DoubleStarDirectory(t *testing.T) {
	if !Match(".github/**", ".github/workflows/ci.yml") {
		t.Error("expected .github/** to match .github/workflows/ci.yml")
	}
}

// TestMatch covers the acceptance-criteria matrix ported from the Merge
// guard's tests: match, no-match, a nested CLAUDE.md, and a top-level
// CLAUDE.md, all against "**/CLAUDE.md".
func TestMatch(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{"no match — ordinary source file", ".github/**", "src/main.go", false},
		{"deleted file path still matches", ".github/**", ".github/workflows/old-ci.yml", true},
		{"nested CLAUDE.md matches **/CLAUDE.md", "**/CLAUDE.md", "services/api/CLAUDE.md", true},
		{"top-level CLAUDE.md also matches **/CLAUDE.md", "**/CLAUDE.md", "CLAUDE.md", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.pattern, tc.path); got != tc.want {
				t.Errorf("Match(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

// TestOverlap_LiteralPathHit verifies that two identical literal patterns
// overlap.
func TestOverlap_LiteralPathHit(t *testing.T) {
	if !Overlap("lib/env-schema.nix", "lib/env-schema.nix") {
		t.Error("expected lib/env-schema.nix to overlap itself")
	}
}

// TestOverlap_Disjoint verifies unrelated path globs never collide.
func TestOverlap_Disjoint(t *testing.T) {
	if Overlap("cmd/launcher/*.go", "docs/*.md") {
		t.Error("expected no overlap between cmd/launcher/*.go and docs/*.md")
	}
}

// TestOverlap_SingleSegmentWildcard verifies a single-segment "*" glob
// overlaps a literal file in the same directory.
func TestOverlap_SingleSegmentWildcard(t *testing.T) {
	if !Overlap("cmd/launcher/*.go", "cmd/launcher/main.go") {
		t.Error("expected cmd/launcher/*.go to overlap cmd/launcher/main.go")
	}
}

// TestOverlap_DoubleStarAnyDepth verifies "**" overlaps a path nested
// arbitrarily deep, mirroring Match's doublestar semantics.
func TestOverlap_DoubleStarAnyDepth(t *testing.T) {
	if !Overlap("lib/**", "lib/nested/dir/file.nix") {
		t.Error("expected lib/** to overlap lib/nested/dir/file.nix")
	}
}

// TestOverlap_DifferentDepthNoWildcardNoOverlap verifies that without a "**"
// a mismatched segment count never overlaps.
func TestOverlap_DifferentDepthNoWildcardNoOverlap(t *testing.T) {
	if Overlap("cmd/launcher/*.go", "cmd/launcher/internal/forge/exec.go") {
		t.Error("expected no overlap: extra path segment with no ** present")
	}
}

// TestOverlap_ManyDoubleStarsDoesNotHang verifies that a pattern with many
// "**" segments — untrusted prompt input, since a hostile issue filer could
// write one — does not blow up the naive exponential backtracking a
// recursive "** matches any suffix" check would hit; overlap checking must
// stay polynomial in the segment count.
func TestOverlap_ManyDoubleStarsDoesNotHang(t *testing.T) {
	// Every "**" can match the run of "y" segments, so the mismatch is only
	// ever discovered at the final "x" vs "y" comparison — naive backtracking
	// must re-explore every star/segment split before concluding no overlap.
	pathological := strings.Repeat("**/", 20) + "x"
	noTrailingX := strings.TrimSuffix(strings.Repeat("y/", 20), "/")

	done := make(chan bool, 1)
	go func() { done <- Overlap(pathological, noTrailingX) }()

	select {
	case overlap := <-done:
		if overlap {
			t.Error("expected no overlap: no segment in the ** chain is literally x")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Overlap did not return within 2s — likely exponential backtracking on repeated **")
	}
}

// TestMatchOverlapConsistency pins Match and Overlap to the same doublestar
// semantics for identical pattern syntax: a pattern overlaps itself iff it
// matches at least one path, and a literal path matched by a pattern is also
// an overlap between that pattern and the literal path used as a pattern.
func TestMatchOverlapConsistency(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
	}{
		{".github/**", ".github/workflows/ci.yml"},
		{"cmd/launcher/*.go", "cmd/launcher/main.go"},
		{"**/CLAUDE.md", "services/api/CLAUDE.md"},
	}
	for _, tc := range cases {
		if Match(tc.pattern, tc.path) != Overlap(tc.pattern, tc.path) {
			t.Errorf("Match(%q, %q) and Overlap(%q, %q) disagree", tc.pattern, tc.path, tc.pattern, tc.path)
		}
	}
}
