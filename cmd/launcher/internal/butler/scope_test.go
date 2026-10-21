package butler

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/chore"
	"spindrift.dev/launcher/internal/dispatch"
	"spindrift.dev/launcher/internal/dispatchrecord"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/ledger"
	"spindrift.dev/launcher/internal/ledger/ledgertest"
	"spindrift.dev/launcher/internal/recordsdigest"
	"spindrift.dev/launcher/internal/tuning"
)

func TestTreeSourceDue(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	cfg := chore.DueConfig{Every: time.Hour, ClaimTimeout: time.Hour}
	src := &treeSource{tree: fakeTree{}, head: "head1"}

	moved := ledger.Tip{State: ledger.State{LastSwept: "old", Phase: ledger.Done}}
	if got := src.Due(moved, nil, now, chore.Room{}, cfg); got != chore.Due {
		t.Errorf("head moved since last sweep: got %v, want Due", got)
	}

	rotated := ledger.Tip{State: ledger.State{LastSwept: "head1", Phase: ledger.Done}}
	if got := src.Due(rotated, nil, now, chore.Room{}, cfg); got != chore.NothingToScan {
		t.Errorf("head unchanged and cursor rotated: got %v, want NothingToScan", got)
	}

	midWalk := ledger.Tip{State: ledger.State{LastSwept: "head1", Cursor: "a/b", Phase: ledger.Done}}
	if got := src.Due(midWalk, nil, now, chore.Room{}, cfg); got != chore.Due {
		t.Errorf("head unchanged but cursor mid-walk: got %v, want Due", got)
	}
}

func TestTreeSourceNextDue(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	cfg := chore.DueConfig{Every: time.Hour, ClaimTimeout: time.Hour}
	src := &treeSource{tree: fakeTree{}, head: "head1"}

	rotated := ledger.Tip{State: ledger.State{LastSwept: "head1", Phase: ledger.Done}}
	if at, onTipMove := src.NextDue(rotated, nil, now, chore.Room{}, cfg); !at.IsZero() || !onTipMove {
		t.Errorf("NothingToScan: got (%v, %v), want (zero, true)", at, onTipMove)
	}

	moved := ledger.Tip{State: ledger.State{LastSwept: "old", Phase: ledger.Done}}
	if at, onTipMove := src.NextDue(moved, nil, now, chore.Room{}, cfg); !at.Equal(now) || onTipMove {
		t.Errorf("Due: got (%v, %v), want (now, false)", at, onTipMove)
	}
}

func TestTreeSourcePrepare(t *testing.T) {
	claim := ledger.Tip{State: ledger.State{LastSwept: "old", Phase: ledger.Claimed}}
	src := &treeSource{tree: fakeTree{files: []string{"b.go", "a.go"}}, head: "head1"}

	if err := src.Stage(); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	got, err := src.Prepare(claim)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	want := chore.Scope{Head: "head1", DiffRange: "old..head1", Slice: []string{"a.go", "b.go"}}
	if !reflect.DeepEqual(got.scope, want) {
		t.Errorf("scope = %+v, want %+v", got.scope, want)
	}
	if got.input != "" || !got.claimTime.IsZero() || got.arm != nil {
		t.Errorf("tree source must hand no input, claim time or settle hook: %+v", got)
	}
}

func TestTreeSourceStageSurfacesTrackedFilesError(t *testing.T) {
	boom := errors.New("ls-tree failed")
	src := &treeSource{tree: fakeTree{filesErr: boom}, head: "head1"}
	if err := src.Stage(); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}

// newTestRecordsSource is a recordsSource over a store at root seeded with n
// settled Records, built the way the tuning sweep tests build their Runner,
// returned with that Runner and the Record IDs in order.
func newTestRecordsSource(t *testing.T, root string, n, minRecords int) (recordsSource, *Runner, []string) {
	t.Helper()
	ids := seedRecords(t, root, 1, n, tuningNow.Add(-48*time.Hour))
	it := forge.NewFake().AsIssueFiler()
	r := New(ledger.Local{Repo: ledgertest.NewRepo(t)}, fakeTree{}, it, nil, tuningPolicy(root, minRecords), func() time.Time { return tuningNow })
	src, err := newRecordsSource(r, "tuning", "head1", tuningNow)
	if err != nil {
		t.Fatal(err)
	}
	return src, r, ids
}

