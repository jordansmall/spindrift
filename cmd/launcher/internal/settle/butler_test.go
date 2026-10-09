package settle

import (
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/doctor"
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

// failed counts only PostIssue failures, so a caller can tell an all-failed
// sweep from a quiet one.
func TestFileButlerFindings_ReportsFailedCount(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"
	fc.PostIssueErrForTitle = map[string]error{"two": errFake}
	plan := func([]Finding) func(Finding) Decoration { return func(Finding) Decoration { return Decoration{} } }

	filing := FileButlerFindings(fc.AsIssueFiler(), "1", twoIntents(), 0, plan)

	if len(filing.Filed) != 1 || filing.Failed != 1 {
		t.Errorf("filed=%v failed=%d, want 1 filed and 1 failed", filing.Filed, filing.Failed)
	}
}

// A finding filed with the tuning label carries tuningChoreTerm in its hidden
// marker, beside the Box's own terms and also when the Box gave none -- the
// term is derived from the label, so the two cannot diverge.
func TestFileButlerFindings_TuningLabelWritesMarkerTerm(t *testing.T) {
	for _, tc := range []struct {
		name   string
		intent string
		want   []string
	}{
		{"with box terms", `{"title":"t","body":"b","dedupTerms":["Role:Implement"]}`, []string{"role:implement", tuningChoreTerm}},
		{"no box terms", `{"title":"t","body":"b"}`, []string{tuningChoreTerm}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := forge.NewFake()
			result := dispatch.Result{IssueIntentsFound: true, IssueIntents: []string{tc.intent}}
			plan := func([]Finding) func(Finding) Decoration {
				return func(Finding) Decoration { return Decoration{ExtraLabels: []string{doctor.TuningFindingLabel}} }
			}
			FileButlerFindings(fc.AsIssueFiler(), "1", result, 0, plan)
			if len(fc.PostIssueCalls) != 1 {
				t.Fatalf("PostIssueCalls = %+v, want 1", fc.PostIssueCalls)
			}
			call := fc.PostIssueCalls[0]
			if got := parseDedupMarker(call.Body); !slices.Equal(got, tc.want) {
				t.Errorf("marker terms = %v, want %v", got, tc.want)
			}
			if !slices.Contains(call.Labels, doctor.TuningFindingLabel) {
				t.Errorf("labels = %v, want %q", call.Labels, doctor.TuningFindingLabel)
			}
		})
	}
}
