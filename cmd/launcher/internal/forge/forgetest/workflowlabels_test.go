package forgetest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseWorkflowRemoveLabelSet(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want []string
	}{
		{
			name: "block scalar stops at sibling key",
			yaml: "with:\n  claim-remove-labels: >-\n    a\n    b\n  git-user-name: bot\n  git-user-email: bot@example.com\n",
			want: []string{"a", "b"},
		},
		{
			name: "block scalar stops at blank line",
			yaml: "  claim-remove-labels: |\n    a\n    b\n\n    c\n",
			want: []string{"a", "b"},
		},
		{
			name: "block scalar at end of file without newline",
			yaml: "  claim-remove-labels: >\n    a b",
			want: []string{"a", "b"},
		},
		{
			name: "inline value",
			yaml: "  claim-remove-labels: a b\n  other: c\n",
			want: []string{"a", "b"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wf.yml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			got, _ := ParseWorkflowRemoveLabelSet(t, path, "claim-remove-labels")
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for _, l := range tc.want {
				if !got[l] {
					t.Errorf("got %v, missing %q", got, l)
				}
			}
		})
	}
}
