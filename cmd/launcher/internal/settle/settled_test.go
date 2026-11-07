package settle

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/seamtest"
	"spindrift.dev/launcher/internal/testutil"
)

// rootLogPath is a log path inside a fresh root's log directory, where Settled
// ingests into the root's store.
func rootLogPath(t *testing.T, name string) string {
	t.Helper()
	dir := hostpaths.LogDir(t.TempDir())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, name)
}

// logRecords is what the store of the root owning the log at path holds, read
// without ingesting: a Record is there only if Settled put it there.
func logRecords(t *testing.T, path string) []dispatchrecord.Record {
	t.Helper()
	return storedRecords(t, logRoot(t, path))
}

func logRoot(t *testing.T, path string) string {
	t.Helper()
	root, ok := hostpaths.RootOfLogDir(filepath.Dir(path))
	if !ok {
		t.Fatalf("%s is not in a root's log directory", path)
	}
	return root
}

// startLine is a dispatch_start stamp for recordID (<kind>:<key>@<suffix>),
// enough for the log to parse into a Record.
func startLine(recordID, hostToken string) string {
	kind, rest, _ := strings.Cut(recordID, ":")
	key, _, _ := strings.Cut(rest, "@")
	return hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpDispatchStart, Start: &hostop.DispatchStart{
		RecordID: recordID, Kind: kind, DispatchKey: key, HostToken: hostToken,
	}})
}

// A Dispatch that never Ran (recover's, an adopt's) has no Record ID of its
// own; SettledPrior takes it from the stamp of the prior Dispatch's log, and
// the report carries the same ID.
func TestSettledPrior_FillsRecordIDFromLogStamp(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)
	path := rootLogPath(t, "issue-7.log")
	if err := os.WriteFile(path, []byte(startLine("work:7@x", "")), 0o644); err != nil {
		t.Fatal(err)
	}

	SettledPrior(dispatchkey.Issue("7"), path, hostop.DispatchSettled{State: "complete", Reason: "merged", PRURL: "https://x/pr/1"})

	recs := logRecords(t, path)
	if len(recs) != 1 || recs[0].ID != "work:7@x" || recs[0].Outcome != "complete" || recs[0].PRURL != "https://x/pr/1" ||
		recs[0].OutcomeSource != dispatchrecord.OutcomeSourceSettled {
		t.Fatalf("stored records = %+v", recs)
	}
	if got, _ := dispatchrecord.ReadStamp(path); got.RecordID != "work:7@x" {
		t.Errorf("stamp = %q, log no longer opens with it", got.RecordID)
	}
	reports := readRecords()
	if len(reports) != 1 || reports[0].Event != report.EventSettled || reports[0].RecordID != "work:7@x" {
		t.Errorf("reports = %+v, want one settled with record_id work:7@x", reports)
	}
}

// A Dispatch whose Run failed before minting a Record ID must not settle the
// earlier Dispatch's Record that still heads the same issue log: last op wins
// on re-ingest, so doing so would rewrite a landed Record as failed.
func TestSettledBy_EmptyRecordIDLeavesEarlierRecordUntouched(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)
	path := rootLogPath(t, "issue-7.log")
	settled := hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpDispatchSettled, Settled: &hostop.DispatchSettled{RecordID: "work:7@x", State: "complete", Reason: "merged"}})
	if err := os.WriteFile(path, []byte(startLine("work:7@x", "")+settled), 0o644); err != nil {
		t.Fatal(err)
	}
	storedRecordsByID(t, logRoot(t, path)) // ingest the landed Record

	SettledBy(&dispatch.Fake{LogPathResult: path}, dispatchkey.Issue("7"), "failed", ReasonBoxFailed, "claim failed")

	recs := logRecords(t, path)
	if len(recs) != 1 || recs[0].ID != "work:7@x" || recs[0].Outcome != "complete" || recs[0].Reason != "merged" {
		t.Errorf("stored records = %+v, want the earlier complete/merged Record", recs)
	}
	if reports := readRecords(); len(reports) != 1 || reports[0].RecordID != "" {
		t.Errorf("reports = %+v, want one settled with no record_id", reports)
	}
}

func TestSettledBy_AppendsUnderDispatchRecordID(t *testing.T) {
	testutil.InstallPipeReporter(t)
	path := rootLogPath(t, "issue-7.log")
	if err := os.WriteFile(path, []byte(startLine("work:7@y", "")), 0o644); err != nil {
		t.Fatal(err)
	}

	SettledBy(&dispatch.Fake{LogPathResult: path, RecordIDResult: "work:7@y"}, dispatchkey.Issue("7"), "failed", ReasonBoxFailed, "boom")

	recs := logRecords(t, path)
	if len(recs) != 1 || recs[0].ID != "work:7@y" || recs[0].Outcome != "failed" || recs[0].Reason != ReasonBoxFailed ||
		recs[0].Note != "boom" || recs[0].OutcomeSource != dispatchrecord.OutcomeSourceSettled {
		t.Fatalf("stored records = %+v, want work:7@y settled failed/%s with note boom", recs, ReasonBoxFailed)
	}
}

