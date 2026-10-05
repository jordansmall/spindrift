package settle

import (
	"slices"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

func twoIntents() dispatch.Result {
	return dispatch.Result{
		IssueIntentsFound: true,
		IssueIntents: []string{
			`{"title":"one","body":"b"}`,
			`{"title":"two","body":"b"}`,
		},
	}
}

func createdNames(fc *forge.Fake) []string {
	var out []string
	for _, c := range fc.CreateLabelCalls {
		out = append(out, c.Name)
	}
	return out
}

// Issue #4400: GitHub rejects `gh issue create --label X` when X is missing, so
// the provenance label of every kind must be ensure-created or each finding is
// dropped. Each kind is driven through its real settle entry point. Once
// created, a second intent in the same pass must not re-create it.
func TestSettleEntryPoints_EnsureFilingLabelPerKind(t *testing.T) {
	withIntents := func(r dispatch.Result) dispatch.Result {
		w := twoIntents()
		r.IssueIntentsFound, r.IssueIntents = w.IssueIntentsFound, w.IssueIntents
		return r
	}
	resolved := func(status string) dispatch.Result {
		return dispatch.Result{Resolved: outcome.Resolved{
			Found:   true,
			Outcome: outcome.Outcome{Issue: "1", Landing: "https://github.com/owner/repo/pull/9", Status: status, Note: "n"},
		}}
	}
	for _, tc := range []struct {
		name  string
		label string
		meta  doctor.LabelMeta
		newFC func() *forge.Fake
		file  func(fc *forge.Fake)
	}{
		{
			"work", dispatchkind.Work.FindingLabel, doctor.ReviewFindingLabelMeta[dispatchkind.Work.FindingLabel],
			func() *forge.Fake {
				fc := forge.NewFake(testDispatchLabels)
				fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
				return fc
			},
			func(fc *forge.Fake) {
				newTestSettle(baseConfig(), fc.AsIssueFiler(), fc).Settle(dispatch.NewFake(), "1", 0, withIntents(resolved("blocked")))
			},
		},
		{
			"research", dispatchkind.Research.FindingLabel, doctor.TriageLabelMeta[dispatchkind.Research.FindingLabel],
			func() *forge.Fake { return newResearchFake("1") },
			func(fc *forge.Fake) {
				NewResearchSettle(fc.AsIssueFiler(), researchVerdictLabels, false).Settle(dispatch.NewFake(), "1", 0, withIntents(resolved("recommend")))
			},
		},
		{
			"butler", dispatchkind.Butler.FindingLabel, doctor.TriageLabelMeta[dispatchkind.Butler.FindingLabel],
			func() *forge.Fake { return forge.NewFake(testDispatchLabels) },
			func(fc *forge.Fake) {
				plan := func([]Finding) func(Finding) Decoration { return func(Finding) Decoration { return Decoration{} } }
				FileButlerFindings(fc.AsIssueFiler(), "1", twoIntents(), 0, plan)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.meta == (doctor.LabelMeta{}) {
				t.Fatalf("no label metadata for %q", tc.label)
			}
			fc := tc.newFC()
			fc.RequireLabelsExist = true
			fc.PostIssueURL = "https://github.com/owner/repo/issues/99"

			tc.file(fc)

			if len(fc.PostIssueCalls) != 2 {
				t.Fatalf("PostIssueCalls = %+v, want both filed", fc.PostIssueCalls)
			}
			for _, c := range fc.PostIssueCalls {
				if !slices.Contains(c.Labels, tc.label) {
					t.Errorf("PostIssue labels = %v, want to include %q", c.Labels, tc.label)
				}
			}
			want := []forge.CreateLabelCall{{Name: tc.label, Description: tc.meta.Description, Color: tc.meta.Color}}
			if !slices.Equal(fc.CreateLabelCalls, want) {
				t.Errorf("CreateLabelCalls = %+v, want exactly %+v", fc.CreateLabelCalls, want)
			}
		})
	}
}

// The butler patch path adds agent-butler-patch as an extra label; it needs the
// same ensure-create as the provenance label.
func TestFileButlerFindings_EnsuresExtraLabels(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.RequireLabelsExist = true
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"
	patchLabel := "agent-butler-patch"

	plan := func([]Finding) func(Finding) Decoration {
		return func(Finding) Decoration { return Decoration{ExtraLabels: []string{patchLabel}} }
	}
	filed, _, _ := FileButlerFindings(fc.AsIssueFiler(), "1", twoIntents(), 0, plan)

	if len(filed) != 2 {
		t.Fatalf("filed = %v, want 2", filed)
	}
	want := []string{dispatchkind.Butler.FindingLabel, patchLabel}
	if got := createdNames(fc); !slices.Equal(got, want) {
		t.Errorf("created labels = %v, want %v", got, want)
	}
	meta := doctor.TriageLabelMeta[patchLabel]
	if c := fc.CreateLabelCalls[1]; c.Description != meta.Description || c.Color != meta.Color {
		t.Errorf("CreateLabelCalls[1] = %+v, want meta %+v", c, meta)
	}
	for _, c := range fc.PostIssueCalls {
		if !slices.Contains(c.Labels, patchLabel) {
			t.Errorf("PostIssue labels = %v, want to include %q", c.Labels, patchLabel)
		}
	}
}

// An existing label is never re-created.
func TestFileIssueIntentsDetailed_ExistingProvenanceLabelNotRecreated(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.RequireLabelsExist = true
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"
	fc.Labels = []string{dispatchkind.Research.FindingLabel}

	fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", twoIntents(), dispatchkind.Research.FindingLabel, "")

	if len(fc.CreateLabelCalls) != 0 {
		t.Errorf("CreateLabelCalls = %+v, want none", fc.CreateLabelCalls)
	}
}

// A failed create the re-list cannot rescue must not drop the provenance label
// (that would defeat closed-provenance dedup suppression): the label stays
// requested, PostIssue fails, and the filing counts as failed.
func TestFileIssueIntentsDetailed_ProvenanceCreateFailureKeepsLabelAndFailsFiling(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.RequireLabelsExist = true
	fc.CreateLabelErr = errFake
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"

	detailed := fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", twoIntents(), dispatchkind.Butler.FindingLabel, "")

	if tally := tallyFiled(detailed); tally.failed != 2 || tally.ok != 0 {
		t.Fatalf("tally = %v, want both failed", tally)
	}
	if len(fc.PostIssueCalls) != 2 {
		t.Fatalf("PostIssueCalls = %+v, want 2", fc.PostIssueCalls)
	}
	for _, c := range fc.PostIssueCalls {
		if len(c.Labels) == 0 || c.Labels[0] != dispatchkind.Butler.FindingLabel {
			t.Errorf("PostIssue labels = %v, want provenance label first", c.Labels)
		}
	}
}

// A provenance label with no known metadata passes through untouched, as before.
func TestFileIssueIntentsDetailed_UnknownProvenanceLabelNotCreated(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	fc.PostIssueURL = "https://github.com/owner/repo/issues/99"

	fileIssueIntentsDetailed(fc.AsIssueFiler(), "1", twoIntents(), "custom-provenance", "")

	if len(fc.CreateLabelCalls) != 0 {
		t.Errorf("CreateLabelCalls = %+v, want none", fc.CreateLabelCalls)
	}
	if len(fc.PostIssueCalls) != 2 || fc.PostIssueCalls[0].Labels[0] != "custom-provenance" {
		t.Errorf("PostIssueCalls = %+v", fc.PostIssueCalls)
	}
}
