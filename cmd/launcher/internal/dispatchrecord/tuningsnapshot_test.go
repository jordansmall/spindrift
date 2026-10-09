package dispatchrecord

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/hostpaths"
)

func TestTuningSnapshotRoundTrips(t *testing.T) {
	s := openStore(t, t.TempDir())
	want := TuningSnapshot{
		RecordID:  "butler:butler-tuning@1",
		SHA256:    "abc123",
		Rendered:  "# digest\nrow 1\n",
		CreatedAt: time.UnixMilli(1_700_000_000_123).UTC(),
	}
	if err := s.PutTuningSnapshot(want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.TuningSnapshot(want.RecordID)
	if err != nil || !ok {
		t.Fatalf("TuningSnapshot = %+v, ok=%v, err=%v", got, ok, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestTuningSnapshotMissingIDIsNotFound(t *testing.T) {
	s := openStore(t, t.TempDir())
	got, ok, err := s.TuningSnapshot("nope")
	if err != nil || ok || got != (TuningSnapshot{}) {
		t.Fatalf("got %+v, ok=%v, err=%v", got, ok, err)
	}
}

// A Record ID is minted once per sweep, so a second snapshot under one would
// mean two sweeps shared an ID.
func TestPutTuningSnapshotRejectsDuplicateRecordID(t *testing.T) {
	s := openStore(t, t.TempDir())
	first := TuningSnapshot{RecordID: "r", SHA256: "a", Rendered: "one", CreatedAt: time.UnixMilli(1)}
	if err := s.PutTuningSnapshot(first); err != nil {
		t.Fatal(err)
	}
	if err := s.PutTuningSnapshot(TuningSnapshot{RecordID: "r", SHA256: "b", Rendered: "two", CreatedAt: time.UnixMilli(2)}); err == nil {
		t.Fatal("duplicate Put succeeded")
	}
	got, _, _ := s.TuningSnapshot("r")
	if got.Rendered != "one" {
		t.Fatalf("first snapshot overwritten: %+v", got)
	}
}

func TestStoreMigratesV10DatabaseAddingTuningSnapshots(t *testing.T) {
	root := t.TempDir()
	path := hostpaths.DispatchRecordsDB(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := append([]string{}, migrations[:10]...)
	stmts = append(stmts, "PRAGMA user_version = 10",
		`INSERT INTO records (record_id, kind, dispatch_key, claim_time, attribution, outcome) VALUES ('work:1@x', 'work', '1', 1000, 'inferred', 'unknown')`)
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s := openStore(t, root)
	if r := onlyRecord(t, s); r.ID != "work:1@x" {
		t.Fatalf("record = %+v", r)
	}
	if err := s.PutTuningSnapshot(TuningSnapshot{RecordID: "r", SHA256: "a", Rendered: "x", CreatedAt: time.UnixMilli(5)}); err != nil {
		t.Fatal(err)
	}
}
