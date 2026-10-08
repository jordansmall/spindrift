package dispatchrecord

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/hostpaths"
)

var stampClaim = time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)

func stampLine(started time.Time) string {
	return opLine(claude.SpindriftOp{Op: "dispatch_start", Start: &claude.DispatchStart{
		RecordID:      RecordID("work", "42", stampClaim),
		Kind:          "work",
		DispatchKey:   "42",
		ClaimTime:     stampClaim,
		Started:       started,
		Revision:      "abc123",
		RoleModels:    map[string]string{"implement": "opus", "review": "sonnet"},
		Driver:        "claude",
		DriverVersion: "1.2.3",
		Knobs:         map[string]string{"MAX_PASSES": "4"},
	}})
}

func stampedLog(started time.Time, ts string, cost float64) []string {
	return append([]string{stampLine(started)}, workLog(ts, cost)...)
}

func putStampedRoot(t *testing.T, root string) []string {
	t.Helper()
	names := []string{"issue-42.log", "issue-42.log.1", "issue-42-fix-1.log", "issue-42-conflict-resolve.log"}
	for i, n := range names {
		started := stampClaim.Add(time.Duration(i) * time.Minute)
		putLog(t, root, n, stampedLog(started, "2026-05-01T09:00:00Z", float64(i+1))...)
	}
	return names
}

func TestStampedLogsOfOneDispatchMergeIntoOneRecord(t *testing.T) {
	root := t.TempDir()
	putStampedRoot(t, root)
	s := openStore(t, root)
	ingest(t, s)
	recs := records(t, s)
	if len(recs) != 1 {
		t.Fatalf("records = %v, want 1", ids(recs))
	}
	r := recs[0]
	if r.ID != RecordID("work", "42", stampClaim) || r.Attribution != AttributionStamped || r.Outcome != OutcomeUnknown {
		t.Fatalf("record = %+v", r)
	}
	if r.Revision != "abc123" || r.Driver != "claude" || r.DriverVersion != "1.2.3" ||
		!reflect.DeepEqual(r.RoleModels, map[string]string{"implement": "opus", "review": "sonnet"}) ||
		!reflect.DeepEqual(r.Knobs, map[string]string{"MAX_PASSES": "4"}) {
		t.Fatalf("stamp fields lost: %+v", r)
	}
	if !r.ClaimTime.Equal(stampClaim) {
		t.Fatalf("claim = %v", r.ClaimTime)
	}
	if len(r.Passes) != 4 {
		t.Fatalf("passes = %d, want 4", len(r.Passes))
	}
	var usd float64
	for _, p := range r.Passes {
		usd += p.USD
	}
	if usd != 10 {
		t.Fatalf("usd = %v, want 10", usd)
	}
}

func TestStampedRenameToPriorRunDoesNotDuplicatePasses(t *testing.T) {
	root := t.TempDir()
	names := putStampedRoot(t, root)
	s := openStore(t, root)
	ingest(t, s)
	first := records(t, s)
	dir := hostpaths.LogDir(root)
	for _, n := range names {
		if err := os.Rename(filepath.Join(dir, n), filepath.Join(dir, n+".prior-run.1")); err != nil {
			t.Fatal(err)
		}
	}
	ingest(t, s)
	if got := records(t, s); !reflect.DeepEqual(first, got) {
		t.Fatalf("rename changed records: %+v vs %+v", first, got)
	}
}

func TestUnstampedNonChainLogIsIgnoredAndNotReopened(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-12.log", workLog("2026-03-01T10:00:00.000Z", 1)...)
	putLog(t, root, "issue-12-fix-1.log", workLog("2026-03-01T11:00:00.000Z", 1)...)
	putLog(t, root, "issue-12.warnings", "x")
	putLog(t, root, "issue-12.run-lineage", "x")
	s := openStore(t, root)
	ingest(t, s)
	if got := records(t, s); len(got) != 1 {
		t.Fatalf("records = %v, want 1", ids(got))
	}
	if n := ingest(t, s); n != 0 {
		t.Fatalf("second ingest parsed %d, want 0", n)
	}
}

func TestParseLogUnstampedNonChainName(t *testing.T) {
	_, _, err := ParseLog(writeLog(t, "issue-1-fix-2.log", workLog("2026-03-01T11:00:00Z", 1)...))
	if !errors.Is(err, ErrUnstamped) {
		t.Fatalf("err = %v, want ErrUnstamped", err)
	}
}

