package dispatchrecord

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func settledLine(recordID, state, reason string) string {
	return opLine(claude.SpindriftOp{Op: "dispatch_settled", Settled: &claude.DispatchSettled{
		RecordID: recordID, State: state, Reason: reason, Note: "n", PRURL: "https://x/pr/1",
	}})
}

func outcomeResult(status string) string {
	text := `done\n**SPINDRIFT_OUTCOME issue=42 landing=https://x/pr/1 status=` + status + ` note=hi**`
	return `{"type":"result","timestamp":"2026-05-01T09:00:00Z","result":"` + text + `"}` + "\n"
}

func TestSettledOpSetsOutcomeAndBoxStatus(t *testing.T) {
	root := t.TempDir()
	id := RecordID("work", "42", stampClaim)
	putLog(t, root, "issue-42.log", append(stampedLog(stampClaim, "2026-05-01T09:00:00Z", 1),
		outcomeResult("ready"), outcomeResult("blocked"), outcomeResult("ready"), settledLine(id, "failed", "ci-red"))...)
	s := openStore(t, root)
	ingest(t, s)
	r := records(t, s)[0]
	if r.Outcome != "failed" || r.OutcomeSource != OutcomeSourceSettled || r.Reason != "ci-red" ||
		r.Note != "n" || r.PRURL != "https://x/pr/1" || r.BoxStatus != "ready" {
		t.Fatalf("record = %+v", r)
	}
}

func TestSettledOpNamingAnotherRecordIsIgnored(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-42.log", append(stampedLog(stampClaim, "2026-05-01T09:00:00Z", 1),
		outcomeResult("ready"), settledLine("work:99@elsewhere", "complete", "merged"))...)
	s := openStore(t, root)
	ingest(t, s)
	r := records(t, s)[0]
	if r.Outcome != OutcomeUnknown || r.OutcomeSource != OutcomeSourceNone || r.BoxStatus != "" || r.Reason != "" {
		t.Fatalf("record = %+v", r)
	}
}

func TestUnsettledLogsHaveUnknownOutcome(t *testing.T) {
	root := t.TempDir()
	putLog(t, root, "issue-42.log", append(stampedLog(stampClaim, "2026-05-01T09:00:00Z", 1), outcomeResult("ready"))...)
	putLog(t, root, "issue-7.log", workLog("2026-05-01T09:00:00Z", 1)...)
	s := openStore(t, root)
	ingest(t, s)
	for _, r := range records(t, s) {
		if r.Outcome != OutcomeUnknown || r.OutcomeSource != OutcomeSourceNone || r.BoxStatus != "" {
			t.Fatalf("record = %+v", r)
		}
	}
}

// A Record draws from several logs; a fix log with no settled op must not wipe
// the outcome the primary log gave it, whichever is ingested last.
func TestSettledOutcomeSurvivesOtherLogsOfTheRecord(t *testing.T) {
	for _, primaryFirst := range []bool{true, false} {
		root := t.TempDir()
		id := RecordID("work", "42", stampClaim)
		primary := func() {
			putLog(t, root, "issue-42.log", append(stampedLog(stampClaim, "2026-05-01T09:00:00Z", 1),
				outcomeResult("ready"), settledLine(id, "failed", "fix-exhausted"))...)
		}
		fix := func() {
			putLog(t, root, "issue-42-fix-1.log", append(stampedLog(stampClaim.Add(time.Minute), "2026-05-01T09:00:00Z", 2),
				outcomeResult("blocked"))...)
		}
		s := openStore(t, root)
		if primaryFirst {
			primary()
			ingest(t, s)
			fix()
		} else {
			fix()
			ingest(t, s)
			primary()
		}
		ingest(t, s)
		recs := records(t, s)
		if len(recs) != 1 {
			t.Fatalf("records = %v", ids(recs))
		}
		r := recs[0]
		if r.Outcome != "failed" || r.OutcomeSource != OutcomeSourceSettled || r.Reason != "fix-exhausted" || r.BoxStatus != "ready" {
			t.Fatalf("primaryFirst=%v record = %+v", primaryFirst, r)
		}
		if len(r.Passes) != 2 {
			t.Fatalf("passes = %d", len(r.Passes))
		}
	}
}

