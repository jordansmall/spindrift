package settle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

// A Dispatch that never Ran (recover's) has no Record ID of its own; Settled
// takes it from the stamp of the prior Dispatch's log, and the report carries
// the same ID.
func TestSettled_FillsRecordIDFromLogStamp(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)
	path := filepath.Join(t.TempDir(), "issue-7.log")
	stamp := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "dispatch_start", Start: &claude.DispatchStart{RecordID: "work:7@x", Kind: "work", DispatchKey: "7"}})
	if err := os.WriteFile(path, []byte(stamp), 0o644); err != nil {
		t.Fatal(err)
	}

	Settled(dispatchkey.Issue("7"), path, claude.DispatchSettled{State: "complete", Reason: "merged", PRURL: "https://x/pr/1"})

	ops := settledOps(t, path)
	if len(ops) != 1 || ops[0].RecordID != "work:7@x" || ops[0].State != "complete" || ops[0].PRURL != "https://x/pr/1" {
		t.Fatalf("settled ops = %+v", ops)
	}
	if got := dispatchrecord.StampRecordID(path); got != "work:7@x" {
		t.Errorf("stamp = %q, log no longer opens with it", got)
	}
	recs := readRecords()
	if len(recs) != 1 || recs[0].Event != report.EventSettled || recs[0].RecordID != "work:7@x" {
		t.Errorf("reports = %+v, want one settled with record_id work:7@x", recs)
	}
}

func TestSettled_ExplicitRecordIDWinsOverStamp(t *testing.T) {
	testutil.InstallPipeReporter(t)
	path := filepath.Join(t.TempDir(), "issue-7.log")
	stamp := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "dispatch_start", Start: &claude.DispatchStart{RecordID: "work:7@x"}})
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

	Settled(dispatchkey.Issue("7"), path, claude.DispatchSettled{State: "failed"})

	if ops := settledOps(t, path); len(ops) != 0 {
		t.Fatalf("settled ops = %+v, want none", ops)
	}
}