func TestParseLogStampMustBeFirstEvent(t *testing.T) {
	lines := append(workLog("2026-03-01T11:00:00Z", 1), stampLine(stampClaim))
	_, _, err := ParseLog(writeLog(t, "issue-1-fix-2.log", lines...))
	if !errors.Is(err, ErrUnstamped) {
		t.Fatalf("err = %v, want ErrUnstamped", err)
	}
	rec, _, err := ParseLog(writeLog(t, "issue-1.log", lines...))
	if err != nil || rec.Attribution != AttributionInferred {
		t.Fatalf("rec = %+v, err = %v", rec, err)
	}
}

func TestStoreMigratesV1DatabaseKeepingRows(t *testing.T) {
	root := t.TempDir()
	path := hostpaths.DispatchRecordsDB(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(migrations[0] + "; PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	claim := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO records VALUES ('work:7@x', 'work', '7', ?, 'inferred', 'unknown')`, claim.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO passes VALUES ('work:7@x', 1, 'implement', 'opus', 1.5, 1, 2, 3, 4, 5, 6, 7, 8, 'ready')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s := openStore(t, root)
	recs := records(t, s)
	if len(recs) != 1 || recs[0].ID != "work:7@x" || len(recs[0].Passes) != 1 {
		t.Fatalf("records = %+v", recs)
	}
	p := recs[0].Passes[0]
	if p.USD != 1.5 || p.Verdict != "ready" || !reflect.DeepEqual(p.Models, []string{"opus"}) {
		t.Fatalf("pass = %+v", p)
	}
	if recs[0].Revision != "" || recs[0].RoleModels != nil {
		t.Fatalf("migrated record gained stamp fields: %+v", recs[0])
	}
}

func hashesLine(recordID string, roles map[string]string) string {
	return opLine(claude.SpindriftOp{Op: "prompt_hashes", PromptHashes: &claude.PromptHashes{RecordID: recordID, Roles: roles}})
}

func TestPromptHashesUnionAcrossLogsOfOneDispatch(t *testing.T) {
	id := RecordID("work", "42", stampClaim)
	put := func(root, name string, offset time.Duration, hashLines ...string) {
		lines := append([]string{stampLine(stampClaim.Add(offset))}, hashLines...)
		putLog(t, root, name, append(lines, workLog("2026-05-01T09:00:00Z", 1)...)...)
	}
	root := t.TempDir()
	put(root, "issue-42.log", 0, hashesLine(id, map[string]string{"implement": "aa", "review": "bb"}))
	put(root, "issue-42-fix-1.log", time.Minute, hashesLine(id, map[string]string{"legacy": "cc"}),
		hashesLine(id, map[string]string{"legacy": "dd"}), hashesLine("work:other@x", map[string]string{"review": "zz"}))
	s := openStore(t, root)
	ingest(t, s)
	recs := records(t, s)
	if len(recs) != 1 {
		t.Fatalf("records = %v, want 1", ids(recs))
	}
	want := map[string]string{"implement": "aa", "review": "bb", "legacy": "dd"}
	if !reflect.DeepEqual(recs[0].PromptHashes, want) {
		t.Fatalf("prompt hashes = %v, want %v", recs[0].PromptHashes, want)
	}

	// Re-ingesting a changed log replaces what it reported earlier.
	put(root, "issue-42.log", 0, hashesLine(id, map[string]string{"implement": "ee"}))
	ingest(t, s)
	want = map[string]string{"implement": "ee", "legacy": "dd"}
	if got := records(t, s)[0].PromptHashes; !reflect.DeepEqual(got, want) {
		t.Fatalf("after re-ingest prompt hashes = %v, want %v", got, want)
	}
}

func TestPromptHashesIgnoredWithoutMatchingStamp(t *testing.T) {
	id := RecordID("work", "42", stampClaim)
	h := hashesLine(id, map[string]string{"implement": "aa"})
	rec, _, err := ParseLog(writeLog(t, "issue-1.log", append([]string{h}, workLog("2026-03-01T11:00:00Z", 1)...)...))
	if err != nil || rec.Attribution != AttributionInferred || rec.PromptHashes != nil {
		t.Fatalf("rec = %+v, err = %v", rec, err)
	}
	rec, _, err = ParseLog(writeLog(t, "issue-42.log", stampLine(stampClaim), hashesLine("work:other@x", map[string]string{"review": "zz"})))
	if err != nil || rec.PromptHashes != nil {
		t.Fatalf("rec = %+v, err = %v", rec, err)
	}
}
