package settle

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchkey"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/runner"
	"spindrift.dev/launcher/internal/testutil"
)

// Seam 2: a real Dispatch writes the Pass log, settle appends dispatch_settled
// to it, and the ingester reads the pair back as one stamped Record. The host's
// verdict is the Record's outcome; the Box's own self-report stays separate in
// box_status.
func TestSettle_DispatchRecordSeam(t *testing.T) {
	const num = "77"
	cases := []struct {
		name         string
		boxStatus    string
		checkStates  []forge.RollupState
		maxFix       int
		wantOutcome  string
		wantReason   string
		wantPR       bool
		wantNote     bool
		wantBoxState string
	}{
		{
			name: "complete", boxStatus: "ready",
			checkStates: []forge.RollupState{forge.StateSuccess, forge.StateSuccess},
			wantOutcome: "complete", wantReason: "merged", wantPR: true, wantBoxState: "ready",
		},
		{
			name: "failed: ready Box, CI stays red", boxStatus: "ready",
			checkStates: []forge.RollupState{forge.StateFailure, forge.StateFailure},
			wantOutcome: "failed", wantReason: "ci-red", wantPR: true, wantNote: true, wantBoxState: "ready",
		},
		{
			name: "ambiguous", boxStatus: "ambiguous",
			wantOutcome: "ambiguous", wantReason: "ambiguous", wantNote: true, wantBoxState: "ambiguous",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			readRecords := testutil.InstallPipeReporter(t)

			root := t.TempDir()
			fr := runner.NewFake()
			fr.RunFunc = seamBoxRun(num, "implement", "a1", "2026-05-01T09:00:00Z", tc.boxStatus, "box-said-so")
			d := seamDispatch(t, root, num, fr)

			fc := seamForge(num)
			if tc.checkStates != nil {
				fc.SetPR(fc.AgentBranch(num), forge.PR{URL: testPR})
				fc.SetCheckStates(testPR, tc.checkStates)
			}
			cfg := baseConfig()
			cfg.MaxFixAttempts = tc.maxFix
			cfg.LogPath = func(n string) string { return dispatch.LogPathFor(root, n) }
			s := newTestSettle(cfg, fc, fc)

			testutil.CaptureStdout(t, func() {
				disp := d.Run()
				res := dispatch.Route(disp,
					func() dispatch.Result { t.Fatal("dispatch skipped"); return dispatch.Result{} },
					func(r dispatch.Result) dispatch.Result { return r },
					func(r dispatch.Result) dispatch.Result { return r })
				s.Settle(d, num, 0, res)
			})

			recs := storedRecords(t, root)
			if len(recs) != 1 {
				t.Fatalf("records = %+v, want exactly one", recs)
			}
			r := recs[0]
			if r.Attribution != dispatchrecord.AttributionStamped || r.OutcomeSource != dispatchrecord.OutcomeSourceSettled {
				t.Errorf("attribution/source = %q/%q, want stamped/dispatch_settled", r.Attribution, r.OutcomeSource)
			}
			if r.Outcome != tc.wantOutcome || r.Reason != tc.wantReason || r.BoxStatus != tc.wantBoxState {
				t.Errorf("outcome/reason/box_status = %q/%q/%q, want %q/%q/%q",
					r.Outcome, r.Reason, r.BoxStatus, tc.wantOutcome, tc.wantReason, tc.wantBoxState)
			}
			if (r.PRURL == testPR) != tc.wantPR || (!tc.wantPR && r.PRURL != "") {
				t.Errorf("pr_url = %q, want set=%v", r.PRURL, tc.wantPR)
			}
			if tc.wantNote && r.Note == "" {
				t.Errorf("note is empty, want the host's reason")
			}

			var boxes, settled []report.Record
			for _, rec := range readRecords() {
				switch rec.Event {
				case report.EventSettled:
					settled = append(settled, rec)
				case "box":
					boxes = append(boxes, rec)
				}
			}
			if len(settled) != 1 || settled[0].RecordID != r.ID {
				t.Errorf("settled reports = %+v, want one with record_id %q", settled, r.ID)
			}
			if len(boxes) == 0 {
				t.Errorf("no box report captured")
			}
			for _, b := range boxes {
				if b.RecordID != r.ID {
					t.Errorf("box report record_id = %q, want %q", b.RecordID, r.ID)
				}
			}
		})
	}
}