func TestRecordsSourceDue(t *testing.T) {
	t.Parallel()
	cfg := chore.DueConfig{Every: time.Hour, ClaimTimeout: time.Hour}
	src, _, ids := newTestRecordsSource(t, t.TempDir(), 5, 3)

	fresh := ledger.Tip{}
	if got := src.Due(fresh, nil, tuningNow, chore.Room{}, cfg); got != chore.Due {
		t.Errorf("5 new settled Records, min 3: got %v, want Due", got)
	}

	pastThree := ledger.Tip{State: ledger.State{Cursor: ids[2], Phase: ledger.Done}}
	if got := src.Due(pastThree, nil, tuningNow, chore.Room{}, cfg); got != chore.TooFewRecords {
		t.Errorf("2 Records past the cursor, min 3: got %v, want TooFewRecords", got)
	}

	pastAll := ledger.Tip{State: ledger.State{Cursor: ids[4], Phase: ledger.Done}}
	if got := src.Due(pastAll, nil, tuningNow, chore.Room{}, cfg); got != chore.TooFewRecords {
		t.Errorf("no Records past the cursor: got %v, want TooFewRecords", got)
	}
}

func TestRecordsSourceNextDue(t *testing.T) {
	t.Parallel()
	cfg := chore.DueConfig{Every: time.Hour, ClaimTimeout: time.Hour}
	src, _, ids := newTestRecordsSource(t, t.TempDir(), 5, 3)

	if at, onTipMove := src.NextDue(ledger.Tip{}, nil, tuningNow, chore.Room{}, cfg); !at.Equal(tuningNow) || onTipMove {
		t.Errorf("Due: got (%v, %v), want (now, false)", at, onTipMove)
	}

	pastAll := ledger.Tip{State: ledger.State{Cursor: ids[4], Phase: ledger.Done}}
	want := tuningNow.Add(chore.RecordsRecheck)
	if at, onTipMove := src.NextDue(pastAll, nil, tuningNow, chore.Room{}, cfg); !at.Equal(want) || onTipMove {
		t.Errorf("TooFewRecords: got (%v, %v), want (%v, false)", at, onTipMove, want)
	}
}

func TestRecordsSourcePrepare(t *testing.T) {
	t.Parallel()
	src, _, ids := newTestRecordsSource(t, t.TempDir(), 5, 1)

	got, err := src.Prepare(ledger.Tip{State: ledger.State{Phase: ledger.Claimed}})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if want := (chore.Scope{Head: "head1", NextCursor: ids[4]}); !reflect.DeepEqual(got.scope, want) {
		t.Errorf("scope = %+v, want %+v", got.scope, want)
	}
	if want := "Window: after (start) through " + ids[4] + " — 5 settled Records"; !strings.Contains(got.input, want) {
		t.Errorf("input lacks %q:\n%s", want, got.input)
	}
	if !got.claimTime.Equal(tuningNow) {
		t.Errorf("claimTime = %v, want %v", got.claimTime, tuningNow)
	}
	if got.arm == nil {
		t.Error("records source must hand a settle hook")
	}
}

func TestRecordsSourcePrepareStartsPastCursor(t *testing.T) {
	t.Parallel()
	src, _, ids := newTestRecordsSource(t, t.TempDir(), 5, 1)

	got, err := src.Prepare(ledger.Tip{State: ledger.State{Cursor: ids[2], Phase: ledger.Claimed}})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got.scope.NextCursor != ids[4] {
		t.Errorf("NextCursor = %q, want %q", got.scope.NextCursor, ids[4])
	}
	if want := "Window: after " + ids[2] + " through " + ids[4] + " — 2 settled Records"; !strings.Contains(got.input, want) {
		t.Errorf("input lacks %q:\n%s", want, got.input)
	}
}

