package main

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/daemon"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/testutil"
)

func TestRecoverQueueOne_ReportsBoxCIWaitThenSettledComplete(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	read := testutil.InstallPipeReporter(t)

	if code, _ := x.run(t); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	got := read()
	key := dispatchkey.Issue("42")
	if len(got) != 3 {
		t.Fatalf("records = %+v, want box, ci_wait, then settled", got)
	}
	if got[0].Event != report.EventBox || got[0].Key != key || got[0].Phase != report.PhaseRecover {
		t.Errorf("first record = %+v, want box for #42 at phase %q", got[0], report.PhaseRecover)
	}
	if got[1].Event != report.EventCIWait || got[1].Key != key || got[1].PRURL == "" || got[1].PRURL != got[2].PRURL {
		t.Errorf("second record = %+v, want ci_wait for #42 naming the settled PR %q", got[1], got[2].PRURL)
	}
	if got[2].Event != report.EventSettled || got[2].Key != key || got[2].State != forge.Complete.String() {
		t.Errorf("third record = %+v, want settled(%s) for #42", got[2], forge.Complete)
	}
}

func TestRecoverQueueOne_ReportsBoxThenSettledFailedOnRelayFailure(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.fc.RelayBundleErr = errors.New("relay: boom")
	read := testutil.InstallPipeReporter(t)

	if code, _ := x.run(t); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}

	got := read()
	key := dispatchkey.Issue("42")
	if len(got) != 2 {
		t.Fatalf("records = %+v, want exactly box then one settled", got)
	}
	if got[0].Event != report.EventBox || got[0].Key != key {
		t.Errorf("first record = %+v, want box for #42", got[0])
	}
	if got[1].Event != report.EventSettled || got[1].Key != key || got[1].State != forge.Failed.String() || got[1].Note == "" {
		t.Errorf("second record = %+v, want settled(%s) with a note for #42", got[1], forge.Failed)
	}
}

func TestRecoverQueueOne_IneligibleScanReportsNothing(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "blocked")
	read := testutil.InstallPipeReporter(t)

	if code, _ := x.run(t); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if got := read(); len(got) != 0 {
		t.Errorf("records = %+v, want none", got)
	}
}

// The daemon spawns `recover --max-jobs 1 --max-parallel 1`, verb first and
// flags after; the flags must be consumed before the handler picks queue mode,
// not read as an issue number.
func TestMainRun_Recover_DaemonChildArgvReachesQueueMode(t *testing.T) {
	t.Setenv("REPO_SLUG", "")
	cmd, err := daemon.ChildCommand(daemon.ChildSpec{
		RepoPath: "/repo", AppAttr: ".#", Revision: strings.Repeat("a", 40), Kind: daemon.KindOf(dispatchkind.Recover),
	})
	if err != nil {
		t.Fatal(err)
	}
	i := slices.Index(cmd.Argv, "--")
	if i < 0 {
		t.Fatalf("argv %v has no -- separator", cmd.Argv)
	}

	var stdout, stderr bytes.Buffer
	code := mainRun(cmd.Argv[i+1:], &stdout, &stderr)

	if code != exitConfigInvalid {
		t.Errorf("mainRun(%v) code = %d, want %d (queue mode reached bootstrap)", cmd.Argv[i+1:], code, exitConfigInvalid)
	}
	if !strings.Contains(stderr.String(), "REPO_SLUG") {
		t.Errorf("stderr = %q, want a REPO_SLUG validation error", stderr.String())
	}
}

func TestRecoverQueueOne_ReportsSettledFailedWhenStoppedAfterClaim(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	stop := withControlledStop(t)
	read := testutil.InstallPipeReporter(t)

	code := x.runOn(t, stopAfterClaim{IssueTracker: x.fc, stop: stop}, x.cf, capsFor(x.tracker, x.cf))

	if code != exitSignalledStop {
		t.Fatalf("exit = %d, want %d", code, exitSignalledStop)
	}
	got := read()
	key := dispatchkey.Issue("42")
	if len(got) != 2 {
		t.Fatalf("records = %+v, want box then one settled", got)
	}
	if got[0].Event != report.EventBox || got[0].Key != key {
		t.Errorf("first record = %+v, want box for #42", got[0])
	}
	if got[1].Event != report.EventSettled || got[1].Key != key || got[1].State != forge.Failed.String() {
		t.Errorf("second record = %+v, want settled(%s) for #42", got[1], forge.Failed)
	}
}

// The daemon must see a settled record for every box record even when the
// restore write fails (#3627): the host decided Failed regardless.
func TestRecoverQueueOne_ReportsSettledFailedWhenRestoreAfterStopFails(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	stop := withControlledStop(t)
	read := testutil.InstallPipeReporter(t)

	it := failRestore{IssueTracker: stopAfterClaim{IssueTracker: x.fc, stop: stop}}
	if code := x.runOn(t, it, x.cf, capsFor(x.tracker, x.cf)); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}

	got := read()
	key := dispatchkey.Issue("42")
	if len(got) != 2 {
		t.Fatalf("records = %+v, want box then exactly one settled", got)
	}
	if got[0].Event != report.EventBox || got[0].Key != key {
		t.Errorf("first record = %+v, want box for #42", got[0])
	}
	if got[1].Event != report.EventSettled || got[1].Key != key || got[1].State != forge.Failed.String() {
		t.Errorf("second record = %+v, want settled(%s) for #42", got[1], forge.Failed)
	}
}

func TestRecoverQueueOne_DrainStopDuringFailedSettleReportsOneSettled(t *testing.T) {
	x := newQueueRecoverFixture(t)
	x.addFailed(t, "42", "ready")
	x.fc.RelayBundleErr = errors.New("relay: boom")
	stop := withControlledStop(t)
	s := stopInSettle{WorkSettler: newWorkSettle(x.c, x.tracker, testWired(x.tracker), x.cf), stop: stop}
	read := testutil.InstallPipeReporter(t)

	if code := x.runOnSettler(t, x.fc, x.cf, capsFor(x.tracker, x.cf), s); code != exitSignalledStop {
		t.Fatalf("exit = %d, want %d", code, exitSignalledStop)
	}

	got := read()
	key := dispatchkey.Issue("42")
	if len(got) != 2 {
		t.Fatalf("records = %+v, want box then exactly one settled", got)
	}
	if got[0].Event != report.EventBox || got[0].Key != key {
		t.Errorf("first record = %+v, want box for #42", got[0])
	}
	if got[1].Event != report.EventSettled || got[1].Key != key || got[1].State != forge.Failed.String() {
		t.Errorf("second record = %+v, want settled(%s) for #42", got[1], forge.Failed)
	}
}
