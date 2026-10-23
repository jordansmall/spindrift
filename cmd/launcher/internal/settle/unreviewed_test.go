package settle

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
	"spindrift.dev/launcher/internal/testutil"
)

func implementReadyPasses() []passmanifest.Entry {
	return []passmanifest.Entry{{Pass: 1, Kind: "implement", OutcomeFound: true}}
}

// Issue #5973: a status=ready that came from an implement pass never cleared
// review, so settle fails it with its own reason and lands nothing.
func TestSettle_ReadyFromUnreviewedPass_FailsUnreviewedAndLandsNothing(t *testing.T) {
	fc := gateForge(forge.StateSuccess, 20)
	d, path := stampedFake(t, "work:1@x")
	result := readyResult()
	result.Passes = implementReadyPasses()
	c := baseConfig()
	c.LogPath = func(string) string { return path }
	c.DispatchLabel = "my-ready"
	s := newTestSettle(c, fc, fc)

	out := testutil.CaptureStdout(t, func() { s.Settle(d, gateIssue, 0, result) })

	if ops := settledOps(t, path); len(ops) != 1 || ops[0].State != "failed" || ops[0].Reason != ReasonUnreviewed {
		t.Fatalf("settled ops = %+v, want one failed/%s", ops, ReasonUnreviewed)
	}
	assertOutputHas(t, out, "status=failed")
	iss, _ := fc.Issue(gateIssue)
	if !containsLabel(iss.Labels, "agent-failed") || containsLabel(iss.Labels, "agent-complete") {
		t.Errorf("want agent-failed only; labels=%v", iss.Labels)
	}
	if fc.Merged != "" || len(fc.MarkReadyCalls) != 0 {
		t.Errorf("nothing may land; Merged=%q MarkReadyCalls=%v", fc.Merged, fc.MarkReadyCalls)
	}
	var found bool
	for _, c := range fc.CommentCalls {
		if strings.Contains(c.Body, "other than its land pass") && strings.Contains(c.Body, "never reviewed and landed") && strings.Contains(c.Body, "`my-ready`") && !strings.Contains(c.Body, "never cleared review") {
			found = true
		}
	}
	if !found {
		t.Errorf("want a comment naming the pass and the configured dispatch label; got %+v", fc.CommentCalls)
	}
}

func TestSettle_ReadyFromLandPass_StillLands(t *testing.T) {
	fc := gateForge(forge.StateSuccess, 20)
	result := readyResult()
	result.Passes = []passmanifest.Entry{
		{Pass: 1, Kind: "implement", OutcomeFound: false},
		{Pass: 2, Kind: "review", Verdict: "APPROVE"},
		{Pass: 3, Kind: "land", OutcomeFound: true},
	}
	s := newTestSettle(baseConfig(), fc, fc)

	testutil.CaptureStdout(t, func() { s.Settle(dispatch.NewFake(), gateIssue, 0, result) })

	if fc.Merged != testPR {
		t.Errorf("a land-pass ready must merge; Merged=%q", fc.Merged)
	}
	iss, _ := fc.Issue(gateIssue)
	if containsLabel(iss.Labels, "agent-failed") {
		t.Errorf("must not carry agent-failed; labels=%v", iss.Labels)
	}
}

func TestSettle_ReadyFromLegacyPass_StillLands(t *testing.T) {
	fc := gateForge(forge.StateSuccess, 20)
	result := readyResult()
	result.Passes = []passmanifest.Entry{{Pass: 1, Kind: "legacy", OutcomeFound: true}}
	s := newTestSettle(baseConfig(), fc, fc)

	testutil.CaptureStdout(t, func() { s.Settle(dispatch.NewFake(), gateIssue, 0, result) })

	if fc.Merged != testPR {
		t.Errorf("a legacy-loop ready must merge; Merged=%q", fc.Merged)
	}
}

