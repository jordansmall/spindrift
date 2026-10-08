package forgejo

import (
	"strings"
	"testing"
)

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
func TestMergeStyle(t *testing.T) {
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
		if got := MergeStyle(tt.method); got != tt.want {
			t.Errorf("MergeStyle(%q) = %q, want %q", tt.method, got, tt.want)
		}
	}
}

func TestParsePRIndex(t *testing.T) {
	for _, tc := range []struct {
		name    string
		url     string
		want    string
		wantErr []string
	}{
		{name: "same repo", url: "https://forge.test/owner/repo/pulls/206", want: "206"},
		{name: "case differs", url: "https://forge.test/Owner/Repo/pulls/7", want: "7"},
		{name: "host and base path differ", url: "https://other.example/git/owner/repo/pulls/7", want: "7"},
		{name: "trailing slash", url: "https://forge.test/owner/repo/pulls/7/", want: "7"},
		{name: "empty index", url: "https://forge.test/owner/repo/pulls/", wantErr: []string{"invalid PR URL"}},
		{name: "non-numeric index", url: "https://forge.test/owner/repo/pulls/abc", wantErr: []string{"not numeric"}},
		{name: "other owner and repo", url: "https://forge.test/other/thing/pulls/7", wantErr: []string{"other/thing", "owner/repo"}},
		{name: "other repo only", url: "https://forge.test/owner/thing/pulls/7", wantErr: []string{"owner/thing", "owner/repo"}},
		{name: "too short", url: "https://forge.test/7", wantErr: []string{"invalid PR URL"}},
		{name: "not a pulls path", url: "https://forge.test/owner/repo/issues/7", wantErr: []string{"invalid PR URL"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePRIndex(tc.url, "owner/repo")
			if tc.wantErr == nil {
				if err != nil || got != tc.want {
					t.Fatalf("parsePRIndex(%q) = %q, %v; want %q, nil", tc.url, got, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("parsePRIndex(%q) = %q, nil; want error", tc.url, got)
			}
			for _, w := range tc.wantErr {
				if !strings.Contains(err.Error(), w) {
					t.Fatalf("parsePRIndex(%q) error = %v; want mention of %q", tc.url, err, w)
				}
			}
		})
	}
}