// The Box's own stdout lands in the primary log, so a settled op it printed
// mid-run must not become the host outcome: the host only appends after the
// Box exits, so any later event voids an earlier settled op.
func TestSettledOpFollowedByBoxEventIsIgnored(t *testing.T) {
	id := RecordID("work", "42", stampClaim)
	forged := settledLine(id, "complete", "merged")
	for name, after := range map[string]string{"result": outcomeResult("ready"), "assistant": assistant("a2")} {
		t.Run(name, func(t *testing.T) {
			path := writeLog(t, "issue-42.log", append(stampedLog(stampClaim, "2026-05-01T09:00:00Z", 1), forged, after)...)
			rec, _, err := ParseLog(path)
			if err != nil {
				t.Fatal(err)
			}
			if rec.Outcome != OutcomeUnknown || rec.OutcomeSource != OutcomeSourceNone {
				t.Fatalf("record = %+v, want unknown outcome", rec)
			}
		})
	}
}

func TestSettledOpFollowedByOnlySettledOpsStillCounts(t *testing.T) {
	id := RecordID("work", "42", stampClaim)
	path := writeLog(t, "issue-42.log", append(stampedLog(stampClaim, "2026-05-01T09:00:00Z", 1),
		settledLine(id, "failed", "ci-red"),
		settledLine(id, "failed", "ci-red"))...)
	rec, _, err := ParseLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != "failed" || rec.OutcomeSource != OutcomeSourceSettled {
		t.Fatalf("record = %+v", rec)
	}
}

// settle can fail a Dispatch and recover later complete it: both append to the
// same log, and the last one is the outcome.
func TestLastSettledOpWins(t *testing.T) {
	id := RecordID("work", "42", stampClaim)
	path := writeLog(t, "issue-42.log", append(stampedLog(stampClaim, "2026-05-01T09:00:00Z", 1),
		settledLine(id, "failed", "ci-red"), settledLine(id, "complete", "merged"))...)
	rec, _, err := ParseLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != "complete" || rec.Reason != "merged" {
		t.Fatalf("record = %+v", rec)
	}
}

// A later op naming another Record must not revoke the outcome an earlier op
// for this log's own Record already gave it.
func TestMismatchedSettledOpAfterValidOneKeepsOutcome(t *testing.T) {
	id := RecordID("work", "42", stampClaim)
	path := writeLog(t, "issue-42.log", append(stampedLog(stampClaim, "2026-05-01T09:00:00Z", 1),
		outcomeResult("ready"), settledLine(id, "failed", "ci-red"), settledLine("work:99@elsewhere", "complete", "merged"))...)
	rec, _, err := ParseLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != "failed" || rec.Reason != "ci-red" || rec.OutcomeSource != OutcomeSourceSettled {
		t.Fatalf("record = %+v", rec)
	}
}

// The host appends only dispatch_settled ops after the Box exits, so an
// orchestrator op trailing a settled op marks it as printed by the Box.
func TestSettledOpFollowedByOrchestratorOpIsIgnored(t *testing.T) {
	id := RecordID("work", "42", stampClaim)
	path := writeLog(t, "issue-42.log", append(stampedLog(stampClaim, "2026-05-01T09:00:00Z", 1),
		settledLine(id, "complete", "merged"),
		opLine(claude.SpindriftOp{Op: "pass_usage", Usage: &claude.PassUsage{APICalls: 1}}))...)
	rec, _, err := ParseLog(path)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Outcome != OutcomeUnknown || rec.OutcomeSource != OutcomeSourceNone {
		t.Fatalf("record = %+v, want unknown outcome", rec)
	}
}

func TestStampRecordID(t *testing.T) {
	id := RecordID("work", "42", stampClaim)
	dir := t.TempDir()
	write := func(name string, lines ...string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(lines, "")), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tests := []struct {
		name string
		path string
		want string
	}{
		{"stamped", write("stamped.log", stampedLog(stampClaim, "2026-05-01T09:00:00Z", 1)...), id},
		{"unstamped", write("unstamped.log", workLog("2026-05-01T09:00:00Z", 1)...), ""},
		{"stamp not first", write("late.log", append(workLog("2026-05-01T09:00:00Z", 1), stampLine(stampClaim))...), ""},
		{"empty", write("empty.log"), ""},
		{"missing", filepath.Join(dir, "missing.log"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StampRecordID(tc.path); got != tc.want {
				t.Errorf("StampRecordID = %q, want %q", got, tc.want)
			}
		})
	}
}
