package dispatchrecord

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/hostpaths"
)

func putLog(t *testing.T, root, name string, lines ...string) string {
	t.Helper()
	dir := hostpaths.LogDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "")), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func appendLog(t *testing.T, p string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Join(lines, "")); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func workLog(ts string, cost float64) []string {
	return []string{
		opLine(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}),
		assistant("a1"),
		result(ts, cost, 3, 1000, 800, "opus", "haiku"),
		opLine(claude.SpindriftOp{Op: "verdict", Verdict: "ready"}),
	}
}

func openStore(t *testing.T, root string) *Store {
	t.Helper()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func ingest(t *testing.T, s *Store) int {
	t.Helper()
	n, err := s.Ingest()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func records(t *testing.T, s *Store) []Record {
	t.Helper()
	recs, err := s.Records()
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func ids(recs []Record) []string {
	out := []string{}
	for _, r := range recs {
		out = append(out, r.ID)
	}
	return out
}

func TestStoreIngestRoundTripsParsedRecord(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-12.log", workLog("2026-03-01T10:00:00.123Z", 1.5)...)
	want, _, err := ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	s := openStore(t, root)
	if n := ingest(t, s); n != 1 {
		t.Fatalf("parsed = %d, want 1", n)
	}
	got := records(t, s)
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, want)
	}
	if got[0].ClaimTime.Location() != time.UTC {
		t.Errorf("claim time not UTC: %v", got[0].ClaimTime.Location())
	}
}

func TestStoreIngestIdempotentAndSkipsUnchanged(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-12.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	putLog(t, root, "issue-13.log", workLog("2026-03-02T10:00:00.000Z", 2)...)
	s := openStore(t, root)
	if n := ingest(t, s); n != 2 {
		t.Fatalf("first parsed = %d, want 2", n)
	}
	first := records(t, s)
	if n := ingest(t, s); n != 0 {
		t.Fatalf("second parsed = %d, want 0", n)
	}
	if !reflect.DeepEqual(first, records(t, s)) {
		t.Fatal("records changed across a no-op ingest")
	}
	s2 := openStore(t, root)
	if n := ingest(t, s2); n != 0 {
		t.Fatalf("reopened parsed = %d, want 0", n)
	}
}

func TestStoreReparsesAppendedLogIntoSameRecord(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-12.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	s := openStore(t, root)
	ingest(t, s)
	before := records(t, s)

	appendLog(t, p,
		opLine(claude.SpindriftOp{Op: "pass_start", Pass: 2, Role: "review"}),
		result("2026-03-01T10:05:00.000Z", 0.5, 1, 10, 5, "opus"))

	if n := ingest(t, s); n != 1 {
		t.Fatalf("parsed = %d, want 1", n)
	}
	after := records(t, s)
	if len(after) != 1 || after[0].ID != before[0].ID {
		t.Fatalf("want one record with id %s, got %v", before[0].ID, ids(after))
	}
	if len(after[0].Passes) != 2 || len(before[0].Passes) != 1 {
		t.Fatalf("passes before=%d after=%d, want 1 then 2", len(before[0].Passes), len(after[0].Passes))
	}
}

func TestStoreRenamedPriorRunStaysOneRecord(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-12.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	s := openStore(t, root)
	ingest(t, s)
	first := records(t, s)

	if err := os.Rename(p, p+".prior-run.1"); err != nil {
		t.Fatal(err)
	}
	ingest(t, s)
	if got := records(t, s); !reflect.DeepEqual(first, got) {
		t.Fatalf("rename changed records: %v", ids(got))
	}

	putLog(t, root, "issue-12.log", workLog("2026-03-05T10:00:00.000Z", 3)...)
	ingest(t, s)
	if got := records(t, s); len(got) != 2 {
		t.Fatalf("records = %v, want 2", ids(got))
	}
}

func TestStoreKeepsRecordWhenLogDeleted(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-12.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	s := openStore(t, root)
	ingest(t, s)
	first := records(t, s)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if n := ingest(t, s); n != 0 {
		t.Fatalf("parsed = %d, want 0", n)
	}
	if !reflect.DeepEqual(first, records(t, s)) {
		t.Fatal("record lost after its log was deleted")
	}
}

func TestStoreIgnoresFixAndConflictLogs(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-12.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	putLog(t, root, "issue-12-fix-1.log", workLog("2026-03-01T11:00:00.000Z", 1)...)
	putLog(t, root, "issue-12-conflict-resolve.log", workLog("2026-03-01T12:00:00.000Z", 1)...)
	putLog(t, root, "notes.txt", "x")
	s := openStore(t, root)
	if n := ingest(t, s); n != 1 {
		t.Fatalf("parsed = %d, want 1", n)
	}
	if got := records(t, s); len(got) != 1 {
		t.Fatalf("records = %v, want 1", ids(got))
	}
}

func TestStoreNoLogDirIsEmpty(t *testing.T) {
	s := openStore(t, t.TempDir())
	if n := ingest(t, s); n != 0 {
		t.Fatalf("parsed = %d", n)
	}
	if got := records(t, s); got == nil || len(got) != 0 {
		t.Fatalf("records = %#v, want empty non-nil", got)
	}
}

func TestStoreOrdersByClaimTimeThenID(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-b.log", workLog("2026-03-02T10:00:00.000Z", 1)...)
	putLog(t, root, "issue-a.log", workLog("2026-03-02T10:00:00.000Z", 1)...)
	putLog(t, root, "issue-c.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	s := openStore(t, root)
	ingest(t, s)
	var keys []string
	for _, r := range records(t, s) {
		keys = append(keys, r.DispatchKey)
	}
	if want := []string{"c", "a", "b"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("order = %v, want %v", keys, want)
	}
}

func TestStoreSchemaVersionAndWAL(t *testing.T) {
	root := t.TempDir()
	s := openStore(t, root)
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 1 {
		t.Fatalf("user_version = %d, err %v; want 1", v, err)
	}
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, err %v", mode, err)
	}
	if _, err := os.Stat(filepath.Join(root, ".spindrift", "dispatch-records.db")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2 := openStore(t, root)
	if err := s2.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 1 {
		t.Fatalf("reopened user_version = %d, err %v", v, err)
	}
}

func TestStoreMtimeFallbackIDChangeReplacesRecord(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-5.log", opLine(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}))
	s := openStore(t, root)
	ingest(t, s)
	if got := records(t, s); len(got) != 1 {
		t.Fatalf("records = %v, want 1", ids(got))
	}

	appendLog(t, p, assistant("a1"), result("2026-10-07T12:00:00.000Z", 1, 1, 10, 5, "opus"))

	ingest(t, s)
	got := records(t, s)
	if want := []string{"work:5@2026-10-07T12:00:00.000Z"}; !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("records = %v, want %v", ids(got), want)
	}
	if len(got[0].Passes) != 1 {
		t.Fatalf("passes = %d, want 1 (stale record's passes must go too)", len(got[0].Passes))
	}
}

func TestStoreRotationKeepsBothRecords(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-5.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	s := openStore(t, root)
	ingest(t, s)

	if err := os.Rename(p, p+".prior-run.1"); err != nil {
		t.Fatal(err)
	}
	putLog(t, root, "issue-5.log", workLog("2026-03-05T10:00:00.000Z", 3)...)
	ingest(t, s)

	got := records(t, s)
	want := []string{"work:5@2026-03-01T10:00:00.000Z", "work:5@2026-03-05T10:00:00.000Z"}
	if !reflect.DeepEqual(ids(got), want) {
		t.Fatalf("records = %v, want %v", ids(got), want)
	}
	for _, r := range got {
		if len(r.Passes) != 1 {
			t.Errorf("%s passes = %d, want 1", r.ID, len(r.Passes))
		}
	}
}

func TestStoreKeepsRecordWhenDeletedLogPathIsReused(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-12.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	s := openStore(t, root)
	ingest(t, s)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	ingest(t, s)
	putLog(t, root, "issue-12.log", workLog("2026-03-05T10:00:00.000Z", 3)...)
	if n := ingest(t, s); n != 1 {
		t.Fatalf("parsed = %d, want 1", n)
	}
	want := []string{"work:12@2026-03-01T10:00:00.000Z", "work:12@2026-03-05T10:00:00.000Z"}
	if got := ids(records(t, s)); !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %v, want %v", got, want)
	}
}

