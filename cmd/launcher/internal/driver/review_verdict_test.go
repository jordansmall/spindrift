package driver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/passmachine"
)

// reviewPassFixtures seeds a native review-pass log per Driver whose final
// top-level message is msg. ResultEvent cannot do it generically: claude's is a
// {"type":"result"} event, which claude's RenderTranscript does not render. A
// Driver registered without an entry here fails TestReviewVerdictSurvivesRenderTranscript.
var reviewPassFixtures = map[string]func(msg string) any{
	"claude": func(msg string) any {
		return map[string]any{
			"type": "assistant",
			"message": map[string]any{
				"content": []map[string]any{{"type": "text", "text": msg}},
			},
		}
	},
	"opencode": func(msg string) any {
		return map[string]any{
			"type": "text",
			"part": map[string]any{"text": msg},
		}
	},
}

// TestReviewVerdictSurvivesRenderTranscript runs every registered Driver's
// review-pass transcript through the verdict scan, so a Driver whose rendering
// drops the reviewer role prefix cannot silently lose review verdicts.
func TestReviewVerdictSurvivesRenderTranscript(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want passmachine.Verdict
	}{
		{"block", "VERDICT: BLOCK\n\n## Blocking\n- x.go:1 -- bug", passmachine.VerdictBlock},
		{"approve", "VERDICT: APPROVE", passmachine.VerdictApprove},
	}
	for _, name := range Names() {
		t.Run(name, func(t *testing.T) {
			fixture, ok := reviewPassFixtures[name]
			if !ok {
				t.Fatalf("Driver %q has no review-pass fixture: add one to reviewPassFixtures so its verdicts are checked", name)
			}
			d, err := New(name)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					ev, err := json.Marshal(fixture(tc.msg))
					if err != nil {
						t.Fatalf("marshal fixture: %v", err)
					}
					path := filepath.Join(t.TempDir(), "stream.log")
					if err := os.WriteFile(path, append(ev, '\n'), 0o600); err != nil {
						t.Fatal(err)
					}
					rendered, err := d.RenderTranscript(path, driverkit.RenderOptions{TopLevelRole: driverkit.ReviewerRole})
					if err != nil {
						t.Fatalf("RenderTranscript: %v", err)
					}
					res := passmachine.Scan(rendered, passmachine.KindReview)
					if res.Verdict != tc.want {
						t.Fatalf("verdict = %q, want %q (rendered %q)", res.Verdict, tc.want, rendered)
					}
					if tc.want == passmachine.VerdictBlock && res.BlockLine < 0 {
						t.Errorf("BlockLine = %d, want >= 0", res.BlockLine)
					}
				})
			}
		})
	}
}