type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// seamTranscript is one Pass's stream-json: pass_start, an assistant turn and a
// result. A non-empty outcomeLine rides in the result and also leads a raw line
// after it, which is what the host's outcome.Resolve reads.
func seamTranscript(role, msgID, ts, outcomeLine string) string {
	resultField := ""
	tail := ""
	if outcomeLine != "" {
		result, _ := json.Marshal(outcomeLine)
		resultField = `,"result":` + string(result)
		tail = outcomeLine + "\n"
	}
	return claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: role}) +
		`{"type":"assistant","message":{"id":"` + msgID + `","model":"m","content":[]}}` + "\n" +
		`{"type":"result","timestamp":"` + ts + `","num_turns":1,"total_cost_usd":1,` +
		`"usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"m":{}}` + resultField + "}\n" +
		tail
}

// seamBoxRun is a fake Box that writes one Pass reporting status.
func seamBoxRun(num, role, msgID, ts, status, note string) func(runner.Box) error {
	return func(box runner.Box) error {
		line := fmt.Sprintf("SPINDRIFT_OUTCOME issue=%s landing=%s status=%s note=%s nonce=%s",
			num, testPR, status, note, box.Env["RUN_NONCE"])
		box.Output.Write([]byte(seamTranscript(role, msgID, ts, line))) //nolint:errcheck
		return nil
	}
}

// seamDispatch builds a real Dispatch for num over fr, with the host log dir
// made.
func seamDispatch(t *testing.T, root, num string, fr *runner.Fake) *dispatch.Dispatch {
	t.Helper()
	if err := os.MkdirAll(dispatch.HostLogDirFor(root), 0o755); err != nil {
		t.Fatal(err)
	}
	drv, err := driver.New("claude")
	if err != nil {
		t.Fatal(err)
	}
	f, err := dispatch.NewFactory(dispatch.Config{Kind: "work", OpenPRForIssue: func(string) (bool, error) { return false, nil }}, root, fr, drv, dispatch.RealClock())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Cleanup)
	f.SetHeartbeatOut(nopWriter{})
	return f.New(num, "t")
}

// seamForge is a forge with issue num in progress.
func seamForge(num string) *forge.Fake {
	fc := forge.NewFake(ambiguousDispatchLabels)
	fc.BranchPrefix = "agent/issue-"
	fc.SetIssue(forge.Issue{Number: num, Labels: []string{"agent-in-progress"}})
	return fc
}

// ingestRecords ingests the logs under root and returns the Records.
func ingestRecords(t *testing.T, root string) []dispatchrecord.Record {
	t.Helper()
	store, err := dispatchrecord.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if _, err := store.Ingest(); err != nil {
		t.Fatal(err)
	}
	recs, err := store.Records()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// An adopt whose CI is red runs a fix pass on a Dispatch that never Ran. The
// fix log must continue the prior Record, so the dispatch_settled op the adopt
// appends to the primary log lands on it and the ingester takes the host's
// outcome from it, rather than leaving the prior run's stale outcome standing.
func TestSettleAdopted_FixPassContinuesPriorRecord(t *testing.T) {
	const num = "77"
	claim := time.Date(2026, 5, 1, 7, 0, 0, 0, time.UTC)
	prior := dispatchrecord.RecordID("work", num, claim)
	readRecords := testutil.InstallPipeReporter(t)

	root := t.TempDir()
	fr := runner.NewFake()
	fr.RunFunc = seamBoxRun(num, "fix", "f1", "2026-05-02T09:00:00Z", "ready", "fixed")
	d := seamDispatch(t, root, num, fr)

	// The prior run's primary log: its stamp, one pass, and a stale failed verdict.
	primary := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchStart, Start: &claude.DispatchStart{
		RecordID: prior, Kind: "work", DispatchKey: num, ClaimTime: claim, Started: claim,
	}}) + seamTranscript("implement", "a1", "2026-05-01T09:00:00Z", "") +
		claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchSettled, Settled: &claude.DispatchSettled{RecordID: prior, State: "failed", Reason: "box-failed"}})
	if err := os.WriteFile(dispatch.LogPathFor(root, num), []byte(primary), 0o644); err != nil {
		t.Fatal(err)
	}

	fc := seamForge(num)
	fc.SetPR(fc.AgentBranch(num), forge.PR{URL: testPR})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StatePending, forge.StateFailure, forge.StateSuccess, forge.StateSuccess})
	cfg := baseConfig()
	cfg.MaxFixAttempts = 1
	cfg.LogPath = func(n string) string { return dispatch.LogPathFor(root, n) }
	s := newTestSettle(cfg, fc, fc)

	testutil.CaptureStdout(t, func() { s.SettleAdopted(d, num, 0, testPR) })

	if d.RecordID() != prior {
		t.Fatalf("Dispatch RecordID = %q, want the prior Record %q", d.RecordID(), prior)
	}
	recs := ingestRecords(t, root)
	if len(recs) != 1 || recs[0].ID != prior {
		t.Fatalf("records = %+v, want exactly the prior Record %q", recs, prior)
	}
	if r := recs[0]; r.Outcome != "complete" || r.OutcomeSource != dispatchrecord.OutcomeSourceSettled {
		t.Errorf("outcome/source = %q/%q, want complete/dispatch_settled (the stale failed verdict must not stand)", r.Outcome, r.OutcomeSource)
	}
	for _, rec := range readRecords() {
		if rec.Event == report.EventSettled && rec.RecordID != prior {
			t.Errorf("settled report record_id = %q, want %q", rec.RecordID, prior)
		}
	}
}