// A bare Settled never reaches for the log's stamp: the explicit Record ID
// stands, so the stamp's own Record stays unsettled.
func TestSettled_ExplicitRecordIDWinsOverStamp(t *testing.T) {
	testutil.InstallPipeReporter(t)
	path := rootLogPath(t, "issue-7.log")
	if err := os.WriteFile(path, []byte(startLine("work:7@x", "")), 0o644); err != nil {
		t.Fatal(err)
	}

	Settled(dispatchkey.Issue("7"), path, hostop.DispatchSettled{RecordID: "work:7@y", State: "failed"})

	op := hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpDispatchSettled, Settled: &hostop.DispatchSettled{RecordID: "work:7@y", State: "failed"}})
	if body, err := os.ReadFile(path); err != nil || !strings.HasSuffix(string(body), op) {
		t.Errorf("log = %q (err %v), want it to end with the settled op for work:7@y", body, err)
	}
	recs := logRecords(t, path)
	if len(recs) != 1 || recs[0].ID != "work:7@x" || recs[0].OutcomeSource == dispatchrecord.OutcomeSourceSettled {
		t.Fatalf("stored records = %+v, want only the stamp's Record work:7@x, unsettled", recs)
	}
}

// A missing log means the Dispatch has no Record to settle, but the report
// still goes out.
func TestSettled_MissingLogStillReports(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)
	path := rootLogPath(t, "issue-7.log")

	Settled(dispatchkey.Issue("7"), path, hostop.DispatchSettled{RecordID: "work:7@x", State: "failed"})
	Settled(dispatchkey.Issue("7"), "", hostop.DispatchSettled{State: "failed"})

	if recs := readRecords(); len(recs) != 2 {
		t.Errorf("reports = %+v, want 2", recs)
	}
}

// An unstamped log yields no Record ID, so nothing settles and the store holds
// no Record.
func TestSettled_UnstampedLogSettlesNothing(t *testing.T) {
	testutil.InstallPipeReporter(t)
	path := rootLogPath(t, "issue-7.log")
	if err := os.WriteFile(path, []byte(seamtest.Line(driverkit.Event{Kind: driverkit.KindText})), 0o644); err != nil {
		t.Fatal(err)
	}

	SettledPrior(dispatchkey.Issue("7"), path, hostop.DispatchSettled{State: "failed"})

	if recs := logRecords(t, path); len(recs) != 0 {
		t.Fatalf("stored records = %+v, want none", recs)
	}
}

// A settled op copies the host token of the stamp whose Record it settles, so a
// tokened stamp's settled outcome survives ParseLog (issue #4812), whether the
// Record ID is filled in from the stamp or given explicitly (the adopt path); a
// token-less legacy stamp still settles.
func TestSettled_EchoesStampHostToken(t *testing.T) {
	for _, token := range []string{"tok", ""} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("token=%s/explicit=%t", token, explicit), func(t *testing.T) {
				testutil.InstallPipeReporter(t)
				path := rootLogPath(t, "issue-7.log")
				if err := os.WriteFile(path, []byte(startLine("work:7@x", token)), 0o644); err != nil {
					t.Fatal(err)
				}

				ds := hostop.DispatchSettled{State: "complete", Reason: "merged"}
				if explicit {
					ds.RecordID = "work:7@x"
					Settled(dispatchkey.Issue("7"), path, ds)
				} else {
					SettledPrior(dispatchkey.Issue("7"), path, ds)
				}

				recs := logRecords(t, path)
				if len(recs) != 1 || recs[0].OutcomeSource != dispatchrecord.OutcomeSourceSettled || recs[0].Outcome != "complete" {
					t.Fatalf("stored records = %+v, want one settled complete", recs)
				}
			})
		}
	}
}

// A real stamp carries the whole knob set and can pass 4 KiB; SettledPrior must
// still read its Record ID from the head of the log.
func TestSettledPrior_ReadsStampLongerThanAReadBuffer(t *testing.T) {
	testutil.InstallPipeReporter(t)
	path := rootLogPath(t, "issue-7.log")
	stamp := hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpDispatchStart, Start: &hostop.DispatchStart{
		RecordID: "work:7@x", Kind: "work", DispatchKey: "7",
		Knobs: map[string]string{"MERGE_GUARD_PATHS": strings.Repeat("a/b ", 1400)},
	}})
	if len(stamp) <= 4096 {
		t.Fatalf("stamp is %d bytes, want over 4096", len(stamp))
	}
	if err := os.WriteFile(path, []byte(stamp), 0o644); err != nil {
		t.Fatal(err)
	}

	SettledPrior(dispatchkey.Issue("7"), path, hostop.DispatchSettled{State: "complete", Reason: "merged"})

	if recs := logRecords(t, path); len(recs) != 1 || recs[0].ID != "work:7@x" || recs[0].OutcomeSource != dispatchrecord.OutcomeSourceSettled {
		t.Fatalf("stored records = %+v, want one settled under the stamp's Record ID", recs)
	}
}
