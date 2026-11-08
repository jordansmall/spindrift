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
	"spindrift.dev/launcher/internal/exitcode"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostop"
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
	stamp := hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpDispatchStart, Start: &hostop.DispatchStart{RecordID: recoverRecordID}})
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte(stamp), prior...), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func settledLogOps(t *testing.T, path string) []hostop.DispatchSettled {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var ops []hostop.DispatchSettled
	for _, line := range strings.Split(string(data), "\n") {
		var ev hostop.Event
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

	if code := x.runOn(t, stopAfterClaim{IssueTracker: x.fc, stop: stop}, x.cf, capsFor(x.tracker, x.cf)); code != int(exitcode.SignalledStop) {
		t.Fatalf("exit = %d, want %d", code, int(exitcode.SignalledStop))
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
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.InProgressLabel}})
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

// JIRA_LIFECYCLE=status (issue #6353): a refused park comments on the card and
// settles transition-refused rather than relay-failed.
func TestParkQueueFailure_RefusedParkCommentsAndSettlesDistinctReason(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	path := stampPrimaryLog(t, x.dir, "42")
	id, err := recoverrecord.BundleID(x.dir, "42")
	if err != nil {
		t.Fatal(err)
	}
	read := testutil.InstallPipeReporter(t)
	x.fc.TransitionStateErr = &forge.TransitionRefusedError{Issue: "42", From: "In Progress", To: "Blocked"}
	x.fc.CommentCalls = nil

	testutil.CaptureStderr(t, func() {
		_ = parkQueueFailure(x.c, x.fc, "42", recoverrecord.Load(x.dir, "42", id), errors.New("boom"), time.Now(), io.Discard, io.Discard)
	})

	requireRecoverSettled(t, path, "failed", "transition-refused", read())
	if len(x.fc.CommentCalls) != 1 || !strings.Contains(x.fc.CommentCalls[0].Body, "could not move") {
		t.Errorf("comments = %+v, want one naming the refusal", x.fc.CommentCalls)
	}
}

func TestRecoverFailed_RefusedRestoreCommentsRefusal(t *testing.T) {
	c := reconcileConfig()
	fc := forge.NewFake(dispatchLabels(c))
	fc.SetIssue(forge.Issue{Number: "42", Labels: []string{c.InProgressLabel}})
	fc.PriorClaimStates = map[string]forge.DispatchState{"42": forge.Complete}
	fc.TransitionStateErr = &forge.TransitionRefusedError{Issue: "42", From: "In Progress", To: "Done"}
	orig := errors.New("no change")

	var got error
	testutil.CaptureStderr(t, func() {
		got = recoverFailed(fc, capsFor(fc, fc), "42", orig, io.Discard, io.Discard)
	})

	if got != orig {
		t.Errorf("err = %v, want the original failure", got)
	}
	if len(fc.CommentCalls) != 1 || !strings.Contains(fc.CommentCalls[0].Body, `"Done"`) {
		t.Errorf("comments = %+v, want one naming the refusal", fc.CommentCalls)
	}
}

// refuseRestore refuses the InProgress->Failed move (the stop-after-claim
// restore) the way a status-lifecycle workflow without that edge would.
type refuseRestore struct{ forge.IssueTracker }

func (w refuseRestore) TransitionState(num string, from, to forge.DispatchState) error {
	if from == forge.InProgress && to == forge.Failed {
		return &forge.TransitionRefusedError{Issue: num, From: "In Progress", To: "Blocked"}
	}
	return w.IssueTracker.TransitionState(num, from, to)
}

func TestRecoverQueueOne_RefusedRestoreCommentsAndSettlesDistinctReason(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	path := stampPrimaryLog(t, x.dir, "42")
	stop := withControlledStop(t)
	read := testutil.InstallPipeReporter(t)
	x.fc.CommentCalls = nil

	testutil.CaptureStderr(t, func() {
		x.runOn(t, stopAfterClaim{IssueTracker: refuseRestore{x.fc}, stop: stop}, x.cf, capsFor(x.tracker, x.cf))
	})

	requireRecoverSettled(t, path, "failed", "transition-refused", read())
	if len(x.fc.CommentCalls) != 1 || !strings.Contains(x.fc.CommentCalls[0].Body, "could not move") {
		t.Errorf("comments = %+v, want one naming the refusal", x.fc.CommentCalls)
	}
}
