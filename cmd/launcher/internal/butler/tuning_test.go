package butler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/doctor"
	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/ledger/ledgertest"
	"spindrift.dev/launcher/internal/testutil"
)

var tuningNow = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

// seedRecords ingests n settled work Records into root's Dispatch Records
// store, each with an implement and a review pass, claimed one hour apart
// starting at first. keys are unique per call through keyBase, and the
// returned IDs are in claim order.
func seedRecords(t *testing.T, root string, keyBase, n int, first time.Time) []string {
	return seedRecordsCost(t, root, keyBase, n, first, 2)
}

// seedRecordsCost is seedRecords with the implement pass costing implementUSD.
func seedRecordsCost(t *testing.T, root string, keyBase, n int, first time.Time, implementUSD float64) []string {
	t.Helper()
	dir := hostpaths.LogDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	op := func(o claude.SpindriftOp) string { return claude.EncodeSpindriftOp(o) }
	result := func(ts string, cost float64) string {
		return fmt.Sprintf(`{"type":"result","timestamp":%q,"num_turns":3,"total_cost_usd":%g,"duration_ms":1000,"duration_api_ms":800,`+
			`"usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40},`+
			`"modelUsage":{"opus":{}}}`+"\n", ts, cost)
	}
	var ids []string
	for i := range n {
		key := fmt.Sprint(keyBase + i)
		claim := first.Add(time.Duration(i) * time.Hour)
		id := dispatchrecord.RecordID("work", key, claim)
		ts := claim.Add(time.Minute).Format(time.RFC3339)
		lines := []string{
			op(claude.SpindriftOp{Op: "dispatch_start", Start: &claude.DispatchStart{
				RecordID: id, Kind: "work", DispatchKey: key, ClaimTime: claim, Started: claim, Driver: "claude",
			}}),
			op(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}),
			result(ts, implementUSD),
			op(claude.SpindriftOp{Op: "pass_start", Pass: 2, Role: "review"}),
			result(ts, 1),
			op(claude.SpindriftOp{Op: "dispatch_settled", Settled: &claude.DispatchSettled{RecordID: id, State: "complete", Reason: "merged"}}),
		}
		if err := os.WriteFile(filepath.Join(dir, "issue-"+key+".log"), []byte(strings.Join(lines, "")), 0o644); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	s, err := dispatchrecord.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Ingest(); err != nil {
		t.Fatal(err)
	}
	return ids
}

func tuningPolicy(root string, minRecords int) Policy {
	p := testRunPolicy(0, "tuning")
	p.Chores = []chore.Chore{{Name: "tuning", Every: 24 * time.Hour, Records: true, FindingLabel: doctor.TuningFindingLabel}}
	p.RecordsRoot = root
	p.TuningMinRecords = minRecords
	p.TuningMinSample = 15
	return p
}

type tuningRun struct {
	backend ledger.Local
	fc      *forge.Fake
	input   []string
	boxes   int

	// recordIDs holds the Record ID each fake Box answered, derived from the
	// Chore's pinned ClaimTime the way the real Dispatch mints it.
	recordIDs []string
	// onRun, when set, runs as each fake Box's Run begins.
	onRun func(recordID string)
}

// hookedBox lets a test observe the instant its fake Box launches.
type hookedBox struct {
	*dispatch.Fake
	onRun func()
}

func (h hookedBox) Run() dispatch.Disposition {
	if h.onRun != nil {
		h.onRun()
	}
	return h.Fake.Run()
}

// sweepTuning runs one tuning Sweep at now against r's backend and forge
// fake, with a tree that must never be asked for files.
func (tr *tuningRun) sweepTuning(t *testing.T, policy Policy, now time.Time) (Outcome, error) {
	t.Helper()
	newBox := func(c dispatch.Chore) dispatch.Dispatcher {
		tr.boxes++
		tr.input = append(tr.input, c.Input)
		d := readyDispatcher()
		d.RecordIDResult = dispatchrecord.RecordID("tuning", c.Name, c.ClaimTime)
		tr.recordIDs = append(tr.recordIDs, d.RecordIDResult)
		return hookedBox{Fake: d, onRun: func() {
			if tr.onRun != nil {
				tr.onRun(d.RecordIDResult)
			}
		}}
	}
	tree := fakeTree{head: "headsha", filesErr: fmt.Errorf("a records-scoped Chore must not list tracked files")}
	r := New(tr.backend, tree, tr.fc.AsIssueFiler(), newBox, policy, func() time.Time { return now })
	var out Outcome
	var err error
	captureStdout(t, func() { out, err = r.Sweep([]string{"tuning"}) })
	return out, err
}

