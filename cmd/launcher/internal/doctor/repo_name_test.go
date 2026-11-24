package doctor

import (
	"strings"
	"testing"
)

func TestRepoNameCheck(t *testing.T) {
	cases := []struct {
		name, canonical, slug string
		wantErr               []string
	}{
		{name: "match passes", canonical: "owner/repo", slug: "owner/repo"},
		{name: "renamed warns naming both", canonical: "owner/new", slug: "owner/old",
			wantErr: []string{"owner/new", "owner/old"}},
		{name: "case-only difference warns", canonical: "owner/repo", slug: "Owner/Repo",
			wantErr: []string{"owner/repo", "Owner/Repo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ck := RepoNameCheck(tc.canonical, tc.slug)
			if ck.Name != "repo-name" || ck.Tier != Advisory {
				t.Fatalf("got %s/%v, want repo-name/Advisory", ck.Name, ck.Tier)
			}
			out, err := ck.Probe()
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if msg := ck.SuccessMsg(out); !strings.Contains(msg, tc.canonical) {
					t.Errorf("SuccessMsg %q must name %s", msg, tc.canonical)
				}
				return
			}
			if err == nil {
				t.Fatal("want an error")
			}
			for _, w := range tc.wantErr {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q missing %q", err, w)
				}
			}
			if !strings.Contains(ck.Remedy, tc.canonical) {
				t.Errorf("Remedy %q must name %s", ck.Remedy, tc.canonical)
			}
		})
	}
}
