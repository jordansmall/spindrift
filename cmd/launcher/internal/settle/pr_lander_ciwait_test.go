package settle

import (
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/report"
)

// ciWaitCases are the rows where the CI wait the host enters announces itself:
// one ci_wait record per wait, carrying the PR and, on a run URL-capable
// forge, the run URL as it appears, changes or disappears, so the daemon's
// dashboard can show "waiting on CI".
func ciWaitCases() []prCase {
	const runA, runB = "https://example.test/actions/runs/1", "https://example.test/actions/runs/2"
	merged := prSettlement(dispatchrecord.ReasonMerged)
	complete := []string{"agent-complete"}
	flipThenMerge := []string{"MarkReady:" + testPR, "Merge:" + testPR}
	rebased := []string{"MarkReady:" + testPR, "Merge:" + testPR, "MarkDraft:" + testPR, "MarkReady:" + testPR, "Merge:" + testPR}
	three := []forge.RollupState{forge.StatePending, forge.StatePending, forge.StatePending, forge.StateSuccess, forge.StateSuccess}

	waits := func(c prCase) prCase {
		c.maxFix = 3
		c.want, c.wantLabels, c.wantMerged = merged, complete, testPR
		if c.wantCalls == nil {
			c.wantCalls = flipThenMerge
		}
		return c
	}
	return []prCase{
		waits(prCase{
			name: "an initial green wait announces itself once", checks: []forge.RollupState{green, green}, wantCIWaits: []string{""},
		}),
		waits(prCase{
			name: "the wait after a fix pass announces itself again", checks: []forge.RollupState{red, green, green},
			wantCIWaits: []string{"", ""}, wantFixes: 1,
		}),
		waits(prCase{
			name: "the re-wait after a force-push announces itself again", checks: []forge.RollupState{green, green, green, green},
			maxRebase: 1, mergeErrs: []error{forge.ErrMergeConflict, nil}, wantCalls: rebased, wantRebases: 1, wantCIWaits: []string{"", ""},
		}),
		waits(prCase{
			name: "a run URL that appears and then changes re-announces the wait", checks: three,
			runURLs: []string{"", runA, runA, runB, runB}, wantCIWaits: []string{"", runA, runB},
		}),
		waits(prCase{
			name: "a run URL that appears and then disappears re-announces the wait", checks: three,
			runURLs: []string{"", runA, "", "", ""}, wantCIWaits: []string{"", runA, ""},
		}),
		waits(prCase{
			name:    "a run URL that never appears announces the wait once",
			checks:  []forge.RollupState{forge.StatePending, forge.StatePending, green, green},
			runURLs: []string{"", "", "", ""}, wantCIWaits: []string{""},
		}),
	}
}

// withoutCIWait drops the ci_wait progress records, for tests that assert on
// the settled record a run ends with.
func withoutCIWait(recs []report.Record) []report.Record {
	var out []report.Record
	for _, r := range recs {
		if r.Event != report.EventCIWait {
			out = append(out, r)
		}
	}
	return out
}