func newTuningRun(t *testing.T) *tuningRun {
	t.Helper()
	fc := forge.NewFake()
	fc.PostIssueURL = "https://example.com/issues/77"
	return &tuningRun{backend: ledger.Local{Repo: ledgertest.NewRepo(t)}, fc: fc}
}

func seedTuningDone(t *testing.T, tr *tuningRun, at time.Time, cursor string) {
	t.Helper()
	claim, err := ledger.Claim(tr.backend, "tuning", ledger.Tip{}, ledger.ClaimedBy{Host: "h", Start: at})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Finish(tr.backend, "tuning", claim, ledger.State{LastSwept: "headsha", Cursor: cursor}, at); err != nil {
		t.Fatal(err)
	}
}

func TestSweep_Tuning_StoresSnapshotAndLedgerRef(t *testing.T) {
	root := t.TempDir()
	seedRecords(t, root, 1, 5, tuningNow.Add(-48*time.Hour))
	tr := newTuningRun(t)
	var storedAtLaunch bool
	tr.onRun = func(id string) {
		s, err := dispatchrecord.Open(root)
		if err != nil {
			t.Error(err)
			return
		}
		defer s.Close()
		_, storedAtLaunch, _ = s.TuningSnapshot(id)
	}

	out, err := tr.sweepTuning(t, tuningPolicy(root, 1), tuningNow)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != Swept || len(tr.input) != 1 {
		t.Fatalf("Outcome = %+v, boxes = %d, want one Swept Box", out, tr.boxes)
	}
	id := tr.recordIDs[0]
	sum := sha256.Sum256([]byte(tr.input[0]))
	want := ledger.Snapshot{Sweep: id, SHA256: hex.EncodeToString(sum[:])}

	if !storedAtLaunch {
		t.Error("the snapshot was not in the store when the Box launched")
	}
	store, err := dispatchrecord.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snap, ok, err := store.TuningSnapshot(id)
	if err != nil || !ok {
		t.Fatalf("TuningSnapshot(%q) = ok %v, err %v", id, ok, err)
	}
	if snap.Rendered != tr.input[0] || snap.SHA256 != want.SHA256 || !snap.CreatedAt.Equal(tuningNow) {
		t.Errorf("snapshot = %+v, want the Box's input, sha %s, created %v", snap, want.SHA256, tuningNow)
	}

	tip, err := tr.backend.Read("tuning")
	if err != nil {
		t.Fatal(err)
	}
	if tip.State.Snapshot == nil || *tip.State.Snapshot != want {
		t.Errorf("Ledger Snapshot = %+v, want %+v", tip.State.Snapshot, want)
	}
	raw, err := json.Marshal(tip.State)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "| Anchor |") || strings.Contains(string(raw), snap.Rendered[:20]) {
		t.Errorf("the digest text leaked into the Ledger state: %s", raw)
	}
}

func TestSweep_Tuning_NoRecordIDLeavesClaim(t *testing.T) {
	root := t.TempDir()
	seedRecords(t, root, 1, 5, tuningNow.Add(-48*time.Hour))
	tr := newTuningRun(t)
	// A Box that answers no Record ID cannot have its snapshot keyed.
	newBox := func(dispatch.Chore) dispatch.Dispatcher { return readyDispatcher() }
	r := New(tr.backend, fakeTree{head: "headsha"}, tr.fc.AsIssueFiler(), newBox, tuningPolicy(root, 1), func() time.Time { return tuningNow })
	var err error
	captureStdout(t, func() { _, err = r.Sweep([]string{"tuning"}) })
	if err == nil {
		t.Fatal("Sweep succeeded without a Record ID to key the snapshot")
	}
	tip, rerr := tr.backend.Read("tuning")
	if rerr != nil || tip.State.ClaimedBy == nil || tip.State.Snapshot != nil {
		t.Errorf("tip = %+v, err %v, want the claim left standing", tip.State, rerr)
	}
}

