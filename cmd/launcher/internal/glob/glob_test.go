package glob

import (
	"strings"
	"testing"
	"time"
)

// The "dir/**" shape is the one MERGE_GUARD_PATHS uses for .github/**.
func TestMatch_DoubleStarDirectory(t *testing.T) {
	if !Match(".github/**", ".github/workflows/ci.yml") {
		t.Error("expected .github/** to match .github/workflows/ci.yml")
	}
}

// The case matrix is ported from the Merge guard's own acceptance criteria.
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

func TestOverlap_LiteralPathHit(t *testing.T) {
	if !Overlap("lib/env-schema.nix", "lib/env-schema.nix") {
		t.Error("expected lib/env-schema.nix to overlap itself")
	}
}

func TestOverlap_Disjoint(t *testing.T) {
	if Overlap("cmd/launcher/*.go", "docs/*.md") {
		t.Error("expected no overlap between cmd/launcher/*.go and docs/*.md")
	}
}

func TestOverlap_SingleSegmentWildcard(t *testing.T) {
	if !Overlap("cmd/launcher/*.go", "cmd/launcher/main.go") {
		t.Error("expected cmd/launcher/*.go to overlap cmd/launcher/main.go")
	}
}

func TestOverlap_DoubleStarAnyDepth(t *testing.T) {
	if !Overlap("lib/**", "lib/nested/dir/file.nix") {
		t.Error("expected lib/** to overlap lib/nested/dir/file.nix")
	}
}

func TestOverlap_DifferentDepthNoWildcardNoOverlap(t *testing.T) {
	if Overlap("cmd/launcher/*.go", "cmd/launcher/internal/forge/exec.go") {
		t.Error("expected no overlap: extra path segment with no ** present")
	}
}

// Patterns reach Overlap from untrusted prompt input, so a hostile issue
// filer can write one with many "**" segments. Overlap must stay polynomial
// in the segment count instead of backtracking exponentially the way a
// recursive "** matches any suffix" check would.
func TestOverlap_ManyDoubleStarsDoesNotHang(t *testing.T) {
	// Every "**" can match the run of "y" segments, so the mismatch only
	// shows up at the final "x" against "y" comparison. Naive backtracking
	// has to re-explore every star and segment split before it concludes
	// there is no overlap.
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

// Match and Overlap must give the same pattern syntax the same meaning, so a
// literal path that a pattern matches also overlaps that pattern.
func TestMatchOverlapConsistency(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
	}{
		{".github/**", ".github/workflows/ci.yml"},
		{"cmd/launcher/*.go", "cmd/launcher/main.go"},
		{"**/CLAUDE.md", "services/api/CLAUDE.md"},
		{"cmd/launcher/*.go", "docs/reference.md"},
		{"docs/*.md", "cmd/launcher/internal/forge/exec.go"},
	}
	for _, tc := range cases {
		if Match(tc.pattern, tc.path) != Overlap(tc.pattern, tc.path) {
			t.Errorf("Match(%q, %q) and Overlap(%q, %q) disagree", tc.pattern, tc.path, tc.pattern, tc.path)
		}
	}
}
