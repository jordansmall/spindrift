package settle

import "testing"

// Internal seam: a pure glob-matching matrix; the Settle table pins the guard
// hit and miss wiring, so each glob shape is cheaper to check directly.
// It covers match, no-match, a deleted file's (still-reported) path, a nested
// CLAUDE.md, and the empty-string opt-out that disables the guard entirely.
func TestInternalSeam_MatchedGuardPaths(t *testing.T) {
	const defaultPaths = ".github/**,.forgejo/**,**/CLAUDE.md,**/AGENTS.md,.claude/**,.opencode/**,.pi/**"

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
		{
			name:  ".forgejo/ workflow matches — guarded on every backend",
			paths: defaultPaths,
			files: []string{".forgejo/workflows/ci.yml"},
			want:  []string{".forgejo/workflows/ci.yml"},
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
