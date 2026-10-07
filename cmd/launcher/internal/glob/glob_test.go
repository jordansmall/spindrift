package glob

import (
	"runtime"
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

// Two patterns overlap when some name satisfies both, even if neither pattern
// is a literal one the other matches.
func TestOverlap_Segments(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"cmd/*.go", "cmd/main*", true},
		{"src/foo*", "src/*bar", true},
		{"a/*x", "a/x*", true},
		{"a/?b", "a/a?", true},
		{"**/*.go", "cmd/main*", true},
		{"a/[ab]c", "a/a?", true},
		{"a/[ab]", "a/a", true},
		{"cmd/launcher/internal/forge/*.go", "cmd/launcher/internal/forge/verdict*", true},
		{"a/[^b]", "a/?", true},
		{`a/\*`, "a/*", true},
		{"a/[a-c]", "a/[c-e]", true},
		{"src/*.go", "src/*.md", false},
		{"a/[ab]", "a/c", false},
		{"a/[^a]", "a/a", false},
		{"a/?", "a/ab", false},
		{"a/[a-c]", "a/[d-f]", false},
		{"a/[^a-z]", "a/[a-z]", false},
		{`a/\*`, "a/b", false},
		// Malformed patterns fail closed.
		{"a/[", "a/b", true},
		{`a/\`, "a/b", true},
		{"\xc3?", "é", true},
	}
	for _, tc := range cases {
		if got := Overlap(tc.a, tc.b); got != tc.want {
			t.Errorf("Overlap(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := Overlap(tc.b, tc.a); got != tc.want {
			t.Errorf("Overlap(%q, %q) = %v, want %v", tc.b, tc.a, got, tc.want)
		}
	}
}

// A single segment full of "*" must not backtrack exponentially, and a long
// run of character classes must stay polynomial too.
func TestOverlap_LongSegmentDoesNotHang(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"stars", strings.Repeat("*a", 500) + "b", strings.Repeat("*a", 500) + "c", false},
		{"classes", "x" + strings.Repeat("[ac]", 500), "y" + strings.Repeat("[bd]", 500), false},
		{"wide classes", strings.Repeat("[a-bd-fh-jl-n]*", 300), strings.Repeat("*[c-eg-ik-m]", 300), true},
	}
	for _, tc := range cases {
		for _, o := range []struct{ order, x, y string }{{"a,b", tc.a, tc.b}, {"b,a", tc.b, tc.a}} {
			done := make(chan bool, 1)
			go func() { done <- Overlap(o.x, o.y) }()
			select {
			case got := <-done:
				if got != tc.want {
					t.Errorf("%s (%s): Overlap = %v, want %v", tc.name, o.order, got, tc.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s (%s): Overlap did not return within 2s", tc.name, o.order)
			}
		}
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
		{"a/[ab]", "a/a"},
		{"a/[ab]", "a/c"},
		{"a/[^b]c", "a/ac"},
		{"a/[a-c]?", "a/bz"},
	}
	for _, tc := range cases {
		m := Match(tc.pattern, tc.path)
		if m != Overlap(tc.pattern, tc.path) {
			t.Errorf("Match(%q, %q) and Overlap(%q, %q) disagree", tc.pattern, tc.path, tc.pattern, tc.path)
		}
		if m != Overlap(tc.path, tc.pattern) {
			t.Errorf("Match(%q, %q) and Overlap(%q, %q) disagree", tc.pattern, tc.path, tc.path, tc.pattern)
		}
	}
}

// overlapAllocBound caps what one Overlap call may allocate in the memory tests.
const overlapAllocBound = 4 << 20

// checkOverlapAlloc fails t if Overlap(a, b) != want or allocates over the
// bound; label names the argument order so a failure says which one.
func checkOverlapAlloc(t *testing.T, label, a, b string, want bool) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	got := Overlap(a, b)
	runtime.ReadMemStats(&after)
	if got != want {
		t.Errorf("%s: Overlap = %v, want %v", label, got, want)
	}
	if delta := after.TotalAlloc - before.TotalAlloc; delta > overlapAllocBound {
		t.Errorf("%s: Overlap allocated %d bytes, want under 4 MiB", label, delta)
	}
}

// The product automaton must keep memory linear in segment length: a full
// (len(a)+1)*(len(b)+1) table is ~16 MB here, so a small bound catches it.
func TestOverlap_LongSegmentMemoryNotQuadratic(t *testing.T) {
	a := strings.Repeat("a", 4000) + "*"
	b := "*" + strings.Repeat("b", 4000)
	checkOverlapAlloc(t, "a,b", a, b, true)
	checkOverlapAlloc(t, "b,a", b, a, true)
}

// segmentsOverlap must keep memory linear in segment count: a full table is
// ~16 MB here and per-cell tokenizing/rows add GBs, so a small bound catches both.
func TestOverlap_ManySegmentsMemoryNotQuadratic(t *testing.T) {
	a := strings.Repeat("[ab]/", 4000) + "**"
	b := strings.Repeat("a/", 4000) + "y"
	checkOverlapAlloc(t, "a,b", a, b, true)
	checkOverlapAlloc(t, "b,a", b, a, true)
}
