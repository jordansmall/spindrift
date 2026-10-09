package settle

import (
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/testutil"
)

// TestSettle_CIWait_EmitsOneRecordPerWaitEntered: each CI wait the host enters
// announces itself once, carrying the PR, so the daemon's dashboard can show
// "waiting on CI".
func TestSettle_CIWait_EmitsOneRecordPerWaitEntered(t *testing.T) {
	cases := []struct {
		name      string
		wantWaits int
		viaMerge  bool
		checks    []forge.RollupState
		mergeErrs []error
	}{
		{
			name:      "initial green wait",
			wantWaits: 1,
			checks:    []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
		},
		{
			name:      "wait after a fix pass",
			wantWaits: 2,
			checks:    []forge.RollupState{forge.StateFailure, forge.StateSuccess, forge.StateSuccess},
		},
		{
			name:      "re-wait after a force-push",
			wantWaits: 1,
			checks:    []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			mergeErrs: []error{forge.ErrMergeConflict, nil},
			viaMerge:  true,
		},
		{
			name:      "initial wait plus re-wait after a force-push",
			wantWaits: 2,
			checks:    []forge.RollupState{forge.StateSuccess, forge.StateSuccess, forge.StateSuccess, forge.StateSuccess},
			mergeErrs: []error{forge.ErrMergeConflict, nil},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			readRecords := testutil.InstallPipeReporter(t)
			c := fixConfig(3)
			c.MergeMode = "immediate"
			c.MaxRebaseAttempts = 1
			fc := forge.NewFake(testDispatchLabels)
			fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
			fc.SetCheckStates(testPR, tc.checks)
			fc.MergeErrs = tc.mergeErrs
			s := newTestSettle(c, fc, fc)

			if tc.viaMerge {
				_ = s.mergeImmediate("1", 0, testPR, nil)
			} else {
				s.selfHeal(dispatch.NewFake(), "1", 0, testPR)
			}

			var waits []report.Record
			for _, r := range readRecords() {
				if r.Event == report.EventCIWait {
					waits = append(waits, r)
				}
			}
			if len(waits) != tc.wantWaits {
				t.Fatalf("ci_wait records = %d, want %d: %+v", len(waits), tc.wantWaits, waits)
			}
			for _, r := range waits {
				if r.Key != dispatchkey.Issue("1") || r.PRURL != testPR {
					t.Errorf("record = %+v, want issue=1 PRURL=%s", r, testPR)
				}
			}
		})
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
