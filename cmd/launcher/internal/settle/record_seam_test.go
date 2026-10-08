package settle

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/forge"
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

			recs := ingestRecords(t, root)
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