func TestSituationFor_UnreviewedNeedsSuccessSelfReportAndUnreviewedPasses(t *testing.T) {
	s := newTestSettle(baseConfig(), forge.NewFake(testDispatchLabels), forge.NewFake(testDispatchLabels))
	ready := outcome.Resolved{SelfReportFound: true, SelfReport: outcome.SelfReport{Status: outcome.StatusReady}}
	cases := []struct {
		name   string
		result dispatch.Result
		want   bool
	}{
		{"success with implement source", dispatch.Result{Resolved: ready, Passes: implementReadyPasses()}, true},
		{"success with land source", dispatch.Result{Resolved: ready, Passes: []passmanifest.Entry{{Pass: 1, Kind: "land", OutcomeFound: true}}}, false},
		{"success without passes", dispatch.Result{Resolved: ready}, false},
		{"no success self-report", dispatch.Result{Passes: implementReadyPasses()}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.situationFor("1", false, tc.result).Unreviewed; got != tc.want {
				t.Errorf("Unreviewed = %v, want %v", got, tc.want)
			}
		})
	}
}

// A crashed later pass behind a stale implement-pass ready is not adoption
// evidence: settle fails it as unreviewed rather than adopting the branch
// (issue #5973). The cases differ only in how the Box's run ended: a synthetic
// blocked backstop, or no outcome line at all.
func TestSettle_GithubReadOnly_UnreviewedSelfReportDoesNotAdopt(t *testing.T) {
	const issNum = "5973"
	const prURL = "https://github.com/owner/repo/pull/5973"
	ready := outcome.SelfReport{Status: outcome.StatusReady}
	cases := []struct {
		name   string
		result dispatch.Result
	}{
		{"synthetic-blocked", dispatch.Result{
			Resolved: outcome.Resolved{
				Found:      true,
				Provenance: outcome.ProvenanceSynthetic,
				Outcome: outcome.Outcome{
					Issue: issNum, Landing: "agent/issue-" + issNum, Status: "blocked", Synthetic: true,
					Note: "driver exited without emitting an outcome",
				},
				SelfReportFound: true,
				SelfReport:      ready,
			},
			Passes: implementReadyPasses(),
		}},
		{"no-outcome", dispatch.Result{
			Resolved:      outcome.Resolved{SelfReportFound: true, SelfReport: ready},
			PRIntent:      "feat: add widget\n\nAdds a widget.",
			PRIntentFound: true,
			Passes:        implementReadyPasses(),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := forge.NewFake(testDispatchLabels)
			fc.BranchPrefix = "agent/issue-"
			fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
			fc.CreateDraftPRURL = prURL
			fc.SetCheckStates(prURL, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
			d, path := stampedFake(t, "work:5973@x")
			c := baseConfig()
			c.ReadOnly = true
			c.LogPath = func(string) string { return path }
			c.OutboxDir = func(num string) string { return "/outbox/" + num }
			c.BaseBranch = "main"
			s := newTestSettle(c, fc.AsNoLandingRecorder(), fc.AsGithubReadOnly())

			testutil.CaptureStdout(t, func() { s.Settle(d, issNum, 0, tc.result) })

			if ops := settledOps(t, path); len(ops) != 1 || ops[0].State != "failed" || ops[0].Reason != ReasonUnreviewed {
				t.Fatalf("settled ops = %+v, want one failed/%s", ops, ReasonUnreviewed)
			}
			if len(fc.RelayBundleCalls) != 0 || len(fc.CreateDraftPRCalls) != 0 || fc.Merged != "" {
				t.Errorf("must not adopt; RelayBundleCalls=%+v CreateDraftPRCalls=%+v Merged=%q", fc.RelayBundleCalls, fc.CreateDraftPRCalls, fc.Merged)
			}
			iss, _ := fc.Issue(issNum)
			if !containsLabel(iss.Labels, "agent-failed") || containsLabel(iss.Labels, "agent-complete") {
				t.Errorf("want agent-failed and not agent-complete; labels=%v", iss.Labels)
			}
		})
	}
}

// recover declines an unreviewed ready on both landing shapes (issue #5973):
// the PR-shaped adopt and the push-only landing that a bundle alone justifies.
func TestSettle_SettleRelayedBranch_UnreviewedDeclines(t *testing.T) {
	const issNum = "5973"
	cases := []struct {
		name  string
		build func(fc *forge.Fake) (forge.IssueTracker, forge.CodeForge)
	}{
		{"pr-shaped", func(fc *forge.Fake) (forge.IssueTracker, forge.CodeForge) {
			return fc.AsNoLandingRecorder(), fc.AsGithubReadOnly()
		}},
		{"push-only", func(fc *forge.Fake) (forge.IssueTracker, forge.CodeForge) {
			return fc.AsLocalShaped(), fc.AsLocal()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := forge.NewFake(testDispatchLabels)
			fc.BranchPrefix = "agent/issue-"
			fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-failed"}})
			c := baseConfig()
			c.OutboxDir = func(num string) string { return "/outbox/" + num }
			tr, cf := tc.build(fc)
			s := newTestSettle(c, tr, cf)

			result := dispatch.Result{
				Resolved: outcome.Resolved{
					SelfReportFound: true,
					SelfReport:      outcome.SelfReport{Status: outcome.StatusReady},
				},
				Passes: implementReadyPasses(),
			}
			sit := s.situationFor(issNum, false, result)
			sit.BundlePresent = true
			if !sit.Unreviewed {
				t.Fatal("fixture must be unreviewed")
			}
			got, err := s.SettleRelayedBranch(dispatch.NewFake(), issNum, 0, sit, result)
			if got || err != nil {
				t.Fatalf("SettleRelayedBranch = %v, %v; want false, nil", got, err)
			}
			if len(fc.RelayBundleCalls) != 0 || len(fc.CreateDraftPRCalls) != 0 || len(fc.RecordLandingCalls) != 0 || fc.Merged != "" {
				t.Errorf("nothing may land; relay=%+v draft=%+v landing=%+v merged=%q",
					fc.RelayBundleCalls, fc.CreateDraftPRCalls, fc.RecordLandingCalls, fc.Merged)
			}
		})
	}
}

// CODE_FORGE=local push-only: a stale implement-pass ready, then a crashed
// later pass leaving a bundle, must not be promoted to Recoverable, because
// `spindrift recover` would decline it (issue #5973).
func TestSettle_LocalPushOnly_UnreviewedBundlePresentFailsUnreviewed(t *testing.T) {
	const issNum = "5973"
	ready := outcome.SelfReport{Status: outcome.StatusReady}
	cases := []struct {
		name   string
		result dispatch.Result
	}{
		{"synthetic-blocked", dispatch.Result{
			Resolved: outcome.Resolved{
				Found:      true,
				Provenance: outcome.ProvenanceSynthetic,
				Outcome: outcome.Outcome{
					Issue: issNum, Status: "blocked", Synthetic: true,
					Note: "driver exited without emitting an outcome",
				},
				SelfReportFound: true,
				SelfReport:      ready,
			},
			Passes: implementReadyPasses(),
		}},
		{"no-outcome", dispatch.Result{
			Resolved: outcome.Resolved{SelfReportFound: true, SelfReport: ready},
			Passes:   implementReadyPasses(),
		}},
		{"killed-by-signal", dispatch.Result{
			KilledBySignal: true,
			Resolved:       outcome.Resolved{SelfReportFound: true, SelfReport: ready},
			Passes:         implementReadyPasses(),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outbox := t.TempDir()
			writeBundle(t, outbox)
			fc := forge.NewFake(testDispatchLabels)
			fc.BranchPrefix = "agent/issue-"
			fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})
			d, path := stampedFake(t, "work:5973@x")
			c := baseConfig()
			c.LogPath = func(string) string { return path }
			c.OutboxDir = func(string) string { return outbox }
			s := newTestSettle(c, fc, fc.AsLocal())

			testutil.CaptureStdout(t, func() { s.Settle(d, issNum, 0, tc.result) })

			if ops := settledOps(t, path); len(ops) != 1 || ops[0].State != "failed" || ops[0].Reason != ReasonUnreviewed {
				t.Fatalf("settled ops = %+v, want one failed/%s", ops, ReasonUnreviewed)
			}
			for _, call := range fc.TransitionStateCalls {
				if call.To == forge.Recoverable {
					t.Errorf("must not promote to Recoverable; calls=%+v", fc.TransitionStateCalls)
				}
			}
		})
	}
}
