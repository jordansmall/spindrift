package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/recoverrecord"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/testutil"
)

const recoverRecordID = "work:42@x"

// stampPrimaryLog makes num's primary log in dir open with a dispatch_start
// stamp, keeping any content already there: recover settles the prior work
// Dispatch's Record, whose ID only the stamp carries.
func stampPrimaryLog(t *testing.T, dir, num string) string {
	t.Helper()
	path := dispatch.LogPathFor(dir, num)
	prior, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	stamp := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchStart, Start: &claude.DispatchStart{RecordID: recoverRecordID}})
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte(stamp), prior...), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func settledLogOps(t *testing.T, path string) []claude.DispatchSettled {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ops []claude.DispatchSettled
	for _, line := range strings.Split(string(data), "\n") {
		var ev claude.Event
		if json.Unmarshal([]byte(line), &ev) == nil && ev.SpindriftOp != nil && ev.SpindriftOp.Settled != nil {
			ops = append(ops, *ev.SpindriftOp.Settled)
		}
	}
	return ops
}

// requireRecoverSettled asserts the log gained exactly one dispatch_settled
// and the last settled report carries the stamp's record_id.
func requireRecoverSettled(t *testing.T, path, state, reason string, recs []report.Record) {
	t.Helper()
	ops := settledLogOps(t, path)
	if len(ops) != 1 || ops[0].RecordID != recoverRecordID || ops[0].State != state || ops[0].Reason != reason {
		t.Fatalf("settled ops = %+v, want one %s/%s for %s", ops, state, reason, recoverRecordID)
	}
	var settled []report.Record
	for _, r := range recs {
		if r.Event == report.EventSettled {
			settled = append(settled, r)
		}
	}
	if len(settled) != 1 || settled[0].RecordID != recoverRecordID || settled[0].State != state {
		t.Fatalf("settled reports = %+v, want one carrying record_id %s", settled, recoverRecordID)
	}
}

func TestParkQueueFailure_AppendsSettled(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	path := stampPrimaryLog(t, x.dir, "42")
	id, err := recoverrecord.BundleID(x.dir, "42")
	if err != nil {
		t.Fatal(err)
	}
	read := testutil.InstallPipeReporter(t)

	if err := parkQueueFailure(x.c, x.fc, "42", recoverrecord.Load(x.dir, "42", id), errors.New("boom"), time.Now(), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}

	requireRecoverSettled(t, path, "failed", "relay-failed", read())
}

func TestRecoverQueueOne_StoppedAfterClaimAppendsSettled(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	path := stampPrimaryLog(t, x.dir, "42")
	stop := withControlledStop(t)
	read := testutil.InstallPipeReporter(t)

	if code := x.runOn(t, stopAfterClaim{IssueTracker: x.fc, stop: stop}, x.cf, capsFor(x.tracker, x.cf)); code != exitSignalledStop {
		t.Fatalf("exit = %d, want %d", code, exitSignalledStop)
	}

	requireRecoverSettled(t, path, "failed", "stopped", read())
}

// A recover that lands the prior Dispatch's work settles through the work
// settler's flushSettled with a never-Run Dispatch, so the stamp supplies the
// Record ID.
func TestRecoverQueueOne_LandedAppendsSettledFromStamp(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	path := stampPrimaryLog(t, x.dir, "42")
	read := testutil.InstallPipeReporter(t)

	if code, _ := x.run(t); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	ops := settledLogOps(t, path)
	if len(ops) != 1 || ops[0].RecordID != recoverRecordID || ops[0].State != "complete" {
		t.Fatalf("settled ops = %+v, want one complete for %s", ops, recoverRecordID)
	}
	var settled []report.Record
	for _, r := range read() {
		if r.Event == report.EventSettled {
			settled = append(settled, r)
		}
	}
	if len(settled) != 1 || settled[0].RecordID != recoverRecordID {
		t.Fatalf("settled reports = %+v, want one carrying record_id %s", settled, recoverRecordID)
	}
}

func TestRecoverFailed_DeclinedAppendsSettled(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.inProgressLabel}})
	fc.PriorClaimStates = map[string]forge.DispatchState{"42": forge.Complete}
	dir := tempLogDir(t)
	t.Chdir(dir)
	path := stampPrimaryLog(t, dir, "42")
	read := testutil.InstallPipeReporter(t)

	if err := recoverFailed(fc, capsFor(fc, fc), "42", errors.New("no change"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}

	requireRecoverSettled(t, path, "complete", "recover-declined", read())
}