// storedRecords reads the Records already in root's store, without ingesting:
// settle itself must have put them there.
func storedRecords(t *testing.T, root string) []dispatchrecord.Record {
	t.Helper()
	store, err := dispatchrecord.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recs, err := store.Records()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

// settleSeamComplete drives a green work Dispatch for num through the real
// settle path and returns its one settled report and the stderr it wrote.
func settleSeamComplete(t *testing.T, root, num string) (report.Record, string) {
	t.Helper()
	readRecords := testutil.InstallPipeReporter(t)
	fr := runner.NewFake()
	fr.RunFunc = seamBoxRun(num, "implement", "a1", "2026-05-01T09:00:00Z", "ready", "box-said-so")
	d := seamDispatch(t, root, num, fr)
	fc := seamForge(num)
	fc.SetPR(fc.AgentBranch(num), forge.PR{URL: testPR})
	fc.SetCheckStates(testPR, []forge.RollupState{forge.StateSuccess, forge.StateSuccess})
	cfg := baseConfig()
	cfg.LogPath = func(n string) string { return dispatch.LogPathFor(root, n) }
	s := newTestSettle(cfg, fc, fc)

	var stderr string
	testutil.CaptureStdout(t, func() {
		stderr = testutil.CaptureStderr(t, func() {
			disp := d.Run()
			res := dispatch.Route(disp,
				func() dispatch.Result { t.Fatal("dispatch skipped"); return dispatch.Result{} },
				func(r dispatch.Result) dispatch.Result { return r },
				func(r dispatch.Result) dispatch.Result { return r })
			s.Settle(d, num, 0, res)
		})
	})
	var settled []report.Record
	for _, rec := range readRecords() {
		if rec.Event == report.EventSettled {
			settled = append(settled, rec)
		}
	}
	if len(settled) != 1 {
		t.Fatalf("settled reports = %+v, want exactly one", settled)
	}
	return settled[0], stderr
}

// A store that cannot be written costs the Dispatch nothing: the failure is
// logged and the settle reports and appends exactly what it would have.
func TestSettle_IngestFailureIsLoggedNotFatal(t *testing.T) {
	const num = "77"
	good, goodErr := settleSeamComplete(t, t.TempDir(), num)
	if strings.Contains(goodErr, "could not ingest") {
		t.Fatalf("healthy store warned: %q", goodErr)
	}

	root := t.TempDir()
	if err := os.MkdirAll(hostpaths.DispatchRecordsDB(root), 0o755); err != nil {
		t.Fatal(err)
	}
	got, stderr := settleSeamComplete(t, root, num)

	if !strings.Contains(stderr, "could not ingest") {
		t.Errorf("stderr = %q, want the ingest warning", stderr)
	}
	if got.State != good.State || got.Note != good.Note || got.PRURL != good.PRURL || (got.RecordID == "") != (good.RecordID == "") {
		t.Errorf("settled report = %+v, want the healthy run's %+v", got, good)
	}
	log, err := os.ReadFile(dispatch.LogPathFor(root, num))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), claude.OpDispatchSettled) {
		t.Errorf("log lacks the %s op", claude.OpDispatchSettled)
	}
}