func TestRecordsSourceArmStoresSnapshot(t *testing.T) {
	t.Parallel()
	src, r, _ := newTestRecordsSource(t, t.TempDir(), 5, 1)
	p, err := src.Prepare(ledger.Tip{})
	if err != nil {
		t.Fatal(err)
	}

	d := dispatch.NewFake()
	d.RecordIDResult = dispatchrecord.RecordID(tuning.Strategy.Name, tuning.Strategy.Name, tuningNow)
	step := &settleRun{out: io.Discard}
	if err := p.arm(d, step); err != nil {
		t.Fatalf("arm: %v", err)
	}

	sum := sha256.Sum256([]byte(p.input))
	want := ledger.Snapshot{Sweep: d.RecordIDResult, SHA256: hex.EncodeToString(sum[:])}
	if step.records == nil || step.records.ref != want {
		t.Fatalf("step.records = %+v, want ref %+v", step.records, want)
	}
	snap, ok, err := r.policy.loadSnapshot(tuning.Strategy.Name, d.RecordIDResult)
	if err != nil || !ok {
		t.Fatalf("snapshot not stored: ok=%v err=%v", ok, err)
	}
	if snap.Rendered != p.input || snap.SHA256 != want.SHA256 || !snap.CreatedAt.Equal(tuningNow) {
		t.Errorf("stored snapshot = %+v, want the digest %q created at %v", snap, p.input, tuningNow)
	}
}

func TestRecordsSourceArmFailsClosedWithoutRecordID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	src, _, _ := newTestRecordsSource(t, root, 5, 1)
	p, err := src.Prepare(ledger.Tip{})
	if err != nil {
		t.Fatal(err)
	}

	step := &settleRun{out: io.Discard}
	err = p.arm(dispatch.NewFake(), step)
	if err == nil || !strings.HasPrefix(err.Error(), "butler: tuning: dispatch has no record ID") {
		t.Fatalf("err = %v, want the no-record-ID error", err)
	}
	if n := countSnapshots(t, root); n != 0 {
		t.Errorf("a failed arm must store nothing: snapshots rows = %d", n)
	}
	if step.records != nil {
		t.Error("a failed arm must leave the settle step unarmed")
	}
}

func TestRecordsSourceSnapshotsKeyedByChore(t *testing.T) {
	t.Parallel()
	_, r, _ := newTestRecordsSource(t, t.TempDir(), 5, 1)
	id := dispatchrecord.RecordID("shared", "shared", tuningNow)
	steps := map[string]*settleRun{tuning.Strategy.Name: {out: io.Discard}, noPriorChore: {out: io.Discard}}
	for name, step := range steps {
		src, err := newRecordsSource(r, name, "head1", tuningNow)
		if err != nil {
			t.Fatal(err)
		}
		d := dispatch.NewFake()
		d.RecordIDResult = id
		if err := src.armSnapshot(d, step, name+" digest"); err != nil {
			t.Fatalf("armSnapshot %s: %v", name, err)
		}
	}
	for name, step := range steps {
		if err := step.records.load(); err != nil || step.records.rendered != name+" digest" {
			t.Errorf("%s load: rendered %q, err %v; want %q", name, step.records.rendered, err, name+" digest")
		}
	}
}

// The due check reads the knob the Chore's row names, not a field of the
// Policy: the same three new Records are due at a floor of 3 and not at 4.
func TestRecordsSourceDueReadsTheRowNamedKnob(t *testing.T) {
	t.Parallel()
	cfg := chore.DueConfig{Every: time.Hour, ClaimTimeout: time.Hour}
	for _, tc := range []struct {
		min  string
		want chore.NotDue
	}{{"3", chore.Due}, {"4", chore.TooFewRecords}} {
		root := t.TempDir()
		seedRecords(t, root, 1, 3, tuningNow.Add(-48*time.Hour))
		p := tuningPolicy(root, 1)
		p.Knobs = mapKnobs(map[string]string{chore.MinRecordsKnob("tuning"): tc.min})
		r := New(ledger.Local{Repo: ledgertest.NewRepo(t)}, fakeTree{}, forge.NewFake().AsIssueFiler(), nil, p, func() time.Time { return tuningNow })
		src, err := newRecordsSource(r, "tuning", "head1", tuningNow)
		if err != nil {
			t.Fatal(err)
		}
		if got := src.Due(ledger.Tip{}, nil, tuningNow, chore.Room{}, cfg); got != tc.want {
			t.Errorf("3 new settled Records, min %s: got %v, want %v", tc.min, got, tc.want)
		}
	}
}

