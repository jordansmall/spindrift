package settle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/testutil"
)

func settledOps(t *testing.T, path string) []claude.DispatchSettled {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []claude.DispatchSettled
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, `"`+claude.OpDispatchSettled+`"`) {
			continue
		}
		var ev claude.Event
		if json.Unmarshal([]byte(line), &ev) != nil || ev.SpindriftOp == nil || ev.SpindriftOp.Settled == nil {
			t.Fatalf("unparseable settled line %q", line)
		}
		out = append(out, *ev.SpindriftOp.Settled)
	}
	return out
}

// A Dispatch that never Ran (recover's, an adopt's) has no Record ID of its
// own; SettledPrior takes it from the stamp of the prior Dispatch's log, and
// the report carries the same ID.
func TestSettledPrior_FillsRecordIDFromLogStamp(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)
	path := filepath.Join(t.TempDir(), "issue-7.log")
	stamp := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchStart, Start: &claude.DispatchStart{RecordID: "work:7@x", Kind: "work", DispatchKey: "7"}})
	if err := os.WriteFile(path, []byte(stamp), 0o644); err != nil {
		t.Fatal(err)
	}

	SettledPrior(dispatchkey.Issue("7"), path, claude.DispatchSettled{State: "complete", Reason: "merged", PRURL: "https://x/pr/1"})

	ops := settledOps(t, path)
	if len(ops) != 1 || ops[0].RecordID != "work:7@x" || ops[0].State != "complete" || ops[0].PRURL != "https://x/pr/1" {
		t.Fatalf("settled ops = %+v", ops)
	}
	if got, _ := dispatchrecord.ReadStamp(path); got.RecordID != "work:7@x" {
		t.Errorf("stamp = %q, log no longer opens with it", got.RecordID)
	}
	recs := readRecords()
	if len(recs) != 1 || recs[0].Event != report.EventSettled || recs[0].RecordID != "work:7@x" {
		t.Errorf("reports = %+v, want one settled with record_id work:7@x", recs)
	}
}

// A Dispatch whose Run failed before minting a Record ID must not settle the
// earlier Dispatch's Record that still heads the same issue log: last op wins
// on re-ingest, so doing so would rewrite a landed Record as failed.
func TestSettledBy_EmptyRecordIDLeavesEarlierRecordUntouched(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)
	path := filepath.Join(t.TempDir(), "issue-7.log")
	stamp := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchStart, Start: &claude.DispatchStart{RecordID: "work:7@x"}})
	settled := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchSettled, Settled: &claude.DispatchSettled{RecordID: "work:7@x", State: "complete", Reason: "merged"}})
	if err := os.WriteFile(path, []byte(stamp+settled), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)

	SettledBy(&dispatch.Fake{LogPathResult: path}, dispatchkey.Issue("7"), "failed", ReasonBoxFailed, "claim failed")

	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Errorf("log changed:\n%s", after)
	}
	if recs := readRecords(); len(recs) != 1 || recs[0].RecordID != "" {
		t.Errorf("reports = %+v, want one settled with no record_id", recs)
	}
}

func TestSettledBy_AppendsUnderDispatchRecordID(t *testing.T) {
	testutil.InstallPipeReporter(t)
	path := filepath.Join(t.TempDir(), "issue-7.log")
	if err := os.WriteFile(path, []byte("existing\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	SettledBy(&dispatch.Fake{LogPathResult: path, RecordIDResult: "work:7@y"}, dispatchkey.Issue("7"), "failed", ReasonBoxFailed, "boom")

	want := claude.DispatchSettled{RecordID: "work:7@y", State: "failed", Reason: ReasonBoxFailed, Note: "boom"}
	if ops := settledOps(t, path); len(ops) != 1 || ops[0] != want {
		t.Fatalf("settled ops = %+v, want [%+v]", ops, want)
	}
}

// A bare Settled never reaches for the log's stamp.
func TestSettled_EmptyRecordIDAppendsNothing(t *testing.T) {
	testutil.InstallPipeReporter(t)
	path := filepath.Join(t.TempDir(), "issue-7.log")
	stamp := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchStart, Start: &claude.DispatchStart{RecordID: "work:7@x"}})
	if err := os.WriteFile(path, []byte(stamp), 0o644); err != nil {
		t.Fatal(err)
	}

	Settled(dispatchkey.Issue("7"), path, claude.DispatchSettled{State: "failed"})

	if ops := settledOps(t, path); len(ops) != 0 {
		t.Fatalf("settled ops = %+v, want none", ops)
	}
}

