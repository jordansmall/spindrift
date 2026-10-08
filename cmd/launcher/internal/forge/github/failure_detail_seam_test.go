package github

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/seamtest"
)

// Drives the real FailureDetail through the seam gh fake, so a fake that
// answers its contexts query with non-JSON fails here instead of in a stub.
func TestFailureDetailSeam(t *testing.T) {
	const url = "https://github.com/o/r/pull/9"
	tests := []struct {
		name      string
		pr        seamtest.GhPR
		wantEmpty bool
		want      []string
		wantNot   []string
	}{
		{
			name:      "no failing checks",
			pr:        seamtest.GhPR{Number: 9, URL: url, Checks: "SUCCESS"},
			wantEmpty: true,
		},
		{
			name: "failing check run",
			pr: seamtest.GhPR{Number: 9, URL: url, Checks: "FAILURE", Contexts: `[` +
				`{"__typename":"CheckRun","name":"build","conclusion":"FAILURE","summary":"boom"},` +
				`{"__typename":"CheckRun","name":"lint","conclusion":"SUCCESS","summary":"fine"}]`},
			want:    []string{"build", "boom"},
			wantNot: []string{"lint"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PATH", seamtest.InstallFakes(t, "gh")+":"+os.Getenv("PATH"))
			for k, v := range seamtest.WriteFakeConfig(t, "gh", seamtest.GhConfig{Record: filepath.Join(t.TempDir(), "gh.rec"), PRs: []seamtest.GhPR{tt.pr}}) {
				t.Setenv(k, v)
			}
			got, err := NewExecClient("o/r", testLabels, "agent/issue-").FailureDetail(url)
			if err != nil {
				t.Fatalf("FailureDetail: %v", err)
			}
			if tt.wantEmpty && got != "" {
				t.Errorf("got %q; want empty", got)
			}
			for _, s := range tt.want {
				if !strings.Contains(got, s) {
					t.Errorf("detail %q missing %q", got, s)
				}
			}
			for _, s := range tt.wantNot {
				if strings.Contains(got, s) {
					t.Errorf("detail %q contains %q", got, s)
				}
			}
		})
	}
}
