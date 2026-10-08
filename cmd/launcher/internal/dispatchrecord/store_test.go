package dispatchrecord

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/hostpaths"
	"spindrift.dev/launcher/internal/passmachine"
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
	// Root is filled from the store at read time, not parsed from the log.
	want.Root = root
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
	got := records(t, s)
	if len(got) != 1 || got[0].ID != first[0].ID || len(got[0].Passes) != len(first[0].Passes) {
		t.Fatalf("rename changed records: %+v", got)
	}
	if want := "issue-12.log.prior-run.1"; got[0].Passes[0].Log != want {
		t.Fatalf("pass log = %q, want %q", got[0].Passes[0].Log, want)
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

func passLogs(r Record) []string {
	out := []string{}
	for _, p := range r.Passes {
		out = append(out, p.Log)
	}
	return out
}

func TestStoreSatellitesJoinTheirDispatch(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-12.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	putLog(t, root, "issue-12-fix-1.log", workLog("2026-03-01T11:00:00.000Z", 2)...)
	putLog(t, root, "issue-12-conflict-resolve.log", workLog("2026-03-01T12:00:00.000Z", 4)...)
	putLog(t, root, "notes.txt", "x")
	s := openStore(t, root)
	if n := ingest(t, s); n != 3 {
		t.Fatalf("parsed = %d, want 3", n)
	}
	got := records(t, s)
	if len(got) != 1 {
		t.Fatalf("records = %v, want 1", ids(got))
	}
	wantLogs := []string{"issue-12.log", "issue-12-fix-1.log", "issue-12-conflict-resolve.log"}
	if !reflect.DeepEqual(passLogs(got[0]), wantLogs) {
		t.Fatalf("pass logs = %v, want %v", passLogs(got[0]), wantLogs)
	}
	var usd []float64
	for i, p := range got[0].Passes {
		if p.Ordinal != i+1 {
			t.Errorf("pass %d ordinal = %d", i, p.Ordinal)
		}
		usd = append(usd, p.USD)
	}
	if want := []float64{1, 2, 4}; !reflect.DeepEqual(usd, want) {
		t.Fatalf("usd = %v, want %v", usd, want)
	}
}

func TestStoreSatelliteWindowedByStartTime(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-42.log.prior-run.1", workLog("2026-03-01T10:00:00.000Z", 1)...)
	putLog(t, root, "issue-42.log", workLog("2026-03-05T10:00:00.000Z", 1)...)
	putLog(t, root, "issue-42-fix-1.log", workLog("2026-03-05T11:00:00.000Z", 2)...)
	putLog(t, root, "issue-42-fix-1.log.prior-run.1", workLog("2026-03-02T11:00:00.000Z", 4)...)
	s := openStore(t, root)
	ingest(t, s)
	got := records(t, s)
	if len(got) != 2 {
		t.Fatalf("records = %v, want 2", ids(got))
	}
	want := [][]string{
		{"issue-42.log.prior-run.1", "issue-42-fix-1.log.prior-run.1"},
		{"issue-42.log", "issue-42-fix-1.log"},
	}
	for i := range got {
		if !reflect.DeepEqual(passLogs(got[i]), want[i]) {
			t.Errorf("record %d logs = %v, want %v", i, passLogs(got[i]), want[i])
		}
	}
}

func TestStoreRenamedSatelliteDoesNotDuplicatePasses(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-42.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	fix := putLog(t, root, "issue-42-fix-1.log", workLog("2026-03-01T11:00:00.000Z", 2)...)
	s := openStore(t, root)
	ingest(t, s)
	if err := os.Rename(fix, fix+".prior-run.1"); err != nil {
		t.Fatal(err)
	}
	if n := ingest(t, s); n != 1 {
		t.Fatalf("parsed = %d, want 1", n)
	}
	got := records(t, s)
	if len(got) != 1 {
		t.Fatalf("records = %v, want 1", ids(got))
	}
	want := []string{"issue-42.log", "issue-42-fix-1.log.prior-run.1"}
	if !reflect.DeepEqual(passLogs(got[0]), want) {
		t.Fatalf("pass logs = %v, want %v", passLogs(got[0]), want)
	}
}

func TestStoreGrownSatelliteReplacesItsOwnPasses(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-42.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	fix := putLog(t, root, "issue-42-fix-1.log", workLog("2026-03-01T11:00:00.000Z", 2)...)
	s := openStore(t, root)
	ingest(t, s)
	appendLog(t, fix, result("2026-03-01T11:30:00.000Z", 3, 1, 1, 1, "opus"))
	ingest(t, s)
	got := records(t, s)
	if len(got) != 1 || len(got[0].Passes) != 2 || got[0].Passes[1].USD != 5 {
		t.Fatalf("records after growth = %+v", got)
	}
}

func TestStoreOrphanSatelliteIsItsOwnRecord(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-42-fix-1.log", workLog("2026-03-01T11:00:00.000Z", 2)...)
	s := openStore(t, root)
	ingest(t, s)
	got := records(t, s)
	if len(got) != 1 || got[0].DispatchKey != "42" || got[0].Kind != "work" ||
		!reflect.DeepEqual(passLogs(got[0]), []string{"issue-42-fix-1.log"}) {
		t.Fatalf("records = %+v", got)
	}
	// A later satellite of the same key joins the orphan's window.
	putLog(t, root, "issue-42-conflict-resolve.log", workLog("2026-03-01T12:00:00.000Z", 4)...)
	ingest(t, s)
	if got := records(t, s); len(got) != 1 || len(got[0].Passes) != 2 {
		t.Fatalf("records = %+v, want one with 2 passes", got)
	}
}

func TestStoreSatelliteBeforeEveryDispatchIsOrphan(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-42.log", workLog("2026-03-05T10:00:00.000Z", 1)...)
	putLog(t, root, "issue-42-fix-1.log.prior-run.1", workLog("2026-03-01T11:00:00.000Z", 2)...)
	s := openStore(t, root)
	ingest(t, s)
	if got := records(t, s); len(got) != 2 {
		t.Fatalf("records = %v, want 2", ids(got))
	}
}

func TestStoreUnchangedSatellitesAreNotReparsed(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-42.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	putLog(t, root, "issue-42-fix-1.log", workLog("2026-03-01T11:00:00.000Z", 2)...)
	s := openStore(t, root)
	ingest(t, s)
	first := records(t, s)
	if n := ingest(t, s); n != 0 {
		t.Fatalf("second ingest parsed = %d, want 0", n)
	}
	if !reflect.DeepEqual(first, records(t, s)) {
		t.Fatal("records changed on an idle ingest")
	}
}

// seedV1DB creates a schema-v1 database under root and runs stmts against it.
func seedV1DB(t *testing.T, root string, stmts ...string) {
	t.Helper()
	path := hostpaths.DispatchRecordsDB(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range append([]string{migrations[0], `PRAGMA user_version = 1`}, stmts...) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreMigratesV1Database(t *testing.T) {
	root := t.TempDir()
	claim := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	seedV1DB(t, root,
		`INSERT INTO records VALUES ('work:7@2026-03-01T10:00:00.000Z', 'work', '7', `+fmt.Sprint(claim.UnixMilli())+`, 'inferred', 'unknown')`,
		`INSERT INTO passes VALUES ('work:7@2026-03-01T10:00:00.000Z', 1, 'implement', 'opus,haiku', 1.5, 1, 2, 3, 4, 5, 6, 7, 8, 'ready')`,
		`INSERT INTO ingested_files VALUES ('/gone/issue-7.log', 10, 20, 'work:7@2026-03-01T10:00:00.000Z', 0)`,
	)

	s := openStore(t, root)
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("user_version = %d, err %v; want %d", v, err, len(migrations))
	}
	want := Record{
		ID: "work:7@2026-03-01T10:00:00.000Z", Root: root, Kind: "work", DispatchKey: "7", ClaimTime: claim,
		Attribution: AttributionInferred, Outcome: OutcomeUnknown, OutcomeSource: OutcomeSourceNone,
		Passes: []Pass{{
			Ordinal: 1, Role: "implement", Models: []string{"opus", "haiku"}, USD: 1.5, InputTokens: 1,
			OutputTokens: 2, CacheReadInputTokens: 3, CacheCreationInputTokens: 4, APICalls: 5, Turns: 6,
			DurationMs: 7, APIDurationMs: 8, Verdict: "ready",
		}},
	}
	if got := records(t, s); !reflect.DeepEqual(got, []Record{want}) {
		t.Fatalf("migrated records = %+v\nwant %+v", got, []Record{want})
	}
	// The migrated file row is forgotten (its log is gone) without touching the Record.
	ingest(t, s)
	if got := records(t, s); len(got) != 1 || len(got[0].Passes) != 1 {
		t.Fatalf("records after ingest = %+v", got)
	}
}

func TestStoreMigratedV1RowsReparseStillPresentLogs(t *testing.T) {
	researchLines := []string{result("2026-03-01T10:00:00.000Z", 1, 1, 1, 1, "m"), outcomeLine("recommend")}
	crashLines := []string{"box: starting\n", "error: image pull failed\n"}
	mtime := time.Date(2026, 3, 2, 9, 8, 7, 0, time.UTC)
	writeLogs := func(root string) (research, crash string) {
		research = putLog(t, root, "issue-55.log.prior-run.1", researchLines...)
		crash = putLog(t, root, "issue-9.log", crashLines...)
		for _, p := range []string{research, crash} {
			if err := os.Chtimes(p, mtime, mtime); err != nil {
				t.Fatal(err)
			}
		}
		return research, crash
	}
	noRoot := func(recs []Record) []Record {
		out := append([]Record(nil), recs...)
		for i := range out {
			out[i].Root = ""
		}
		return out
	}

	freshRoot := t.TempDir()
	writeLogs(freshRoot)
	fresh := openStore(t, freshRoot)
	ingest(t, fresh)
	want := records(t, fresh)
	oldID := RecordID("unknown", "55", time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC))
	if oldID == "" || len(want) != 2 {
		t.Fatalf("fresh records = %+v, want a research Record and a crash Record", want)
	}

	root := t.TempDir()
	research, crash := writeLogs(root)
	claim := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	stat := func(p string) (int64, int64) {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		return info.Size(), info.ModTime().UnixNano()
	}
	rsize, rmtime := stat(research)
	csize, cmtime := stat(crash)
	seedV1DB(t, root,
		fmt.Sprintf(`INSERT INTO records VALUES ('%s', 'unknown', '55', %d, 'inferred', 'unknown')`, oldID, claim.UnixMilli()),
		fmt.Sprintf(`INSERT INTO passes VALUES ('%s', 1, 'implement', 'm', 1, 1, 1, 1, 1, 1, 1, 1, 1, '')`, oldID),
		fmt.Sprintf(`INSERT INTO ingested_files VALUES ('%s', %d, %d, '%s', 0)`, research, rsize, rmtime, oldID),
		fmt.Sprintf(`INSERT INTO ingested_files VALUES ('%s', %d, %d, '', 1)`, crash, csize, cmtime),
	)

	s := openStore(t, root)
	if n := ingest(t, s); n != 2 {
		t.Fatalf("parsed = %d, want 2: carried-over rows must be re-read", n)
	}
	if got := noRoot(records(t, s)); !reflect.DeepEqual(got, noRoot(want)) {
		t.Fatalf("records = %+v\nwant (fresh store) %+v", got, noRoot(want))
	}
}

func TestStoreMigratedV1RowKeepsRecordWhenPathReused(t *testing.T) {
	root := t.TempDir()
	march := "work:9@2026-03-01T10:00:00.000Z"
	claim := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	log := putLog(t, root, "issue-9.log", workLog("2026-04-01T10:00:00.000Z", 2)...)
	seedV1DB(t, root,
		fmt.Sprintf(`INSERT INTO records VALUES ('%s', 'work', '9', %d, 'inferred', 'unknown')`, march, claim.UnixMilli()),
		fmt.Sprintf(`INSERT INTO passes VALUES ('%s', 1, 'implement', 'opus', 1.5, 1, 2, 3, 4, 5, 6, 7, 8, 'ready')`, march),
		fmt.Sprintf(`INSERT INTO ingested_files VALUES ('%s', 10, 20, '%s', 0)`, log, march),
	)

	s := openStore(t, root)
	ingest(t, s)
	got := records(t, s)
	if len(got) != 2 || got[0].ID != march || len(got[0].Passes) != 1 {
		t.Fatalf("records = %v, want the March Record with its pass kept beside the April one", ids(got))
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
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("user_version = %d, err %v; want %d", v, err, len(migrations))
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
	if err := s2.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != len(migrations) {
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

func TestStoreReingestKeepsRecordWhenReusedPathChangedStatAndID(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-12.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	s := openStore(t, root)
	ingest(t, s)
	putLog(t, root, "issue-12.log", workLog("2026-03-05T10:00:00.000Z", 3)...)
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reingest(); err != nil {
		t.Fatal(err)
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

func reviewEvidenceLog(verdict string) []string {
	return []string{
		opLine(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "review"}),
		resultText("2026-10-07T12:00:00Z", verdict),
		opLine(claude.SpindriftOp{Op: "pass_start", Pass: 2, Role: "fix"}),
		assistantTool("f1", "Write", map[string]string{"file_path": passmachine.DispositionsPath, "content": "fixed: all"}),
		toolResult("f1", false),
		resultText("2026-10-07T12:01:00Z", "done"),
	}
}

func TestStoreRoundTripsReviewEvidence(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-7.log", reviewEvidenceLog("VERDICT: BLOCK")...)
	s := openStore(t, root)
	ingest(t, s)
	recs := records(t, s)
	if len(recs) != 1 || len(recs[0].Passes) != 2 {
		t.Fatalf("records = %+v", recs)
	}
	if got := recs[0].Passes[0].VerdictText; got != "VERDICT: BLOCK" {
		t.Fatalf("VerdictText = %q", got)
	}
	if got := recs[0].Passes[1].Dispositions; got != "fixed: all" {
		t.Fatalf("Dispositions = %q", got)
	}
}

func TestStoreReingestRepairsUnchangedStatLog(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-7.log", reviewEvidenceLog("VERDICT: BLOCK")...)
	gone := putLog(t, root, "issue-8.log", workLog("2026-10-07T13:00:00Z", 1)...)
	s := openStore(t, root)
	ingest(t, s)
	before := records(t, s)

	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	// Same size and mtime, different content: only a forced re-parse sees it.
	if err := os.WriteFile(p, []byte(strings.Join(reviewEvidenceLog("VERDICT: PASS!"), "")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if n := ingest(t, s); n != 0 {
		t.Fatalf("Ingest parsed %d, want 0 for a stat-identical log", n)
	}
	n, err := s.Reingest()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("Reingest parsed %d, want 1", n)
	}
	after := records(t, s)
	if len(after) != 2 {
		t.Fatalf("records = %v, want both kept", ids(after))
	}
	for i := range after {
		switch after[i].DispatchKey {
		case "7":
			if got := after[i].Passes[0].VerdictText; got != "VERDICT: PASS!" {
				t.Fatalf("VerdictText = %q, want repaired", got)
			}
		case "8":
			if !reflect.DeepEqual(after[i], before[i]) {
				t.Fatalf("record with deleted log changed: %+v vs %+v", after[i], before[i])
			}
		}
	}
}

func TestStoreReingestReplacesRecordWhoseIDChanged(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-7.log", workLog("2026-10-07T12:00:00Z", 1)...)
	s := openStore(t, root)
	ingest(t, s)
	want := ids(records(t, s))

	// Simulate an older parser that derived a different ID from the same file.
	const stale = "unknown:7@old"
	for _, q := range []string{
		"UPDATE records SET record_id = ?",
		"UPDATE passes SET record_id = ?",
		"UPDATE ingested_files SET record_id = ?",
	} {
		if _, err := s.db.Exec(q, stale); err != nil {
			t.Fatal(err)
		}
	}
	if n := ingest(t, s); n != 0 {
		t.Fatalf("Ingest parsed %d, want 0 for a stat-identical log", n)
	}
	if _, err := s.Reingest(); err != nil {
		t.Fatal(err)
	}
	if got := ids(records(t, s)); !reflect.DeepEqual(got, want) {
		t.Fatalf("records = %v, want only %v", got, want)
	}
	var passes int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM passes WHERE record_id = ?", stale).Scan(&passes); err != nil {
		t.Fatal(err)
	}
	if passes != 0 {
		t.Fatalf("%d passes left under the stale ID", passes)
	}
}

// restampRecordID renames every row of the lone stored Record to id, as if an
// older parser had derived that ID from the same logs.
func restampRecordID(t *testing.T, s *Store, id string) {
	t.Helper()
	for _, q := range []string{
		"UPDATE records SET record_id = ?",
		"UPDATE passes SET record_id = ?",
		"UPDATE prompt_hashes SET record_id = ?",
		"UPDATE ingested_files SET record_id = ?",
	} {
		if _, err := s.db.Exec(q, id); err != nil {
			t.Fatal(err)
		}
	}
}

func requireJoinedRecord(t *testing.T, got, want []Record) {
	t.Helper()
	if !reflect.DeepEqual(ids(got), ids(want)) {
		t.Fatalf("records = %v, want only %v", ids(got), ids(want))
	}
	if !reflect.DeepEqual(passLogs(got[0]), passLogs(want[0])) {
		t.Fatalf("pass logs = %v, want %v", passLogs(got[0]), passLogs(want[0]))
	}
	var total float64
	for _, p := range got[0].Passes {
		total += p.USD
	}
	if total != 3 {
		t.Fatalf("total usd = %v, want 3", total)
	}
}

// A satellite joined under a stale ID must not keep that Record alive and win
// the claim-time tie against the primary's replacement, whether the stale ID
// sorts below or above the new one.
func TestStoreReingestMovesJoinedSatelliteToReplacementRecord(t *testing.T) {
	for _, stale := range []string{"unknown:7@old", "zzz:7@old"} {
		t.Run(stale, func(t *testing.T) {
			root := t.TempDir()
			putLog(t, root, "issue-7.log", workLog("2026-10-07T12:00:00Z", 1)...)
			putLog(t, root, "issue-7-fix-1.log", workLog("2026-10-07T13:00:00Z", 2)...)
			s := openStore(t, root)
			ingest(t, s)
			want := records(t, s)
			if len(want) != 1 || len(want[0].Passes) != 2 {
				t.Fatalf("setup records = %+v", want)
			}
			restampRecordID(t, s, stale)
			if _, err := s.Reingest(); err != nil {
				t.Fatal(err)
			}
			requireJoinedRecord(t, records(t, s), want)
		})
	}
}

// The same split without a forced re-parse: a provisional primary's ID moves
// as its log grows while a satellite is joined to it.
func TestStoreProvisionalIDChangeCarriesJoinedSatellite(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-7.log", workLog("2026-10-07T12:00:00Z", 1)...)
	putLog(t, root, "issue-7-fix-1.log", workLog("2026-10-07T13:00:00Z", 2)...)
	s := openStore(t, root)
	ingest(t, s)
	want := records(t, s)
	if len(want) != 1 || len(want[0].Passes) != 2 {
		t.Fatalf("setup records = %+v", want)
	}
	restampRecordID(t, s, "unknown:7@old")
	if _, err := s.db.Exec("UPDATE ingested_files SET provisional = 1 WHERE path = ?", p); err != nil {
		t.Fatal(err)
	}
	// Only the primary changed on disk; the satellite's stat is untouched.
	appendLog(t, p, "\n")
	ingest(t, s)
	requireJoinedRecord(t, records(t, s), want)
}

func TestStoreMigratesV1DatabaseKeepsVerdictEvidenceColumns(t *testing.T) {
	root := t.TempDir()
	path := hostpaths.DispatchRecordsDB(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		migrations[0],
		"PRAGMA user_version = 1",
		`INSERT INTO records VALUES ('work:1@x', 'work', '1', 1000, 'inferred', 'unknown')`,
		`INSERT INTO passes VALUES ('work:1@x', 1, 'review', 'm', 1.5, 1, 2, 3, 4, 5, 6, 7, 8, 'BLOCK')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s := openStore(t, root)
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("user_version = %d, err %v; want %d", v, err, len(migrations))
	}
	recs := records(t, s)
	if len(recs) != 1 || len(recs[0].Passes) != 1 {
		t.Fatalf("records = %+v", recs)
	}
	want := Pass{Ordinal: 1, Role: "review", Models: []string{"m"}, USD: 1.5, InputTokens: 1, OutputTokens: 2,
		CacheReadInputTokens: 3, CacheCreationInputTokens: 4, APICalls: 5, Turns: 6, DurationMs: 7, APIDurationMs: 8, Verdict: "BLOCK"}
	if !reflect.DeepEqual(recs[0].Passes[0], want) {
		t.Fatalf("pass = %+v, want %+v", recs[0].Passes[0], want)
	}
}

// A v1 database already holds an ingested_files row for each log, so the
// migration must make the next plain Ingest re-parse them for their evidence.
func TestStoreReparsesV1IngestedLogAfterMigration(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-7.log", reviewEvidenceLog("VERDICT: BLOCK")...)
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	path := hostpaths.DispatchRecordsDB(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	id := "work:7@2026-10-07T12:00:00.000Z"
	for _, q := range []string{
		migrations[0],
		"PRAGMA user_version = 1",
		`INSERT INTO records VALUES ('` + id + `', 'work', '7', 1791374400000, 'inferred', 'unknown')`,
		`INSERT INTO passes VALUES ('` + id + `', 1, 'review', 'm', 1, 1, 1, 1, 1, 1, 1, 1, 1, 'BLOCK')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO ingested_files VALUES (?, ?, ?, ?, 0)`, p, info.Size(), info.ModTime().UnixNano(), id); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s := openStore(t, root)
	if n := ingest(t, s); n != 1 {
		t.Fatalf("parsed = %d, want the v1-ingested log re-parsed once", n)
	}
	recs := records(t, s)
	if len(recs) != 1 || recs[0].ID != id || len(recs[0].Passes) != 2 {
		t.Fatalf("records = %+v", recs)
	}
	if got := recs[0].Passes[0].VerdictText; got != "VERDICT: BLOCK" {
		t.Fatalf("VerdictText = %q after upgrade", got)
	}
	if n := ingest(t, s); n != 0 {
		t.Fatalf("second ingest parsed = %d, want 0", n)
	}
}

func TestStoreMigratesV3DatabaseWithOutcomeDefaults(t *testing.T) {
	root := t.TempDir()
	path := hostpaths.DispatchRecordsDB(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := append([]string{}, migrations[:3]...)
	stmts = append(stmts, "PRAGMA user_version = 3",
		`INSERT INTO records (record_id, kind, dispatch_key, claim_time, attribution, outcome) VALUES ('work:1@x', 'work', '1', 1000, 'inferred', 'unknown')`)
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s := openStore(t, root)
	recs := records(t, s)
	if len(recs) != 1 || recs[0].Outcome != OutcomeUnknown || recs[0].OutcomeSource != OutcomeSourceNone ||
		recs[0].Reason != "" || recs[0].BoxStatus != "" {
		t.Fatalf("records = %+v", recs)
	}
}

// A provisional satellite that joined an earlier Record and then grows past a
// later Dispatch's start moves there whole: its earlier passes must not stay
// behind on the first Record.
func TestStoreGrowingProvisionalSatelliteLeavesNoPassesBehind(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-9.log.prior-run.1", workLog("2026-03-01T10:00:00.000Z", 1)...)
	putLog(t, root, "issue-9.log", workLog("2026-03-05T10:00:00.000Z", 1)...)
	fix := putLog(t, root, "issue-9-fix-1.log", opLine(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "fix"}))
	chtimes := func(ts string) {
		t.Helper()
		mt, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(fix, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	chtimes("2026-03-02T12:00:00Z")
	s := openStore(t, root)
	ingest(t, s)
	recs := records(t, s)
	if len(recs) != 2 || len(recs[0].Passes) != 2 {
		t.Fatalf("setup records = %+v", recs)
	}
	appendLog(t, fix, assistant("f1"))
	chtimes("2026-03-06T12:00:00Z")
	ingest(t, s)
	recs = records(t, s)
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2: %+v", len(recs), recs)
	}
	onFix, total := 0, 0
	for _, r := range recs {
		total += len(r.Passes)
		for _, l := range passLogs(r) {
			if l == "issue-9-fix-1.log" {
				onFix++
			}
		}
	}
	if onFix != 1 || total != 3 {
		t.Fatalf("fix log on %d Records, %d passes in total; want 1 and 3: %+v", onFix, total, recs)
	}
	last := recs[len(recs)-1]
	if !last.ClaimTime.Equal(time.Date(2026, 3, 5, 10, 0, 0, 0, time.UTC)) ||
		!reflect.DeepEqual(passLogs(last), []string{"issue-9.log", "issue-9-fix-1.log"}) {
		t.Fatalf("later Record = %s with logs %v, want the 03-05 Record carrying the fix log", last.ID, passLogs(last))
	}
}

// A primary whose inferred kind changes as it grows keeps its claim time, so
// the stale-ID Record is replaced by plain Ingest and its joined satellite
// moves to the replacement rather than keeping the stale Record alive.
func TestStoreIngestKindChangeKeepsJoinedSatelliteOnReplacement(t *testing.T) {
	root := t.TempDir()
	p := putLog(t, root, "issue-7.log", result("2026-10-07T12:00:00Z", 1, 1, 10, 5, "opus"))
	putLog(t, root, "issue-7-fix-1.log", workLog("2026-10-07T13:00:00Z", 2)...)
	s := openStore(t, root)
	ingest(t, s)
	before := records(t, s)
	if len(before) != 1 || before[0].Kind != KindUnknown {
		t.Fatalf("setup records = %+v, want one %s Record", before, KindUnknown)
	}

	appendLog(t, p, outcomeLine("recommend"))
	ingest(t, s)

	got := records(t, s)
	if len(got) != 1 {
		t.Fatalf("records = %v, want exactly one", ids(got))
	}
	if got[0].Kind != "research" || got[0].ID == before[0].ID {
		t.Fatalf("record = %s (%s), want a research Record replacing %s", got[0].ID, got[0].Kind, before[0].ID)
	}
	if want := []string{"issue-7.log", "issue-7-fix-1.log"}; !reflect.DeepEqual(passLogs(got[0]), want) {
		t.Fatalf("pass logs = %v, want %v", passLogs(got[0]), want)
	}
	var stale int
	if err := s.db.QueryRow(`SELECT (SELECT COUNT(*) FROM records WHERE record_id = ?)
		+ (SELECT COUNT(*) FROM passes WHERE record_id = ?)
		+ (SELECT COUNT(*) FROM ingested_files WHERE record_id = ?)`,
		before[0].ID, before[0].ID, before[0].ID).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Fatalf("%d rows left under stale ID %s", stale, before[0].ID)
	}
}

// requireRecordLogs fails unless the store holds Records, in order, each
// carrying exactly the named logs.
func requireRecordLogs(t *testing.T, s *Store, want ...[]string) {
	t.Helper()
	got := records(t, s)
	var gotLogs [][]string
	for _, r := range got {
		gotLogs = append(gotLogs, passLogs(r))
	}
	if !reflect.DeepEqual(gotLogs, want) {
		t.Fatalf("record logs = %v (ids %v), want %v", gotLogs, ids(got), want)
	}
}

// A satellite orphaned for want of a primary Dispatch rejoins the primary's
// Record once the primary's log gains output.
func TestStoreSatelliteRejoinsPrimaryThatGainsOutput(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-9.log")
	putLog(t, root, "issue-9-fix-1.log", workLog("2026-03-01T11:00:00.000Z", 2)...)
	s := openStore(t, root)
	ingest(t, s)
	requireRecordLogs(t, s, []string{"issue-9-fix-1.log"})
	putLog(t, root, "issue-9.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	ingest(t, s)
	requireRecordLogs(t, s, []string{"issue-9.log", "issue-9-fix-1.log"})
}

// requirePassUSD fails unless the store's passes, in Record then pass order,
// cost exactly want.
func requirePassUSD(t *testing.T, s *Store, want ...float64) {
	t.Helper()
	var got []float64
	for _, r := range records(t, s) {
		for _, p := range r.Passes {
			got = append(got, p.USD)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pass USD = %v, want %v", got, want)
	}
}

// A renamed orphan satellite has no ingested_files row left under its new
// name; when an earlier Dispatch appears in the same Ingest it moves onto that
// Dispatch and must not leave its old segment or Record behind.
func TestStoreRenamedOrphanSatelliteMovesOntoEarlierDispatch(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-9.log")
	fix := putLog(t, root, "issue-9-fix-1.log", workLog("2026-03-01T11:00:00.000Z", 2)...)
	s := openStore(t, root)
	ingest(t, s)
	requireRecordLogs(t, s, []string{"issue-9-fix-1.log"})
	putLog(t, root, "issue-9.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	if err := os.Rename(fix, fix+".prior-run.1"); err != nil {
		t.Fatal(err)
	}
	ingest(t, s)
	requireRecordLogs(t, s, []string{"issue-9.log", "issue-9-fix-1.log.prior-run.1"})
	requirePassUSD(t, s, 1, 2)
}

// A renamed satellite that had joined a Dispatch moves to a newer one in the
// same Ingest; its pass must leave the old Record.
func TestStoreRenamedJoinedSatelliteMovesOntoNewerDispatch(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-9.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	fix := putLog(t, root, "issue-9-fix-1.log", workLog("2026-03-01T11:00:00.000Z", 2)...)
	s := openStore(t, root)
	ingest(t, s)
	requireRecordLogs(t, s, []string{"issue-9.log", "issue-9-fix-1.log"})
	putLog(t, root, "issue-9.log.prior-run.1", workLog("2026-03-01T10:30:00.000Z", 4)...)
	if err := os.Rename(fix, fix+".prior-run.1"); err != nil {
		t.Fatal(err)
	}
	ingest(t, s)
	requireRecordLogs(t, s,
		[]string{"issue-9.log"},
		[]string{"issue-9.log.prior-run.1", "issue-9-fix-1.log.prior-run.1"})
	requirePassUSD(t, s, 1, 4, 2)
}

// Quarantine reuses the first free .prior-run.N slot, so a renamed satellite can
// land on a path whose ingested_files row belongs to a deleted file: the row
// must not stand in for the moved file, whose old segment still has to leave,
// and the deleted file's pass stays where it was (a fresh store cannot know it).
func TestStoreRenamedSatelliteIntoReusedSlot(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-9.log.prior-run.1", workLog("2026-03-01T10:00:00.000Z", 1)...)
	gone := putLog(t, root, "issue-9-fix-1.log.prior-run.1", workLog("2026-03-01T11:00:00.000Z", 2)...)
	putLog(t, root, "issue-9.log")
	fix := putLog(t, root, "issue-9-fix-1.log", workLog("2026-03-01T12:00:00.000Z", 3)...)
	s := openStore(t, root)
	ingest(t, s)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	putLog(t, root, "issue-9.log", workLog("2026-03-01T11:30:00.000Z", 4)...)
	// Coarse filesystem timestamps would otherwise make the two files stat-identical.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(fix, later, later); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fix, gone); err != nil {
		t.Fatal(err)
	}
	ingest(t, s)
	want := [][]string{
		{"issue-9.log.prior-run.1", "issue-9-fix-1.log.prior-run.1"},
		{"issue-9.log", "issue-9-fix-1.log.prior-run.1"},
	}
	requireRecordLogs(t, s, want...)
	requirePassUSD(t, s, 1, 2, 4, 3)
	if _, err := s.Reingest(); err != nil {
		t.Fatal(err)
	}
	requireRecordLogs(t, s, want...)
	requirePassUSD(t, s, 1, 2, 4, 3)

	freshRoot := t.TempDir()
	putLog(t, freshRoot, "issue-9.log.prior-run.1", workLog("2026-03-01T10:00:00.000Z", 1)...)
	putLog(t, freshRoot, "issue-9.log", workLog("2026-03-01T11:30:00.000Z", 4)...)
	putLog(t, freshRoot, "issue-9-fix-1.log.prior-run.1", workLog("2026-03-01T12:00:00.000Z", 3)...)
	fresh := openStore(t, freshRoot)
	ingest(t, fresh)
	requireRecordLogs(t, fresh,
		[]string{"issue-9.log.prior-run.1"},
		[]string{"issue-9.log", "issue-9-fix-1.log.prior-run.1"})
	requirePassUSD(t, fresh, 1, 4, 3)
}

// Deleting a satellite's log never removes its pass; when the orphan Record it
// joined is then abandoned, that pass moves to the primary's Record, whether
// the deletion and the primary's arrival are seen by separate Ingests or one.
func TestStoreOrphanLeavingKeepsDeletedPeerPasses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		separate bool
	}{
		{"separate ingests", true},
		{"one ingest", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			putLog(t, root, "issue-9.log")
			putLog(t, root, "issue-9-fix-1.log", workLog("2026-03-01T11:00:00.000Z", 2)...)
			peer := putLog(t, root, "issue-9-conflict-resolve.log", workLog("2026-03-01T12:00:00.000Z", 4)...)
			s := openStore(t, root)
			ingest(t, s)
			if err := os.Remove(peer); err != nil {
				t.Fatal(err)
			}
			if tc.separate {
				ingest(t, s)
			}
			putLog(t, root, "issue-9.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
			ingest(t, s)
			got := records(t, s)
			if len(got) != 1 {
				t.Fatalf("records = %v, want one", ids(got))
			}
			logs := passLogs(got[0])
			want := []string{"issue-9.log", "issue-9-fix-1.log", "issue-9-conflict-resolve.log"}
			if !reflect.DeepEqual(logs, want) {
				t.Fatalf("logs = %v, want %v", logs, want)
			}
			requirePassUSD(t, s, 1, 2, 4)
		})
	}
}

// A deleted peer's pass re-windows by its own start when its orphan Record is
// abandoned, landing on the later Dispatch that precedes it rather than the
// replacement.
func TestStoreOrphanLeavingRewindowsDeletedPeerPasses(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-9.log")
	putLog(t, root, "issue-9-fix-1.log", workLog("2026-03-01T11:00:00.000Z", 2)...)
	peer := putLog(t, root, "issue-9-conflict-resolve.log", workLog("2026-03-01T12:00:00.000Z", 4)...)
	s := openStore(t, root)
	ingest(t, s)
	if err := os.Remove(peer); err != nil {
		t.Fatal(err)
	}
	ingest(t, s)
	putLog(t, root, "issue-9.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	putLog(t, root, "issue-9.log.prior-run.1", workLog("2026-03-01T11:30:00.000Z", 8)...)
	ingest(t, s)
	got := records(t, s)
	if len(got) != 2 {
		t.Fatalf("records = %v, want two", ids(got))
	}
	want := [][]string{
		{"issue-9.log", "issue-9-fix-1.log"},
		{"issue-9.log.prior-run.1", "issue-9-conflict-resolve.log"},
	}
	for i, r := range got {
		if logs := passLogs(r); !reflect.DeepEqual(logs, want[i]) {
			t.Fatalf("record %d logs = %v, want %v", i, logs, want[i])
		}
	}
}

// A satellite ingested before its primary's log exists rejoins it when the
// primary appears.
func TestStoreSatelliteRejoinsPrimaryAddedLater(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-9-fix-1.log", workLog("2026-03-01T11:00:00.000Z", 2)...)
	s := openStore(t, root)
	ingest(t, s)
	putLog(t, root, "issue-9.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	ingest(t, s)
	requireRecordLogs(t, s, []string{"issue-9.log", "issue-9-fix-1.log"})
}

// Both orphans precede every Dispatch; the later-starting one sorts first by
// name, yet they still end up on the earlier one's Record.
func TestStoreOrphanPeersShareTheEarliestOrphan(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-9-conflict-resolve.log", workLog("2026-03-01T12:00:00.000Z", 4)...)
	putLog(t, root, "issue-9-fix-1.log", workLog("2026-03-01T11:00:00.000Z", 2)...)
	s := openStore(t, root)
	ingest(t, s)
	requireRecordLogs(t, s, []string{"issue-9-fix-1.log", "issue-9-conflict-resolve.log"})
}

type shape struct {
	ID   string
	Logs []string
	USD  []float64
}

func shapes(t *testing.T, s *Store) []shape {
	t.Helper()
	var out []shape
	for _, r := range records(t, s) {
		sh := shape{ID: r.ID, Logs: passLogs(r)}
		for _, p := range r.Passes {
			sh.USD = append(sh.USD, p.USD)
		}
		out = append(out, sh)
	}
	return out
}

type timedLog struct {
	name, ts string
	cost     float64
}

// The Records a log set yields must not depend on the order logs arrive in.
func TestStoreAssignmentIsOrderIndependent(t *testing.T) {
	logs := []timedLog{
		{"issue-9.log.prior-run.1", "2026-03-01T10:00:00.000Z", 1},
		{"issue-9.log", "2026-03-05T10:00:00.000Z", 2},
		{"issue-9-fix-1.log.prior-run.1", "2026-03-02T11:00:00.000Z", 3},
		{"issue-9-fix-1.log", "2026-03-05T11:00:00.000Z", 4},
		{"issue-9-conflict-resolve.log", "2026-02-28T10:00:00.000Z", 5},
		{"issue-9-fix-2.log", "2026-02-28T12:00:00.000Z", 6},
	}
	run := func(t *testing.T, batches ...[]timedLog) []shape {
		root := t.TempDir()
		s := openStore(t, root)
		for _, batch := range batches {
			for _, l := range batch {
				putLog(t, root, l.name, workLog(l.ts, l.cost)...)
			}
			ingest(t, s)
		}
		return shapes(t, s)
	}
	one := func(ls []timedLog) [][]timedLog {
		var out [][]timedLog
		for _, l := range ls {
			out = append(out, []timedLog{l})
		}
		return out
	}
	byStart := append([]timedLog(nil), logs...)
	sort.Slice(byStart, func(i, j int) bool { return byStart[i].ts < byStart[j].ts })
	reversed := make([]timedLog, len(byStart))
	for i, l := range byStart {
		reversed[len(byStart)-1-i] = l
	}
	var primaries, satellites []timedLog
	for _, l := range logs {
		if _, sat, _ := ChainKey(l.name); sat {
			satellites = append(satellites, l)
		} else {
			primaries = append(primaries, l)
		}
	}
	want := run(t, logs)
	var wantUSD float64
	for _, l := range logs {
		wantUSD += l.cost
	}
	var gotPasses int
	var gotUSD float64
	for _, sh := range want {
		gotPasses += len(sh.USD)
		for _, u := range sh.USD {
			gotUSD += u
		}
	}
	if len(want) != 3 || gotPasses != len(logs) || gotUSD != wantUSD {
		t.Fatalf("fresh ingest shapes = %+v, want 3 Records with %d passes costing %v", want, len(logs), wantUSD)
	}
	for name, batches := range map[string][][]timedLog{
		"satellites then primaries": {satellites, primaries},
		"reverse chronological":     one(reversed),
		"chronological":             one(byStart),
	} {
		t.Run(name, func(t *testing.T) {
			if got := run(t, batches...); !reflect.DeepEqual(got, want) {
				t.Errorf("shapes = %+v, want %+v", got, want)
			}
		})
	}
}

// logWrite is one write of a log file; a non-empty mtime sets its modified
// time, the only start a log without a result timestamp has.
type logWrite struct {
	name, mtime string
	lines       []string
}

func writeLogs(t *testing.T, root string, ws ...logWrite) {
	t.Helper()
	for _, w := range ws {
		p := putLog(t, root, w.name, w.lines...)
		if w.mtime == "" {
			continue
		}
		mt, err := time.Parse(time.RFC3339, w.mtime)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
}

// Provisional (mtime-derived) logs growing, and empty logs gaining output,
// land on the same Records however the files and their growth interleave
// across Ingest calls.
func TestStoreAssignmentIsOrderIndependentForGrowingLogs(t *testing.T) {
	pass1 := opLine(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "fix"})
	prior := logWrite{name: "issue-9.log.prior-run.1", lines: workLog("2026-03-01T10:00:00.000Z", 1)}
	priorFix := logWrite{name: "issue-9-fix-1.log.prior-run.1", lines: workLog("2026-03-02T11:00:00.000Z", 2)}

	provFix := logWrite{name: "issue-9-fix-1.log", mtime: "2026-03-02T12:00:00Z", lines: []string{pass1}}
	grownFix := logWrite{name: "issue-9-fix-1.log", mtime: "2026-03-06T12:00:00Z", lines: []string{pass1, assistant("f1")}}
	primary := logWrite{name: "issue-9.log", lines: workLog("2026-03-05T10:00:00.000Z", 3)}

	provPrimary := logWrite{name: "issue-9.log", mtime: "2026-03-05T11:00:00Z",
		lines: []string{opLine(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"})}}
	donePrimary := logWrite{name: "issue-9.log", mtime: "2026-03-05T12:00:00Z", lines: workLog("2026-03-05T12:00:00.000Z", 3)}
	laterFix := logWrite{name: "issue-9-fix-1.log", lines: workLog("2026-03-05T13:00:00.000Z", 4)}

	emptyPrimary := logWrite{name: "issue-9.log"}
	outPrimary := logWrite{name: "issue-9.log", lines: workLog("2026-03-05T10:00:00.000Z", 3)}
	outFix := logWrite{name: "issue-9-fix-1.log", lines: workLog("2026-03-05T11:00:00.000Z", 4)}

	type batches [][]logWrite
	for _, sc := range []struct {
		name       string
		final      []logWrite
		wantRecs   int
		wantPasses int
		wantUSD    float64
		seqs       map[string]batches
	}{
		{
			name:       "provisional satellite grows past a later Dispatch",
			final:      []logWrite{prior, primary, grownFix},
			wantRecs:   2,
			wantPasses: 3,
			wantUSD:    4,
			seqs: map[string]batches{
				"satellite first":  {{provFix}, {prior, primary}, {grownFix}},
				"primary first":    {{prior, primary}, {provFix}, {grownFix}},
				"grown before all": {{provFix}, {grownFix}, {prior, primary}},
				"together":         {{prior, primary, provFix}, {grownFix}},
			},
		},
		{
			name:       "provisional primary gains a timestamped result",
			final:      []logWrite{prior, priorFix, donePrimary, laterFix},
			wantRecs:   2,
			wantPasses: 4,
			wantUSD:    10,
			seqs: map[string]batches{
				"satellite first": {{prior, priorFix, laterFix}, {provPrimary}, {donePrimary}},
				"primary first":   {{prior, priorFix, provPrimary}, {laterFix}, {donePrimary}},
				"together":        {{prior, priorFix, provPrimary, laterFix}, {donePrimary}},
			},
		},
		{
			name:       "empty primary gains output",
			final:      []logWrite{prior, outPrimary, outFix},
			wantRecs:   2,
			wantPasses: 3,
			wantUSD:    8,
			seqs: map[string]batches{
				"satellite first": {{prior, outFix}, {emptyPrimary}, {outPrimary}},
				"primary first":   {{prior, emptyPrimary}, {outFix}, {outPrimary}},
				"together":        {{prior, emptyPrimary, outFix}, {outPrimary}},
			},
		},
	} {
		t.Run(sc.name, func(t *testing.T) {
			fresh := t.TempDir()
			writeLogs(t, fresh, sc.final...)
			fs := openStore(t, fresh)
			ingest(t, fs)
			want := shapes(t, fs)
			passes := 0
			var usd float64
			for _, sh := range want {
				passes += len(sh.Logs)
				for _, u := range sh.USD {
					usd += u
				}
			}
			if len(want) != sc.wantRecs || passes != sc.wantPasses || usd != sc.wantUSD {
				t.Fatalf("fresh ingest = %+v, want %d Records, %d passes, USD %v", want, sc.wantRecs, sc.wantPasses, sc.wantUSD)
			}
			for name, seq := range sc.seqs {
				t.Run(name, func(t *testing.T) {
					root := t.TempDir()
					s := openStore(t, root)
					for _, b := range seq {
						writeLogs(t, root, b...)
						ingest(t, s)
					}
					if got := shapes(t, s); !reflect.DeepEqual(got, want) {
						t.Errorf("shapes = %+v, want %+v", got, want)
					}
				})
			}
		})
	}
}

// A provisional primary's ID moves when its log gains a timestamped result;
// the satellite joined to it must follow, with no pass counted twice.
func TestStoreProvisionalPrimaryResultRekeysJoinedSatellite(t *testing.T) {
	root := t.TempDir()
	writeLogs(t, root,
		logWrite{name: "issue-9.log", mtime: "2026-03-01T11:00:00Z",
			lines: []string{opLine(claude.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"})}},
		logWrite{name: "issue-9-fix-1.log", lines: workLog("2026-03-01T13:00:00.000Z", 2)})
	s := openStore(t, root)
	ingest(t, s)
	requireRecordLogs(t, s, []string{"issue-9.log", "issue-9-fix-1.log"})

	writeLogs(t, root, logWrite{name: "issue-9.log", mtime: "2026-03-01T12:00:00Z",
		lines: workLog("2026-03-01T12:00:00.000Z", 1)})
	ingest(t, s)
	requireRecordLogs(t, s, []string{"issue-9.log", "issue-9-fix-1.log"})
	got := records(t, s)
	if want := "work:9@2026-03-01T12:00:00.000Z"; got[0].ID != want || len(got[0].Passes) != 2 {
		t.Fatalf("record = %s with %d passes, want %s with 2", got[0].ID, len(got[0].Passes), want)
	}
}
