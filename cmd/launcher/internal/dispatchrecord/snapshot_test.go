package dispatchrecord

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/hostpaths"
)

func TestSnapshotRoundTrips(t *testing.T) {
	t.Parallel()
	s := openStore(t, t.TempDir())
	want := Snapshot{
		Chore:     "tuning",
		RecordID:  "butler:butler-tuning@1",
		SHA256:    "abc123",
		Rendered:  "# digest\nrow 1\n",
		CreatedAt: time.UnixMilli(1_700_000_000_123).UTC(),
	}
	if err := s.PutSnapshot(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Snapshot(want.Chore, want.RecordID)
	if err != nil || !ok {
		t.Fatalf("Snapshot = %+v, ok=%v, err=%v", got, ok, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestSnapshotMissingIDIsNotFound(t *testing.T) {
	t.Parallel()
	s := openStore(t, t.TempDir())
	got, ok, err := s.Snapshot("tuning", "nope")
	if err != nil || ok || got != (Snapshot{}) {
		t.Fatalf("got %+v, ok=%v, err=%v", got, ok, err)
	}
}

// A Record ID is minted once per sweep, so a second snapshot under one would
// mean two sweeps shared an ID.
func TestPutSnapshotRejectsDuplicateChoreAndRecordID(t *testing.T) {
	t.Parallel()
	s := openStore(t, t.TempDir())
	first := Snapshot{Chore: "tuning", RecordID: "r", SHA256: "a", Rendered: "one", CreatedAt: time.UnixMilli(1)}
	if err := s.PutSnapshot(first); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSnapshot(Snapshot{Chore: "tuning", RecordID: "r", SHA256: "b", Rendered: "two", CreatedAt: time.UnixMilli(2)}); err == nil {
		t.Fatal("duplicate Put succeeded")
	}
	got, _, _ := s.Snapshot("tuning", "r")
	if got.Rendered != "one" {
		t.Fatalf("first snapshot overwritten: %+v", got)
	}
}

func TestSnapshotsOfTwoChoresShareARecordIDWithoutColliding(t *testing.T) {
	t.Parallel()
	s := openStore(t, t.TempDir())
	for _, chore := range []string{"tuning", "noprior"} {
		snap := Snapshot{Chore: chore, RecordID: "r", SHA256: "sha-" + chore, Rendered: "body " + chore, CreatedAt: time.UnixMilli(7).UTC()}
		if err := s.PutSnapshot(snap); err != nil {
			t.Fatalf("%s: %v", chore, err)
		}
	}
	for _, chore := range []string{"tuning", "noprior"} {
		got, ok, err := s.Snapshot(chore, "r")
		if err != nil || !ok || got.Chore != chore || got.Rendered != "body "+chore {
			t.Fatalf("%s: got %+v, ok=%v, err=%v", chore, got, ok, err)
		}
	}
}

// seedDatabase writes a database at schema version n under root: the first n
// migrations, user_version n, then the extra statements.
func seedDatabase(t *testing.T, root string, n int, extra ...string) {
	t.Helper()
	path := hostpaths.DispatchRecordsDB(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := append([]string{}, migrations[:n]...)
	stmts = append(stmts, fmt.Sprintf("PRAGMA user_version = %d", n))
	stmts = append(stmts, extra...)
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStoreMigratesV10DatabaseAddingSnapshots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	seedDatabase(t, root, 10,
		`INSERT INTO records (record_id, kind, dispatch_key, claim_time, attribution, outcome) VALUES ('work:1@x', 'work', '1', 1000, 'inferred', 'unknown')`)

	s := openStore(t, root)
	if r := onlyRecord(t, s); r.ID != "work:1@x" {
		t.Fatalf("record = %+v", r)
	}
	if err := s.PutSnapshot(Snapshot{Chore: "tuning", RecordID: "r", SHA256: "a", Rendered: "x", CreatedAt: time.UnixMilli(5)}); err != nil {
		t.Fatal(err)
	}
}

// Pre-upgrade tuning snapshots carry no Chore column; the migration must
// backfill them to "tuning" and drop the old table.
func TestStoreMigratesV12TuningSnapshotsIntoSnapshots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	seedDatabase(t, root, 12,
		`INSERT INTO tuning_snapshots (record_id, sha256, rendered, created_at) VALUES ('butler:butler-tuning@1', 'abc123', '# digest', 1700000000123)`)

	s := openStore(t, root)
	want := Snapshot{Chore: "tuning", RecordID: "butler:butler-tuning@1", SHA256: "abc123", Rendered: "# digest", CreatedAt: time.UnixMilli(1_700_000_000_123).UTC()}
	got, ok, err := s.Snapshot("tuning", want.RecordID)
	if err != nil || !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("Snapshot = %+v, ok=%v, err=%v; want %+v", got, ok, err, want)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'tuning_snapshots'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("tuning_snapshots still present: n=%d, err=%v", n, err)
	}
}