func TestSettled_ExplicitRecordIDWinsOverStamp(t *testing.T) {
	testutil.InstallPipeReporter(t)
	path := filepath.Join(t.TempDir(), "issue-7.log")
	stamp := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchStart, Start: &claude.DispatchStart{RecordID: "work:7@x"}})
	if err := os.WriteFile(path, []byte(stamp), 0o644); err != nil {
		t.Fatal(err)
	}

	Settled(dispatchkey.Issue("7"), path, claude.DispatchSettled{RecordID: "work:7@y", State: "failed"})

	if ops := settledOps(t, path); len(ops) != 1 || ops[0].RecordID != "work:7@y" {
		t.Fatalf("settled ops = %+v", ops)
	}
}

// A missing log means the Dispatch has no Record to settle: nothing is
// appended and no stub file appears, but the report still goes out.
func TestSettled_MissingLogAppendsNothing(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)
	path := filepath.Join(t.TempDir(), "issue-7.log")

	Settled(dispatchkey.Issue("7"), path, claude.DispatchSettled{RecordID: "work:7@x", State: "failed"})
	Settled(dispatchkey.Issue("7"), "", claude.DispatchSettled{State: "failed"})

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("log stat err = %v, want not exist", err)
	}
	if recs := readRecords(); len(recs) != 2 {
		t.Errorf("reports = %+v, want 2", recs)
	}
}

// An unstamped log yields no Record ID, so nothing is appended to it.
func TestSettled_UnstampedLogAppendsNothing(t *testing.T) {
	testutil.InstallPipeReporter(t)
	path := filepath.Join(t.TempDir(), "issue-7.log")
	if err := os.WriteFile(path, []byte(`{"type":"assistant"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	SettledPrior(dispatchkey.Issue("7"), path, claude.DispatchSettled{State: "failed"})

	if ops := settledOps(t, path); len(ops) != 0 {
		t.Fatalf("settled ops = %+v, want none", ops)
	}
}

// A tokened stamp only honours a settled op that echoes its host token
// (issue #4812); the host append copies it from the log's own stamp, so the
// settled outcome survives ParseLog. A token-less legacy stamp still settles.
// The explicit rows are the adopt path: a fix or conflict log is stamped with
// the prior Record's ID under a fresh token, and the caller already holds that
// ID, so SettledPrior's fill-in is bypassed and the token must still be echoed.
func TestSettled_EchoesStampHostToken(t *testing.T) {
	tests := []struct {
		name       string
		token      string
		explicitID bool
	}{
		{"prior fill-in", "tok", false},
		{"prior fill-in legacy", "", false},
		{"explicit record id", "fresh-tok", true},
		{"explicit record id legacy", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testutil.InstallPipeReporter(t)
			path := filepath.Join(t.TempDir(), "issue-7.log")
			stamp := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchStart, Start: &claude.DispatchStart{RecordID: "work:7@x", Kind: "work", DispatchKey: "7", HostToken: tc.token}})
			if err := os.WriteFile(path, []byte(stamp), 0o644); err != nil {
				t.Fatal(err)
			}

			ds := claude.DispatchSettled{State: "complete", Reason: "merged"}
			if tc.explicitID {
				ds.RecordID = "work:7@x"
				Settled(dispatchkey.Issue("7"), path, ds)
			} else {
				SettledPrior(dispatchkey.Issue("7"), path, ds)
			}

			if ops := settledOps(t, path); len(ops) != 1 || ops[0].HostToken != tc.token {
				t.Fatalf("settled ops = %+v, want one echoing host token %q", ops, tc.token)
			}
			rec, _, err := dispatchrecord.ParseLog(path)
			if err != nil {
				t.Fatal(err)
			}
			if rec.OutcomeSource != dispatchrecord.OutcomeSourceSettled {
				t.Errorf("OutcomeSource = %q, want %q", rec.OutcomeSource, dispatchrecord.OutcomeSourceSettled)
			}
		})
	}
}
