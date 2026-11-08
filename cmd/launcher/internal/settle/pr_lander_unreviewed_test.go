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

func implementReadyPasses() []passmanifest.Entry {
	return []passmanifest.Entry{{Pass: 1, Kind: "implement", OutcomeFound: true}}
}

// landLastNoOutcomePasses is the #2363 shape: the loop reached its land pass,
// which died before printing an outcome.
func landLastNoOutcomePasses() []passmanifest.Entry {
	return []passmanifest.Entry{
		{Pass: 1, Kind: "implement", OutcomeFound: true},
		{Pass: 2, Kind: "review", Verdict: "APPROVE"},
		{Pass: 3, Kind: "land", OutcomeFound: false},
	}
}

// fixLastPasses is a loop that stopped in a fix pass, before its land pass.
func fixLastPasses() []passmanifest.Entry {
	return []passmanifest.Entry{
		{Pass: 1, Kind: "implement", OutcomeFound: true},
		{Pass: 2, Kind: "review", Verdict: "REQUEST_CHANGES"},
		{Pass: 3, Kind: "fix"},
	}
}

func withPasses(r dispatch.Result, passes []passmanifest.Entry) *dispatch.Result {
	r.Passes = passes
	return &r
}

// unreviewedComment checks the issue comment that names the pass the loop
// stopped in and the configured dispatch label, and not the old wording.
func unreviewedComment(label string) checkFn {
	return func(t *testing.T, fc *forge.Fake, _ *dispatch.Fake) {
		t.Helper()
		for _, c := range fc.CommentCalls {
			if strings.Contains(c.Body, "stopped before its land pass finished") && strings.Contains(c.Body, "never reviewed and landed") &&
				strings.Contains(c.Body, "`"+label+"`") && !strings.Contains(c.Body, "never cleared review") {
				return
			}
		}
		t.Errorf("want a comment naming the pass and the dispatch label %q; got %+v", label, fc.CommentCalls)
	}
}

// unreviewedCases are the rows where the review loop stopped before its land
// pass finished (issues #5973, #5984): a ready that came from an implement
// pass is failed unreviewed rather than landed or adopted, and recover
// declines it, while a ready from a land or legacy pass still lands.
func unreviewedCases() []prCase {
	g := &handoffRig{}
	unreviewed := Settlement{Verdict: ForReason(dispatchrecord.ReasonUnreviewed), Note: dispatchrecord.UnreviewedNote}
	failed := []string{"agent-failed"}
	flipThenMerge := []string{"MarkReady:" + testPR, "Merge:" + testPR}
	readyOnly := outcome.Resolved{SelfReportFound: true, SelfReport: outcome.SelfReport{Status: outcome.StatusReady}}
	readOnly := g.readOnly(0, nil)
	landedPasses := []passmanifest.Entry{
		{Pass: 1, Kind: "implement", OutcomeFound: false},
		{Pass: 2, Kind: "review", Verdict: "APPROVE"},
		{Pass: 3, Kind: "land", OutcomeFound: true},
	}
	failsUnreviewed := func(c prCase) prCase {
		c.shape, c.noPR = shapeGithubReadOnly, true
		c.setup = readOnly
		c.want, c.wantPRClosed, c.wantLabels, c.wantNoLabels = unreviewed, true, failed, complete
		c.wantComments = []string{"stopped before its land pass finished"}
		c.check = allOf(draftPRCalls(0), relayCalls(0))
		return c
	}
	declines := func(c prCase) prCase {
		c.shape, c.noPR, c.entry, c.want, c.wantAbandoned, c.wantDeclined = shapeGithubReadOnly, true, entryRelayed, unreviewed, true, true
		c.setup = readOnly
		c.check = allOf(draftPRCalls(0), relayCalls(0))
		return c
	}
	declined := []struct {
		name   string
		result dispatch.Result
	}{
		{"ready-from-implement", dispatch.Result{Resolved: readyOnly, Passes: implementReadyPasses()}},
		{"no-report-implement-last", dispatch.Result{Passes: []passmanifest.Entry{{Pass: 1, Kind: "implement"}}}},
		{"no-report-fix-last", dispatch.Result{Passes: fixLastPasses()}},
		{"blocked-report-fix-last", dispatch.Result{
			Resolved: outcome.Resolved{SelfReportFound: true, SelfReport: outcome.SelfReport{Status: outcome.StatusBlocked}},
			Passes:   fixLastPasses(),
		}},
		{"ambiguous-report-implement-last", dispatch.Result{
			Resolved: outcome.Resolved{SelfReportFound: true, SelfReport: outcome.SelfReport{Status: outcome.StatusAmbiguous}},
			Passes:   implementReadyPasses(),
		}},
	}

	cases := []prCase{
		// Issue #5973: a status=ready that came from an implement pass never
		// cleared review, so settle fails it with its own reason and lands nothing.
		{
			name:         "a ready from an implement pass fails unreviewed and lands nothing",
			wantStatuses: []Status{detailOnly, StatusFailed},
			result:       withPasses(settleReadyResult("1", testPR), implementReadyPasses()),
			setup:        func(_ *testing.T, _ *forge.Fake, c *Config) { c.DispatchLabel = "my-ready" },
			want:         unreviewed, wantPRClosed: true, wantLabels: failed, wantNoLabels: complete, wantComments: []string{"stopped before its land pass finished"},
			wantSteps: []Step{stepAt(testPR, StatusFailed, "!! "+dispatchrecord.UnreviewedNote)},
			check:     unreviewedComment("my-ready"),
		},
		{
			name:         "a ready from a land pass still lands",
			wantStatuses: []Status{detailOnly, StatusVerifiedMerged},
			result:       withPasses(settleReadyResult("1", testPR), landedPasses),
			want:         merged, wantLabels: complete, wantNoLabels: failed, wantMerged: testPR, wantCalls: flipThenMerge,
		},
		{
			name:         "a ready from a legacy pass still lands",
			wantStatuses: []Status{detailOnly, StatusVerifiedMerged},
			result:       withPasses(settleReadyResult("1", testPR), []passmanifest.Entry{{Pass: 1, Kind: "legacy", OutcomeFound: true}}),
			want:         merged, wantLabels: complete, wantNoLabels: failed, wantMerged: testPR, wantCalls: flipThenMerge,
		},

		// A crashed later pass behind a stale implement-pass ready is not
		// adoption evidence: settle fails it as unreviewed rather than adopting
		// the branch. The rows differ only in how the Box's run ended: a
		// synthetic blocked backstop, or no outcome line at all.
		failsUnreviewed(prCase{
			name:         "a synthetic-blocked run behind an implement-pass ready does not adopt",
			wantStatuses: []Status{detailOnly, StatusFailed},
			result:       withPasses(*boxRun(true, outcome.ProvenanceSynthetic, outcome.StatusReady, ""), implementReadyPasses()),
		}),
		failsUnreviewed(prCase{
			name:         "a no-outcome run behind an implement-pass ready does not adopt",
			wantStatuses: []Status{StatusFailed},
			result:       withPasses(*boxRun(false, "", outcome.StatusReady, adoptIntent), implementReadyPasses()),
		}),
	}

	// recover declines an unreviewed run, settling it unreviewed without
	// claiming it (issues #5973, #5984): the no-report
	// rows are runs stopped in implement or fix.
	for _, rc := range declined {
		res := rc.result
		cases = append(cases, declines(prCase{name: "recover declines an unreviewed run: " + rc.name, result: &res}))
	}
	return cases
}
