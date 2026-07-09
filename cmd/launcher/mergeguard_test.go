package main

import "testing"

// TestGlobMatch_DoubleStarDirectory verifies that a "dir/**" pattern matches
// any path nested under dir, the shape MERGE_GUARD_PATHS uses for .github/**.
func TestGlobMatch_DoubleStarDirectory(t *testing.T) {
	if !globMatch(".github/**", ".github/workflows/ci.yml") {
		t.Error("expected .github/** to match .github/workflows/ci.yml")
	}
}

// TestMatchedGuardPaths_HitReturnsMatchedFiles verifies that a changed path
// matching one of the comma-separated MERGE_GUARD_PATHS globs is reported.
func TestMatchedGuardPaths_HitReturnsMatchedFiles(t *testing.T) {
	got := matchedGuardPaths(".github/**,**/CLAUDE.md", []string{"src/main.go", ".github/workflows/ci.yml"})
	want := []string{".github/workflows/ci.yml"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("matchedGuardPaths = %v, want %v", got, want)
	}
}

// TestMatchedGuardPaths covers the acceptance-criteria matrix: match,
// no-match, a deleted file's (still-reported) path, a nested CLAUDE.md, and
// the empty-string opt-out that disables the guard entirely.
func TestMatchedGuardPaths(t *testing.T) {
	const defaultPaths = ".github/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**"

	cases := []struct {
		name  string
		paths string
		files []string
		want  []string
	}{
		{
			name:  "no match — ordinary source file",
			paths: defaultPaths,
			files: []string{"src/main.go"},
			want:  nil,
		},
		{
			name:  "deleted file path still matches",
			paths: defaultPaths,
			// A deleted file is reported by its old path — the guard must
			// still catch it (added/modified/deleted all matter equally).
			files: []string{".github/workflows/old-ci.yml"},
			want:  []string{".github/workflows/old-ci.yml"},
		},
		{
			name:  "nested CLAUDE.md matches **/CLAUDE.md",
			paths: defaultPaths,
			files: []string{"services/api/CLAUDE.md"},
			want:  []string{"services/api/CLAUDE.md"},
		},
		{
			name:  "top-level CLAUDE.md also matches **/CLAUDE.md",
			paths: defaultPaths,
			files: []string{"CLAUDE.md"},
			want:  []string{"CLAUDE.md"},
		},
		{
			name:  "empty MERGE_GUARD_PATHS disables the guard entirely",
			paths: "",
			files: []string{"CLAUDE.md", ".github/workflows/ci.yml"},
			want:  nil,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := matchedGuardPaths(tc.paths, tc.files)
			if len(got) != len(tc.want) {
				t.Fatalf("matchedGuardPaths(%q, %v) = %v, want %v", tc.paths, tc.files, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("matchedGuardPaths(%q, %v) = %v, want %v", tc.paths, tc.files, got, tc.want)
				}
			}
		})
	}
}
