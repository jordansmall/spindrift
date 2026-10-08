package settle

import (
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/testutil"
)

// stampedFake returns a Fake Dispatcher whose primary log opens with a
// dispatch_start stamp for recordID.
func stampedFake(t *testing.T, recordID string) (*dispatch.Fake, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "issue.log")
	stamp := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchStart, Start: &claude.DispatchStart{RecordID: recordID}})
	if err := os.WriteFile(path, []byte(stamp), 0o644); err != nil {
		t.Fatal(err)
	}
	d := dispatch.NewFake()
	d.LogPathResult = path
	d.RecordIDResult = recordID
	return d, path
}

func assertOneSettled(t *testing.T, path, recordID, state, reason string, recs []report.Record) {
	t.Helper()
	ops := settledOps(t, path)
	if len(ops) != 1 || ops[0].RecordID != recordID || ops[0].State != state || ops[0].Reason != reason {
		t.Fatalf("settled ops = %+v, want one %s/%s for %s", ops, state, reason, recordID)
	}
	if len(recs) != 1 || recs[0].Event != report.EventSettled || recs[0].RecordID != recordID || recs[0].State != state {
		t.Fatalf("reports = %+v, want one settled carrying record_id %s", recs, recordID)
	}
}

func TestResearchSettle_VerdictAppendsSettled(t *testing.T) {
	read := testutil.InstallPipeReporter(t)
	fc := newResearchFake("42")
	d, path := stampedFake(t, "research:42@x")
	result := dispatch.Result{Resolved: outcome.Resolved{Found: true, Outcome: outcome.Outcome{Issue: "42", Status: "recommend"}}}

	NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels, false).Settle(d, "42", 0, result)

	assertOneSettled(t, path, "research:42@x", "complete", "verdict", read())
}

func TestResearchSettle_FailAppendsSettled(t *testing.T) {
	read := testutil.InstallPipeReporter(t)
	fc := newResearchFake("42")
	d, path := stampedFake(t, "research:42@x")

	NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels, false).Settle(d, "42", 0, dispatch.Result{})

	assertOneSettled(t, path, "research:42@x", "failed", "research-failed", read())
}

// A nil Dispatcher (the butler patch gate) has no Record: even when the
// primary log opens with a stamp, which may name an unrelated earlier Record,
// the settle appends nothing.
func TestSettleAdopted_NilDispatcherAppendsNothing(t *testing.T) {
	c := baseConfig()
	_, path := stampedFake(t, "work:1@old")
	c.LogPath = func(string) string { return path }
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	s := newTestSettle(c, fc, fc)

	testutil.CaptureStdout(t, func() { s.SettleAdopted(nil, "1", 0, testPR) })

	if ops := settledOps(t, path); len(ops) != 0 {
		t.Errorf("settled ops = %+v, want none for a nil Dispatcher", ops)
	}
}
