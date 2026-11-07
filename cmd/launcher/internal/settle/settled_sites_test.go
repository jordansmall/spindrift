package settle

import (
	"errors"
	"os"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/outcome"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/testutil"
)

// stampedFake returns a Fake Dispatcher whose primary log opens with a
// dispatch_start stamp for recordID.
func stampedFake(t *testing.T, recordID string) (*dispatch.Fake, string) {
	t.Helper()
	path := rootLogPath(t, "issue-1.log")
	if err := os.WriteFile(path, []byte(startLine(recordID, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	d := dispatch.NewFake()
	d.LogPathResult = path
	d.RecordIDResult = recordID
	return d, path
}

func assertOneSettled(t *testing.T, path, recordID, state, reason string, recs []report.Record) {
	t.Helper()
	stored := logRecords(t, path)
	if len(stored) != 1 || stored[0].ID != recordID || stored[0].Outcome != state || stored[0].Reason != reason || stored[0].OutcomeSource != dispatchrecord.OutcomeSourceSettled {
		t.Fatalf("stored records = %+v, want one %s/%s settled for %s", stored, state, reason, recordID)
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

	NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels, false, testResearchFindingLabel).Settle(d, "42", 0, result)

	assertOneSettled(t, path, "research:42@x", "complete", "verdict", read())
}

func TestResearchSettle_FailAppendsSettled(t *testing.T) {
	read := testutil.InstallPipeReporter(t)
	fc := newResearchFake("42")
	d, path := stampedFake(t, "research:42@x")

	NewResearchSettle(fc.AsNoLandingRecorder(), researchVerdictLabels, false, testResearchFindingLabel).Settle(d, "42", 0, dispatch.Result{})

	assertOneSettled(t, path, "research:42@x", "failed", "research-failed", read())
}

// A nil Dispatcher (the butler patch gate) has no Record: even when the
// primary log opens with a stamp, which may name an unrelated earlier Record,
// the settle leaves no Record in the store.
func TestSettleAdopted_NilDispatcherSettlesNoRecord(t *testing.T) {
	c := baseConfig()
	_, path := stampedFake(t, "work:1@old")
	c.LogPath = func(string) string { return path }
	fc := forge.NewFake(testDispatchLabels)
	fc.SetIssue(forge.Issue{Number: "1", Labels: []string{"agent-in-progress"}})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateFailure})
	s := newTestSettle(c, fc, fc)

	testutil.CaptureStdout(t, func() { s.SettleAdopted(nil, "1", 0, testPR) })

	if recs := logRecords(t, path); len(recs) != 0 {
		t.Errorf("stored records = %+v, want none for a nil Dispatcher", recs)
	}
}

func flushLatched(t *testing.T, fc *forge.Fake, reason string) []dispatchrecord.Record {
	t.Helper()
	c := baseConfig()
	d, path := stampedFake(t, "work:9@x")
	c.LogPath = func(string) string { return path }
	fc.SetIssue(forge.Issue{Number: "9", Labels: []string{"agent-in-progress"}})
	s := newTestSettle(c, fc, fc)
	s.latchPR("9", testPR)
	testutil.CaptureStdout(t, func() {
		s.transitionState("9", ForReason(reason), "")
		s.flushSettled(d, "9")
	})
	return logRecords(t, path)
}

func TestFlushSettled_MergedCarriesMergeCommit(t *testing.T) {
	fc := forge.NewFake()
	fc.SetMergeCommit(testPR, "deadbeef")
	records := flushLatched(t, fc, dispatchrecord.ReasonMerged)
	if len(records) != 1 || records[0].MergeCommit != "deadbeef" {
		t.Fatalf("records = %+v, want one carrying merge_commit deadbeef", records)
	}
}

func TestFlushSettled_NotMergedOmitsMergeCommit(t *testing.T) {
	fc := forge.NewFake()
	fc.SetMergeCommit(testPR, "deadbeef")
	records := flushLatched(t, fc, dispatchrecord.ReasonCIRed)
	if len(records) != 1 || records[0].MergeCommit != "" {
		t.Fatalf("records = %+v, want one without a merge commit", records)
	}
}

// A failed merge-commit read warns and leaves the field empty, but the settled
// op still lands with the unchanged outcome.
func TestFlushSettled_MergeCommitReadErrorKeepsOutcome(t *testing.T) {
	fc := forge.NewFake()
	fc.MergeCommitErr = errors.New("boom")
	var records []dispatchrecord.Record
	stderr := testutil.CaptureStderr(t, func() { records = flushLatched(t, fc, dispatchrecord.ReasonMerged) })
	if len(records) != 1 || records[0].Outcome != "complete" || records[0].Reason != dispatchrecord.ReasonMerged || records[0].MergeCommit != "" {
		t.Fatalf("records = %+v, want one complete/merged without a merge commit", records)
	}
	if !strings.Contains(stderr, "read merge commit of "+testPR) || !strings.Contains(stderr, "boom") {
		t.Fatalf("stderr = %q, want the read-merge-commit warning", stderr)
	}
}
