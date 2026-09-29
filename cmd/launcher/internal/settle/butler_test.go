package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
)

// TestFileButlerFindings_PlanSeesPatch pins Patch's path through the settle
// package's own butler-finding view (ADR 0057, issue #4072): the plan
// callback -- called before anything is filed, per Finding's own doc comment
// -- sees the same Patch the issue-intent payload carried, unexamined.
func TestFileButlerFindings_PlanSeesPatch(t *testing.T) {
	const diff = "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-a\n+b\n"
	fc := forge.NewFake()

	result := dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"t","body":"b","class":"flaky-test","patch":"` +
				jsonEscapeForTest(diff) + `"}`,
		},
	}

	var gotPatch string
	var sawFinding bool
	plan := func(kept []Finding) func(Finding) Decoration {
		if len(kept) != 1 {
			t.Fatalf("kept = %+v, want 1 finding", kept)
		}
		gotPatch = kept[0].Patch
		sawFinding = true
		return func(Finding) Decoration { return Decoration{} }
	}

	FileButlerFindings(fc.AsIssueFiler(), "1", result, 0, plan)

	if !sawFinding {
		t.Fatal("plan was never called")
	}
	if gotPatch != diff {
		t.Fatalf("Finding.Patch = %q, want %q", gotPatch, diff)
	}
}

// jsonEscapeForTest escapes s for embedding in a hand-built JSON string
// literal -- only the two bytes this test's diff fixture actually contains.
func jsonEscapeForTest(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\n':
			out = append(out, '\\', 'n')
		case '"':
			out = append(out, '\\', '"')
		default:
			out = append(out, s[i])
		}
	}
	return string(out)
}