func TestStoreOverwrittenLogKeepsEarlierTimestampedRecord(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-12.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	s := openStore(t, root)
	ingest(t, s)
	putLog(t, root, "issue-12.log", workLog("2026-03-05T10:00:00.000Z", 3)...)
	ingest(t, s)
	if got := len(records(t, s)); got != 2 {
		t.Fatalf("records = %d, want 2", got)
	}
}

func TestStoreGrowFromProvisionalLeavesOneRecord(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-5.log", opLine(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}))
	s := openStore(t, root)
	ingest(t, s)
	appendLog(t, p, result("2026-10-07T12:00:00.000Z", 1, 1, 10, 5, "opus"))
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	ingest(t, s)
	if got, want := ids(records(t, s)), []string{"work:5@2026-10-07T12:00:00.000Z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %v, want %v", got, want)
	}
}

func TestStoreKeepsProvisionalRecordWhenDeletedLogPathIsReused(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-5.log", opLine(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}))
	s := openStore(t, root)
	ingest(t, s)
	provisional := ids(records(t, s))
	if len(provisional) != 1 {
		t.Fatalf("records = %v, want 1", provisional)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	ingest(t, s)
	putLog(t, root, "issue-5.log", workLog("2026-03-05T10:00:00.000Z", 3)...)
	ingest(t, s)
	got := ids(records(t, s))
	sort.Strings(got)
	want := []string{"work:5@2026-03-05T10:00:00.000Z", provisional[0]}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %v, want the provisional kept beside the new one: %v", got, want)
	}
}

