package forgejo

import "testing"

// TestParsePRIndex_HappyPath verifies parsePRIndex extracts the trailing
// numeric path segment from a Forgejo PR html_url.
func TestParsePRIndex_HappyPath(t *testing.T) {
	got, err := parsePRIndex("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("parsePRIndex(...) unexpected error: %v", err)
	}
	if got != "206" {
		t.Fatalf("parsePRIndex(...) = %q, want %q", got, "206")
	}
}

// TestParsePRIndex_RejectsEmpty verifies parsePRIndex errors on a URL with no
// trailing path segment.
func TestParsePRIndex_RejectsEmpty(t *testing.T) {
	if _, err := parsePRIndex("https://forge.test/owner/repo/pulls/"); err == nil {
		t.Fatal("parsePRIndex(...) with empty trailing segment: want error, got nil")
	}
}

// TestParsePRIndex_RejectsNonNumeric verifies parsePRIndex errors when the
// trailing path segment is not a number.
func TestParsePRIndex_RejectsNonNumeric(t *testing.T) {
	if _, err := parsePRIndex("https://forge.test/owner/repo/pulls/abc"); err == nil {
		t.Fatal("parsePRIndex(...) with non-numeric trailing segment: want error, got nil")
	}
}

// TestForgejoMergeDo verifies forgejoMergeDo maps the MergeMethod knob's
// value onto the "Do" field value Forgejo's merge endpoint expects, with an
// empty (unset) method defaulting to "rebase" — mirroring the github
// adapter's mergeMethodFlag default.
func TestForgejoMergeDo(t *testing.T) {
	tests := []struct {
		method string
		want   string
	}{
		{"merge", "merge"},
		{"squash", "squash"},
		{"rebase", "rebase"},
		{"", "rebase"},
		{"bogus", "rebase"},
	}
	for _, tt := range tests {
		if got := forgejoMergeDo(tt.method); got != tt.want {
			t.Errorf("forgejoMergeDo(%q) = %q, want %q", tt.method, got, tt.want)
		}
	}
}
