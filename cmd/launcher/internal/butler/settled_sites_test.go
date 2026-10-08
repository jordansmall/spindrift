package butler

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/ledger/ledgertest"
	"spindrift.dev/launcher/internal/report"
	"spindrift.dev/launcher/internal/testutil"
)

const settledRecordID = "butler:bugs@x"

// settledFake returns a Fake whose primary log opens with a dispatch_start
// stamp for settledRecordID.
func settledFake(t *testing.T) (*dispatch.Fake, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "butler.log")
	stamp := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: claude.OpDispatchStart, Start: &claude.DispatchStart{RecordID: settledRecordID}})
	if err := os.WriteFile(path, []byte(stamp), 0o644); err != nil {
		t.Fatal(err)
	}
	d := dispatch.NewFake()
	d.LogPathResult = path
	d.RecordIDResult = settledRecordID
	return d, path
}

// requireOneSettled asserts the log gained exactly one dispatch_settled with
// the given state/reason and the report carries the same record_id.
func requireOneSettled(t *testing.T, path, state, reason string, recs []report.Record) {
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
	if len(ops) != 1 || ops[0].RecordID != settledRecordID || ops[0].State != state || ops[0].Reason != reason {
		t.Fatalf("settled ops = %+v, want one %s/%s for %s", ops, state, reason, settledRecordID)
	}
	if len(recs) != 1 || recs[0].Event != report.EventSettled || recs[0].RecordID != settledRecordID || recs[0].State != state {
		t.Fatalf("reports = %+v, want one settled carrying record_id %s", recs, settledRecordID)
	}
}

func newSettledRun(t *testing.T, backend ledger.Backend, fc *forge.Fake) *settleRun {
	t.Helper()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claim := claimButlerChore(t, backend, "bugs", start)
	now := start.Add(time.Minute)
	return newSettleRun(fc.AsIssueFiler(), backend, "bugs", claim, chore.Scope{Head: "h", NextCursor: "c"}, func() time.Time { return now }, chore.Room{}, promotion{}, patchRung{})
}

func TestSettleRun_CompleteAppendsSettled(t *testing.T) {
	read := testutil.InstallPipeReporter(t)
	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"
	s := newSettledRun(t, ledger.Local{Repo: ledgertest.NewRepo(t)}, fc)
	d, path := settledFake(t)

	s.settle(d, readyResult(`{"title":"f","body":"b","dedupTerms":["a.go:Foo"]}`))

	requireOneSettled(t, path, "complete", "findings-filed", read())
}

func TestSettleRun_FailAppendsSettled(t *testing.T) {
	read := testutil.InstallPipeReporter(t)
	s := newSettledRun(t, ledger.Local{Repo: ledgertest.NewRepo(t)}, forge.NewFake())
	d, path := settledFake(t)

	s.settle(d, dispatch.Result{})

	requireOneSettled(t, path, "failed", "chore-failed", read())
}

func TestSettleRun_LedgerFinishFailureAppendsSettled(t *testing.T) {
	read := testutil.InstallPipeReporter(t)
	backend := doneAppendErr{Backend: ledger.Local{Repo: ledgertest.NewRepo(t)}, err: errors.New("push failed")}
	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/501"
	s := newSettledRun(t, backend, fc)
	d, path := settledFake(t)

	s.settle(d, readyResult(`{"title":"f","body":"b","dedupTerms":["a.go:Foo"]}`))

	requireOneSettled(t, path, "failed", "ledger-finish-failed", read())
}