// Slots settle at once on one root; every Dispatch's Record must land.
func TestSettled_ConcurrentSettlesAllLand(t *testing.T) {
	testutil.InstallPipeReporter(t)
	root := t.TempDir()
	const n = 8
	type slot struct {
		d   *dispatch.Dispatch
		num string
	}
	slots := make([]slot, n)
	for i := range slots {
		num := fmt.Sprintf("%d", 100+i)
		fr := runner.NewFake()
		fr.RunFunc = seamBoxRun(num, "implement", "a"+num, "2026-05-01T09:00:00Z", "ready", "box-said-so")
		d := seamDispatch(t, root, num, fr)
		testutil.CaptureStdout(t, func() { d.Run() })
		slots[i] = slot{d, num}
	}

	var wg, ready sync.WaitGroup
	start := make(chan struct{})
	for _, sl := range slots {
		wg.Add(1)
		ready.Add(1)
		go func() {
			defer wg.Done()
			ready.Done()
			<-start
			SettledBy(sl.d, dispatchkey.Issue(sl.num), "complete", "merged", "")
		}()
	}
	ready.Wait()
	stderr := testutil.CaptureStderr(t, func() {
		close(start)
		wg.Wait()
	})
	if strings.Contains(stderr, "could not ingest") {
		t.Errorf("a concurrent ingest failed: %q", stderr)
	}

	recs := storedRecords(t, root)
	if len(recs) != n {
		t.Fatalf("stored %d records, want %d", len(recs), n)
	}
	for _, r := range recs {
		if r.Outcome != "complete" || r.OutcomeSource != dispatchrecord.OutcomeSourceSettled {
			t.Errorf("record %s outcome/source = %q/%q, want complete/dispatch_settled", r.ID, r.Outcome, r.OutcomeSource)
		}
		if len(r.Passes) != 1 || r.Passes[0].Role != "implement" {
			t.Errorf("record %s passes = %+v, want one implement pass", r.ID, r.Passes)
		}
	}

	db, err := sql.Open("sqlite", hostpaths.DispatchRecordsDB(root))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Errorf("integrity_check = %q, want ok", integrity)
	}
}

// Settle ingests only its own Dispatch's logs, so another slot's log it cannot
// read neither blocks its Record nor raises the ingest warning.
func TestSettle_UnreadableUnrelatedLogDoesNotBlockIngest(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads past file modes")
	}
	const num = "77"
	root := t.TempDir()
	dir := hostpaths.LogDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "issue-1.log")
	line := `{"type":"result","timestamp":"2026-03-01T10:00:00.000Z","total_cost_usd":1}` + "\n"
	if err := os.WriteFile(bad, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bad, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(bad, 0o644); err != nil {
			t.Error(err)
		}
	})

	_, stderr := settleSeamComplete(t, root, num)

	if strings.Contains(stderr, "could not ingest") {
		t.Errorf("stderr = %q, want no ingest warning", stderr)
	}
	store, err := dispatchrecord.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recs, err := store.Records()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range recs {
		if strings.Contains(r.ID, num) {
			found = true
		}
	}
	if !found {
		t.Errorf("records = %+v, want one for %s", recs, num)
	}
}

// A log of the settled Dispatch's own that cannot be read is reported, but the
// Record from its readable primary still lands.
func TestSettle_UnreadableOwnLogWarnsButRecordLands(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads past file modes")
	}
	const num = "77"
	root := t.TempDir()
	dir := hostpaths.LogDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "issue-"+num+"-fix-1.log")
	line := `{"type":"result","timestamp":"2026-03-01T10:00:00.000Z","total_cost_usd":1}` + "\n"
	if err := os.WriteFile(bad, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bad, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The Dispatch's start quarantines the file to <bad>.prior-run.N, which
		// keeps the mode and still belongs to this key.
		moved, _ := filepath.Glob(bad + "*")
		for _, p := range moved {
			if err := os.Chmod(p, 0o644); err != nil {
				t.Error(err)
			}
		}
	})

	_, stderr := settleSeamComplete(t, root, num)

	if !strings.Contains(stderr, "could not ingest") {
		t.Errorf("stderr = %q, want the ingest warning", stderr)
	}
	store, err := dispatchrecord.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	recs, err := store.Records()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range recs {
		if strings.Contains(r.ID, num) {
			found = true
		}
	}
	if !found {
		t.Errorf("records = %+v, want one for %s", recs, num)
	}
}
