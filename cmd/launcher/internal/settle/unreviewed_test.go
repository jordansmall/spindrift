package settle

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/passmanifest"
)

func TestSituationFor_UnreviewedFollowsSelfReportAndPasses(t *testing.T) {
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
		{"no success self-report, implement last", dispatch.Result{Passes: implementReadyPasses()}, true},
		{"no success self-report, fix last", dispatch.Result{Passes: fixLastPasses()}, true},
		{"no success self-report, land last", dispatch.Result{Passes: landLastNoOutcomePasses()}, false},
		{"no success self-report, no passes", dispatch.Result{}, false},
		{"blocked self-report, implement last", dispatch.Result{Resolved: outcome.Resolved{SelfReportFound: true, SelfReport: outcome.SelfReport{Status: outcome.StatusBlocked}}, Passes: implementReadyPasses()}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.situationFor("1", false, tc.result).Unreviewed; got != tc.want {
				t.Errorf("Unreviewed = %v, want %v", got, tc.want)
			}
		})
	}
}

// pushOnlyUnreviewedCases are the push-only rows (CODE_FORGE=local) where a
// bundle alone does not justify a landing: a stale implement-pass ready, or a
// signal-killed run whose loop stopped before its land pass, fails unreviewed
// instead of promoting to Recoverable, because `spindrift recover` would
// decline it (issues #5973, #5984, #2363).
func pushOnlyUnreviewedCases() []pushOnlyCase {
	unreviewed := Settlement{Verdict: ForReason(dispatchrecord.ReasonUnreviewed), Note: dispatchrecord.UnreviewedNote}
	failed := []string{"agent-failed"}
	ready := outcome.SelfReport{Status: outcome.StatusReady}
	noRecoverable := func(t *testing.T, e *pushOnlyEnv) {
		t.Helper()
		for _, call := range e.fc.TransitionStateCalls {
			if call.To == forge.Recoverable {
				t.Errorf("must not promote to Recoverable; calls=%+v", e.fc.TransitionStateCalls)
			}
		}
	}
	withPassesEnv := func(p []passmanifest.Entry) func(*pushOnlyEnv) {
		return func(e *pushOnlyEnv) { e.passes = p }
	}
	results := []struct {
		name   string
		result dispatch.Result
	}{
		{"synthetic-blocked", dispatch.Result{
			Resolved: outcome.Resolved{
				Found:      true,
				Provenance: outcome.ProvenanceSynthetic,
				Outcome: outcome.Outcome{
					Issue: "1", Status: "blocked", Synthetic: true,
					Note: "driver exited without emitting an outcome",
				},
				SelfReportFound: true,
				SelfReport:      ready,
			},
		}},
		{"no-outcome", dispatch.Result{Resolved: outcome.Resolved{SelfReportFound: true, SelfReport: ready}}},
		{"killed-by-signal", dispatch.Result{KilledBySignal: true, Resolved: outcome.Resolved{SelfReportFound: true, SelfReport: ready}}},
	}

	var cases []pushOnlyCase
	for _, rc := range results {
		res := rc.result
		cases = append(cases, pushOnlyCase{
			name: "local bundle behind an implement-pass ready fails unreviewed: " + rc.name, local: true, bundle: true,
			setup:  withPassesEnv(implementReadyPasses()),
			result: func(*pushOnlyEnv) dispatch.Result { return res },
			want:   unreviewed, wantLabels: failed, wantBundle: bundleKept, check: noRecoverable, wantComments: []string{"stopped before its land pass finished"},
		})
	}

	// A signal-killed local run that never printed a self-report is recoverable
	// only if its loop reached the land pass.
	for _, rc := range []struct {
		name   string
		passes []passmanifest.Entry
	}{
		{"implement-last", []passmanifest.Entry{{Pass: 1, Kind: "implement"}}},
		{"fix-last", fixLastPasses()},
	} {
		cases = append(cases, pushOnlyCase{
			name: "local killed run without a report fails unreviewed: " + rc.name, local: true, bundle: true,
			setup:  withPassesEnv(rc.passes),
			result: func(*pushOnlyEnv) dispatch.Result { return dispatch.Result{KilledBySignal: true} },
			want:   unreviewed, wantLabels: failed, wantBundle: bundleKept, check: noRecoverable, wantComments: []string{"stopped before its land pass finished"},
		})
	}
	cases = append(cases, pushOnlyCase{
		name: "local killed run without a report that reached its land pass is recoverable", local: true, bundle: true,
		setup:  withPassesEnv(landLastNoOutcomePasses()),
		result: func(*pushOnlyEnv) dispatch.Result { return dispatch.Result{KilledBySignal: true} },
		want:   Settlement{Verdict: ForReason(dispatchrecord.ReasonRecoverable), Note: "killed by signal"}, wantBundle: bundleKept,
		check: func(t *testing.T, e *pushOnlyEnv) {
			t.Helper()
			for _, call := range e.fc.TransitionStateCalls {
				if call.To == forge.Recoverable {
					return
				}
			}
			t.Errorf("want Recoverable; calls=%+v", e.fc.TransitionStateCalls)
		},
	})

	// recover declines an unreviewed run on the push-only landing a bundle
	// alone justifies.
	for _, rc := range []struct {
		name   string
		result dispatch.Result
	}{
		{"ready-from-implement", dispatch.Result{Resolved: outcome.Resolved{SelfReportFound: true, SelfReport: ready}}},
		{"no-report-implement-last", dispatch.Result{}},
		{"no-report-fix-last", dispatch.Result{}},
		{"blocked-report-fix-last", dispatch.Result{Resolved: outcome.Resolved{SelfReportFound: true, SelfReport: outcome.SelfReport{Status: outcome.StatusBlocked}}}},
		{"ambiguous-report-implement-last", dispatch.Result{Resolved: outcome.Resolved{SelfReportFound: true, SelfReport: outcome.SelfReport{Status: outcome.StatusAmbiguous}}}},
	} {
		res := rc.result
		passes := implementReadyPasses()
		if strings.Contains(rc.name, "fix-last") {
			passes = fixLastPasses()
		}
		cases = append(cases, pushOnlyCase{
			name: "recover declines an unreviewed local run: " + rc.name, entry: entryRelayed, local: true, bundle: true,
			setup:  withPassesEnv(passes),
			result: func(*pushOnlyEnv) dispatch.Result { return res },
			want:   unreviewed, abandoned: true, declined: true, wantBundle: bundleKept,
			check: func(t *testing.T, e *pushOnlyEnv) {
				t.Helper()
				if len(e.fc.RecordLandingCalls) != 0 {
					t.Errorf("nothing may land; RecordLanding=%+v", e.fc.RecordLandingCalls)
				}
			},
		})
	}
	return cases
}
