package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
)

// TestSituationFor_BundlePresent covers situationFor's BundlePresent field:
// true when the outbox actually holds the relayable bundle file, false when
// it doesn't (bundlePresent's own plain-stat contract).
func TestSituationFor_BundlePresent(t *testing.T) {
	cases := []struct {
		name       string
		writeIt    bool
		wantResult bool
	}{
		{name: "present", writeIt: true, wantResult: true},
		{name: "absent", writeIt: false, wantResult: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outbox := t.TempDir()
			if tc.writeIt {
				writeBundle(t, outbox)
			}

			fc := forge.NewFake(testDispatchLabels)
			c := baseConfig()
			c.OutboxDir = func(num string) string { return outbox }
			s := newTestSettle(c, fc, fc)

			got := s.situationFor("1", false, dispatch.Result{})
			if got.BundlePresent != tc.wantResult {
				t.Errorf("BundlePresent = %v, want %v", got.BundlePresent, tc.wantResult)
			}
		})
	}
}

// TestSituationFor_SelfReportSuccess covers situationFor's SelfReportSuccess
// field across a genuine success, a genuine non-success, and no self-report
// at all.
func TestSituationFor_SelfReportSuccess(t *testing.T) {
	cases := []struct {
		name   string
		result dispatch.Result
		want   bool
	}{
		{
			// issue #2981: a paraphrasing driver's bare "success" word is not
			// part of the generated status vocabulary (outcome.WorkStatuses),
			// so it no longer counts as a success self-report — only a
			// genuine status=ready does. This regression case used to assert
			// the opposite (issue #2223's near-miss leniency); it now pins
			// the narrower behavior instead of being deleted.
			name: "bare success word alone does not count",
			result: dispatch.Result{Resolved: outcome.Resolved{
				SelfReportFound: true,
				SelfReport:      outcome.SelfReport{Status: "success"},
			}},
			want: false,
		},
		{
			name: "ready counts too",
			result: dispatch.Result{Resolved: outcome.Resolved{
				SelfReportFound: true,
				SelfReport:      outcome.SelfReport{Status: "ready"},
			}},
			want: true,
		},
		{
			name: "non-success status",
			result: dispatch.Result{Resolved: outcome.Resolved{
				SelfReportFound: true,
				SelfReport:      outcome.SelfReport{Status: "blocked"},
			}},
			want: false,
		},
		{
			name:   "absent",
			result: dispatch.Result{Resolved: outcome.Resolved{SelfReportFound: false}},
			want:   false,
		},
	}

	fc := forge.NewFake(testDispatchLabels)
	c := baseConfig()
	c.OutboxDir = func(num string) string { return t.TempDir() }
	s := newTestSettle(c, fc, fc)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.situationFor("1", false, tc.result)
			if got.SelfReportSuccess != tc.want {
				t.Errorf("SelfReportSuccess = %v, want %v", got.SelfReportSuccess, tc.want)
			}
		})
	}
}

// TestSituationFor_OpenPRFoundPassesThrough covers situationFor's
// OpenPRFound field: it is the caller's own supplied fact, passed through
// unchanged rather than derived from anything else.
func TestSituationFor_OpenPRFoundPassesThrough(t *testing.T) {
	fc := forge.NewFake(testDispatchLabels)
	c := baseConfig()
	c.OutboxDir = func(num string) string { return t.TempDir() }
	s := newTestSettle(c, fc, fc)

	if got := s.situationFor("1", true, dispatch.Result{}); !got.OpenPRFound {
		t.Errorf("OpenPRFound = false, want true")
	}
	if got := s.situationFor("1", false, dispatch.Result{}); got.OpenPRFound {
		t.Errorf("OpenPRFound = true, want false")
	}
}

// TestSituationFor_ExportedWrapperMatchesInternal covers SituationFor, the
// exported wrapper main.go (a different package) uses to compute a Situation
// without duplicating situationFor's own logic.
func TestSituationFor_ExportedWrapperMatchesInternal(t *testing.T) {
	outbox := t.TempDir()
	writeBundle(t, outbox)

	fc := forge.NewFake(testDispatchLabels)
	c := baseConfig()
	c.OutboxDir = func(num string) string { return outbox }
	s := newTestSettle(c, fc, fc)

	result := dispatch.Result{Resolved: outcome.Resolved{
		SelfReportFound: true,
		SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
	}}

	want := s.situationFor("1", true, result)
	got := s.SituationFor("1", true, result)
	if got != want {
		t.Errorf("SituationFor = %+v, want %+v", got, want)
	}
}

// TestSettle_SettleRelayedBranch_OpenPRFoundReturnsFalse covers
// SettleRelayedBranch's precondition guard: with an open PR already found on
// num, this function must never run — that's SettleAdopted's job — so it
// returns false with no side effect, even when the rest of the evidence
// (a genuine success self-report) would otherwise adopt.
func TestSettle_SettleRelayedBranch_OpenPRFoundReturnsFalse(t *testing.T) {
	const issNum = "2225"
	const prURL = "https://github.com/owner/repo/pull/2225"

	fc := forge.NewFake(testDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
	fc.CreateDraftPRURL = prURL

	result := dispatch.Result{Resolved: outcome.Resolved{
		SelfReportFound: true,
		SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
	}}

	c := baseConfig()
	c.OutboxDir = func(num string) string { return t.TempDir() }
	c.BaseBranch = "main"
	s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())

	sit := Situation{OpenPRFound: true, SelfReportSuccess: true}
	got := s.SettleRelayedBranch(dispatch.NewFake(), issNum, 0, sit, result)
	if got {
		t.Fatalf("SettleRelayedBranch = true, want false when sit.OpenPRFound is true")
	}
	if len(fc.CreateDraftPRCalls) != 0 {
		t.Errorf("expected no CreateDraftPR calls; got %+v", fc.CreateDraftPRCalls)
	}
	if fc.Merged != "" {
		t.Errorf("expected no merge to have run; fc.Merged=%q", fc.Merged)
	}
}