func TestSweep_Tuning_NotDueBeforeInterval(t *testing.T) {
	root := t.TempDir()
	ids := seedRecords(t, root, 1, 5, tuningNow.Add(-48*time.Hour))
	tr := newTuningRun(t)
	seedTuningDone(t, tr, tuningNow.Add(-2*time.Hour), ids[0])

	out, err := tr.sweepTuning(t, tuningPolicy(root, 1), tuningNow)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != NotDue || tr.boxes != 0 {
		t.Fatalf("Outcome = %+v, boxes = %d, want NotDue and no Box", out, tr.boxes)
	}

	// Once the interval has passed with the same Records, it is due.
	out, err = tr.sweepTuning(t, tuningPolicy(root, 1), tuningNow.Add(25*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != Swept {
		t.Fatalf("Outcome = %+v, want Swept after the interval", out)
	}
}

func TestSweep_Tuning_NotDueTooFewRecords(t *testing.T) {
	readRecords := testutil.InstallPipeReporter(t)
	root := t.TempDir()
	seedRecords(t, root, 1, 5, tuningNow.Add(-48*time.Hour))
	tr := newTuningRun(t)

	out, err := tr.sweepTuning(t, tuningPolicy(root, 20), tuningNow)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != NotDue || tr.boxes != 0 {
		t.Fatalf("Outcome = %+v, boxes = %d, want NotDue and no Box", out, tr.boxes)
	}
	if len(out.Reasons) != 1 || !strings.Contains(out.Reasons[0], "too few") {
		t.Errorf("Reasons = %v, want a too-few-records reason", out.Reasons)
	}
	recs := readRecords()
	wantAt := tuningNow.Add(chore.RecordsRecheck)
	if len(recs) != 1 || !recs[0].NextDue.At.Equal(wantAt) || recs[0].NextDue.OnTipMove {
		t.Errorf("not_due records = %+v, want one dated at %v and not on_tip_move", recs, wantAt)
	}

	out, err = tr.sweepTuning(t, tuningPolicy(root, 5), tuningNow)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != Swept {
		t.Fatalf("Outcome = %+v, want Swept with exactly the minimum Records", out)
	}
}

func TestSweep_Tuning_NeverDueWithoutStore(t *testing.T) {
	for name, root := range map[string]string{"no root": "", "missing db": t.TempDir()} {
		t.Run(name, func(t *testing.T) {
			tr := newTuningRun(t)
			out, err := tr.sweepTuning(t, tuningPolicy(root, 0), tuningNow)
			if err != nil {
				t.Fatal(err)
			}
			if out.Kind != NotDue || tr.boxes != 0 {
				t.Fatalf("Outcome = %+v, boxes = %d, want NotDue and no Box", out, tr.boxes)
			}
			if root != "" {
				if _, err := os.Stat(hostpaths.DispatchRecordsDB(root)); !os.IsNotExist(err) {
					t.Errorf("the due check created the store (stat err = %v)", err)
				}
			}
		})
	}
}

func TestSweep_Tuning_DueFilesWithBothLabels(t *testing.T) {
	root := t.TempDir()
	seedRecords(t, root, 1, 3, tuningNow.Add(-48*time.Hour))
	tr := newTuningRun(t)

	out, err := tr.sweepTuning(t, tuningPolicy(root, 3), tuningNow)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != Swept || out.Filed != 1 {
		t.Fatalf("Outcome = %+v, want Swept with 1 filed", out)
	}

	if len(tr.input) != 1 {
		t.Fatalf("Box dispatched %d times, want 1", len(tr.input))
	}
	for _, want := range []string{
		"Window: after", "| Anchor | Metric | n | Window | Baseline | Δ | Flag |",
		"| role:implement:avg-usd |", "| role:review:avg-usd |", "thin",
	} {
		if !strings.Contains(tr.input[0], want) {
			t.Errorf("Chore input lacks %q:\n%s", want, tr.input[0])
		}
	}

	if len(tr.fc.PostIssueCalls) != 1 {
		t.Fatalf("PostIssueCalls = %+v, want 1", tr.fc.PostIssueCalls)
	}
	call := tr.fc.PostIssueCalls[0]
	for _, l := range []string{"agent-butler-finding", doctor.TuningFindingLabel} {
		if !slices.Contains(call.Labels, l) {
			t.Errorf("labels = %v, want %q", call.Labels, l)
		}
	}
	if !strings.Contains(call.Body, "chore=tuning") {
		t.Errorf("body lacks the chore=tuning marker term:\n%s", call.Body)
	}
	created := false
	for _, c := range tr.fc.CreateLabelCalls {
		created = created || c.Name == doctor.TuningFindingLabel
	}
	if !created {
		t.Errorf("CreateLabelCalls = %+v, want %q created when missing", tr.fc.CreateLabelCalls, doctor.TuningFindingLabel)
	}
}

func TestSweep_Tuning_DoneAdvancesCursor(t *testing.T) {
	root := t.TempDir()
	first := seedRecords(t, root, 1, 3, tuningNow.Add(-72*time.Hour))
	tr := newTuningRun(t)

	if out, err := tr.sweepTuning(t, tuningPolicy(root, 3), tuningNow.Add(-48*time.Hour)); err != nil || out.Kind != Swept {
		t.Fatalf("first Sweep = %+v, %v, want Swept", out, err)
	}
	tip, err := tr.backend.Read("tuning")
	if err != nil {
		t.Fatal(err)
	}
	if tip.State.Phase != ledger.Done || tip.State.Cursor != first[2] {
		t.Fatalf("tip = %+v, want Done with cursor %q", tip.State, first[2])
	}

	// Two more Records are fewer than the minimum of 3, so not due; a third
	// makes it due, and only the three new ones are in the window.
	second := seedRecords(t, root, 10, 2, tuningNow.Add(-24*time.Hour))
	if out, err := tr.sweepTuning(t, tuningPolicy(root, 3), tuningNow); err != nil || out.Kind != NotDue {
		t.Fatalf("Sweep with 2 new Records = %+v, %v, want NotDue", out, err)
	}
	second = append(second, seedRecords(t, root, 20, 1, tuningNow.Add(-12*time.Hour))...)
	if out, err := tr.sweepTuning(t, tuningPolicy(root, 3), tuningNow); err != nil || out.Kind != Swept {
		t.Fatalf("Sweep with 3 new Records = %+v, %v, want Swept", out, err)
	}
	if got := tr.input[len(tr.input)-1]; !strings.Contains(got, "3 settled Records") || !strings.Contains(got, "through "+second[2]) {
		t.Errorf("second input should cover only the 3 new Records through %s:\n%s", second[2], got)
	}
	tip, err = tr.backend.Read("tuning")
	if err != nil {
		t.Fatal(err)
	}
	if tip.State.Cursor != second[2] {
		t.Errorf("Cursor = %q, want the latest swept Record %q", tip.State.Cursor, second[2])
	}
}

// The Chore input carries a concrete baseline and Δ once older settled
// Records sit before the cursor: the earlier Records are the baseline, the
// ones settled after the cursor the window.
func TestSweep_Tuning_InputCarriesBaselineAndDelta(t *testing.T) {
	root := t.TempDir()
	// Baseline: 3 Records at $3 each (implement $2 + review $1).
	base := seedRecords(t, root, 1, 3, tuningNow.Add(-72*time.Hour))
	// Window: 3 Records at $5 each (implement $4 + review $1), ingested later
	// so they settle after the cursor.
	seedRecordsCost(t, root, 10, 3, tuningNow.Add(-24*time.Hour), 4)
	tr := newTuningRun(t)
	seedTuningDone(t, tr, tuningNow.Add(-48*time.Hour), base[2])

	if out, err := tr.sweepTuning(t, tuningPolicy(root, 3), tuningNow); err != nil || out.Kind != Swept {
		t.Fatalf("Sweep = %+v, %v, want Swept", out, err)
	}
	if len(tr.input) != 1 {
		t.Fatalf("Box dispatched %d times, want 1", len(tr.input))
	}
	for _, want := range []string{
		"| summary:usd-per-record | USD per Record | 3 | $5.00 | $3.00 | +2.00 | thin |",
		"| role:implement:avg-usd | implement avg USD per pass | 3 | $4.00 | $2.00 | +2.00 | thin |",
		"| role:implement:passes-per-record | implement passes per Record | 3 | 1.0 | 1.0 | +0.0 | thin |",
	} {
		if !strings.Contains(tr.input[0], want) {
			t.Errorf("Chore input lacks %q:\n%s", want, tr.input[0])
		}
	}
}

// settle (merge hold, dedup marker) keys off doctor.TuningFindingLabel while
// the catalog row declares the label the host applies; they must agree.
func TestCatalogTuningFindingLabelMatchesDoctorConst(t *testing.T) {
	cs, err := chore.Load(chore.Knobs{Chores: "tuning"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cs[0].FindingLabel; got != doctor.TuningFindingLabel {
		t.Errorf("catalog tuning FindingLabel = %q, want doctor.TuningFindingLabel %q", got, doctor.TuningFindingLabel)
	}
	if !cs[0].Records {
		t.Error("catalog tuning row must be records-scoped")
	}
}
