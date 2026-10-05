package forgejo

import "testing"

func TestParsePRIndex_HappyPath(t *testing.T) {
	got, err := parsePRIndex("https://forge.test/owner/repo/pulls/206")
	if err != nil {
		t.Fatalf("parsePRIndex(...) unexpected error: %v", err)
	}
	if got != "206" {
		t.Fatalf("parsePRIndex(...) = %q, want %q", got, "206")
	}
}

func TestParsePRIndex_RejectsEmpty(t *testing.T) {
	if _, err := parsePRIndex("https://forge.test/owner/repo/pulls/"); err == nil {
		t.Fatal("parsePRIndex(...) with empty trailing segment: want error, got nil")
	}
}

func TestParsePRIndex_RejectsNonNumeric(t *testing.T) {
	if _, err := parsePRIndex("https://forge.test/owner/repo/pulls/abc"); err == nil {
		t.Fatal("parsePRIndex(...) with non-numeric trailing segment: want error, got nil")
	}
}

// Forgejo's defaults are "WIP:" and "[WIP]" (no colon); "[WIP]:" is the bracket
// form followed by a colon, which a title may also carry.
func TestIsDraftTitle_RecognizesBothWIPPrefixes(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		{"[WIP] add feature", true},
		{"[wip] add feature", true},
		{"[WIP]: add feature", true},
		{"[wip]: add feature", true},
		{"WIP: add feature", true},
		{"add feature", false},
	}
	for _, tt := range tests {
		if got := isDraftTitle(tt.title); got != tt.want {
			t.Errorf("isDraftTitle(%q) = %v, want %v", tt.title, got, tt.want)
		}
	}
}

func TestStripWIPPrefix_StripsEveryRecognizedPrefix(t *testing.T) {
	for _, title := range []string{
		"[WIP] add feature",
		"[wip] add feature",
		"[WIP]: add feature",
		"WIP: add feature",
		"[WIP] : add feature",
	} {
		if got := stripWIPPrefix(title); got != "add feature" {
			t.Errorf("stripWIPPrefix(%q) = %q, want %q", title, got, "add feature")
		}
	}
}

func TestStripWIPPrefix_StripsStackedMarkers(t *testing.T) {
	for _, tc := range []struct{ title, want string }{
		{"WIP: [WIP] add feature", "add feature"},
		{"WIP: WIP: add feature", "add feature"},
		{"[WIP] WIP: add feature", "add feature"},
		{"[WIP]", ""},
		{"WIP:", ""},
		{"wip: ", ""},
		{"[WIP]:", ""},
	} {
		got := stripWIPPrefix(tc.title)
		if got != tc.want {
			t.Errorf("stripWIPPrefix(%q) = %q, want %q", tc.title, got, tc.want)
		}
		if isDraftTitle(got) {
			t.Errorf("stripWIPPrefix(%q) = %q, still a draft title", tc.title, got)
		}
	}
}

// An unset or unrecognized method falls back to "rebase", matching the github
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