func openRecordsDB(t *testing.T, root string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", hostpaths.DispatchRecordsDB(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func countSnapshots(t *testing.T, root string) int {
	t.Helper()
	var n int
	if err := openRecordsDB(t, root).QueryRow("SELECT COUNT(*) FROM snapshots").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A claim made before the upgrade left its digest in tuning_snapshots; the
// settle after the upgrade must still load it, under the Chore's name.
func TestRecordsSnapshotLoadsRowStoredBeforeTheChoreKeyedTable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s, err := dispatchrecord.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	const digest = "pre-upgrade digest"
	sum := sha256.Sum256([]byte(digest))
	ref := ledger.Snapshot{Sweep: dispatchrecord.RecordID(tuning.Strategy.Name, tuning.Strategy.Name, tuningNow), SHA256: hex.EncodeToString(sum[:])}
	db := openRecordsDB(t, root)
	for _, q := range []string{
		"DROP TABLE snapshots",
		"ALTER TABLE passes DROP COLUMN efforts",
		"CREATE TABLE tuning_snapshots (record_id TEXT PRIMARY KEY, sha256 TEXT NOT NULL, rendered TEXT NOT NULL, created_at INTEGER NOT NULL)",
		"INSERT INTO tuning_snapshots VALUES ('" + ref.Sweep + "', '" + ref.SHA256 + "', '" + digest + "', 5)",
		"PRAGMA user_version = 12",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	snap := &recordsSnapshot{chore: tuning.Strategy.Name, ref: ref, policy: tuningPolicy(root, 1)}
	if err := snap.load(); err != nil || snap.rendered != digest {
		t.Fatalf("load: rendered %q, err %v; want %q", snap.rendered, err, digest)
	}
}

// An unset, empty or absent lookup takes the row default, the very value the
// row's generated reader resolves.
func TestNewRecordsSourceTakesTheRowDefaultMinRecords(t *testing.T) {
	t.Parallel()
	env := chore.MinRecordsKnob("tuning")
	for name, knobs := range map[string]recordsdigest.Lookup{
		"no lookup": nil,
		"unset":     mapKnobs(nil),
		"empty":     mapKnobs(map[string]string{env: ""}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := tuningPolicy(t.TempDir(), 1)
			p.Knobs = knobs
			r := New(ledger.Local{Repo: ledgertest.NewRepo(t)}, fakeTree{}, forge.NewFake().AsIssueFiler(), nil, p, func() time.Time { return tuningNow })
			src, err := newRecordsSource(r, "tuning", "head1", tuningNow)
			if err != nil {
				t.Fatal(err)
			}
			read := knobs
			if read == nil {
				read = mapKnobs(nil)
			}
			want, err := tuning.ReadKnobs(read)
			if err != nil {
				t.Fatal(err)
			}
			if src.minRecords != 20 || src.minRecords != want.ButlerTuningMinRecords {
				t.Errorf("minRecords = %d, want 20 and ReadKnobs' %d", src.minRecords, want.ButlerTuningMinRecords)
			}
		})
	}
}

// A malformed value fails both readers with the very same message.
func TestMinRecordsAndReadKnobsRejectAMalformedValueAlike(t *testing.T) {
	t.Parallel()
	lookup := mapKnobs(map[string]string{chore.MinRecordsKnob("tuning"): "many"})
	_, err := chore.MinRecords("tuning", lookup)
	_, readErr := tuning.ReadKnobs(lookup)
	if err == nil || readErr == nil || err.Error() != readErr.Error() {
		t.Fatalf("MinRecords err = %v, ReadKnobs err = %v; want the same error", err, readErr)
	}
}

func TestNewRecordsSourceErrorsOnAnUnresolvableKnob(t *testing.T) {
	t.Parallel()
	env := chore.MinRecordsKnob("tuning")
	for name, knobs := range map[string]recordsdigest.Lookup{
		"not an int":      mapKnobs(map[string]string{env: "many"}),
		"bad digest knob": mapKnobs(map[string]string{env: "1", "BUTLER_TUNING_COOLDOWN": "-1h"}),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := tuningPolicy(t.TempDir(), 1)
			p.Knobs = knobs
			r := New(ledger.Local{Repo: ledgertest.NewRepo(t)}, fakeTree{}, forge.NewFake().AsIssueFiler(), nil, p, func() time.Time { return tuningNow })
			_, err := newRecordsSource(r, "tuning", "head1", tuningNow)
			if err == nil || !strings.Contains(err.Error(), "tuning") || !strings.Contains(err.Error(), "BUTLER_TUNING_") {
				t.Fatalf("err = %v, want one naming the Chore and the knob", err)
			}
		})
	}
}