func TestStoreTruncatedProvisionalLogKeepsRecord(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-5.log", opLine(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}))
	s := openStore(t, root)
	ingest(t, s)
	before := ids(records(t, s))
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	if n := ingest(t, s); n != 1 {
		t.Fatalf("parsed = %d, want 1", n)
	}
	if got := ids(records(t, s)); !reflect.DeepEqual(got, before) {
		t.Fatalf("records = %v, want %v", got, before)
	}

	// Emptied in place, the log is no longer the provisional Dispatch, so what
	// it regrows into is a new Record beside the old one.
	appendLog(t, p, workLog("2026-03-05T10:00:00.000Z", 3)...)
	ingest(t, s)
	got := ids(records(t, s))
	sort.Strings(got)
	want := append(before, "work:5@2026-03-05T10:00:00.000Z")
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("regrown records = %v, want %v", got, want)
	}
}

func TestStoreLogDirRemovedKeepsProvisionalRecord(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-5.log", opLine(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}))
	s := openStore(t, root)
	ingest(t, s)
	provisional := ids(records(t, s))
	if len(provisional) != 1 {
		t.Fatalf("records = %v, want 1", provisional)
	}
	if err := os.RemoveAll(hostpaths.LogDir(root)); err != nil {
		t.Fatal(err)
	}
	ingest(t, s)
	putLog(t, root, "issue-5.log", workLog("2026-03-05T10:00:00.000Z", 3)...)
	ingest(t, s)
	got := ids(records(t, s))
	sort.Strings(got)
	want := []string{"work:5@2026-03-05T10:00:00.000Z", provisional[0]}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %v, want the provisional kept beside the new one: %v", got, want)
	}
}

func TestStoreProvisionalReplacedWhenPathReusedWithoutIngest(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-5.log", opLine(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}))
	s := openStore(t, root)
	ingest(t, s)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	putLog(t, root, "issue-5.log", workLog("2026-03-05T10:00:00.000Z", 3)...)
	ingest(t, s)
	if got, want := ids(records(t, s)), []string{"work:5@2026-03-05T10:00:00.000Z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %v, want %v", got, want)
	}
}

func TestStoreEventFreeLogYieldsNoRecordUntilItGrows(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-5.log")
	s := openStore(t, root)
	if n := ingest(t, s); n != 1 {
		t.Fatalf("parsed = %d, want 1", n)
	}
	if got := records(t, s); len(got) != 0 {
		t.Fatalf("records = %v, want none", ids(got))
	}
	if n := ingest(t, s); n != 0 {
		t.Fatalf("second parsed = %d, want 0", n)
	}

	if err := os.WriteFile(p, []byte(strings.Join(workLog("2026-03-01T10:00:00.000Z", 1), "")), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := ingest(t, s); n != 1 {
		t.Fatalf("grown parsed = %d, want 1", n)
	}
	if got, want := ids(records(t, s)), []string{"work:5@2026-03-01T10:00:00.000Z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %v, want %v", got, want)
	}
}
