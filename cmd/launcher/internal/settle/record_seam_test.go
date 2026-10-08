package settle

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

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
			if err := dispatchMkLogDir(root); err != nil {
				t.Fatal(err)
			}
			drv, err := driver.New("claude")
			if err != nil {
				t.Fatal(err)
			}
			fr := runner.NewFake()
			fr.RunFunc = func(box runner.Box) error {
				line := fmt.Sprintf("SPINDRIFT_OUTCOME issue=%s landing=%s status=%s note=box-said-so nonce=%s",
					num, testPR, tc.boxStatus, box.Env["RUN_NONCE"])
				result, _ := json.Marshal(line)
				box.Output.Write([]byte(claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}) + //nolint:errcheck
					`{"type":"assistant","message":{"id":"a1","model":"m","content":[]}}` + "\n" +
					`{"type":"result","timestamp":"2026-05-01T09:00:00Z","num_turns":1,"total_cost_usd":1,` +
					`"usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"m":{}},"result":` + string(result) + "}\n" +
					// The in-box extractor also leads a raw line with the outcome,
					// which is what the host's outcome.Resolve reads.
					line + "\n"))
				return nil
			}
			dcfg := dispatch.Config{
				Kind:           "work",
				OpenPRForIssue: func(string) (bool, error) { return false, nil },
			}
			f, err := dispatch.NewFactory(dcfg, root, fr, drv, dispatch.RealClock())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(f.Cleanup)
			f.SetHeartbeatOut(nopWriter{})
			d := f.New(num, "t")

			fc := forge.NewFake(ambiguousDispatchLabels)
			fc.BranchPrefix = "agent/issue-"
			fc.SetIssue(forge.Issue{Number: num, Labels: []string{"agent-in-progress"}})
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

func dispatchMkLogDir(root string) error {
	return os.MkdirAll(dispatch.HostLogDirFor(root), 0o755)
}
