package dispatchrecord

import (
	"math"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/seamtest"
	"spindrift.dev/launcher/internal/usage"
)

// sessionResult is a result event for session S carrying the cumulative
// figures claude reports (issue #6706).
func sessionResult(ts string, cost float64, apiMs int64, opus, haiku int) string {
	return seamtest.Line(driverkit.Event{Kind: driverkit.KindResult, Timestamp: seamtest.Millis(ts), NumTurns: 3,
		SessionID: "S", Tokens: driverkit.Tokens{Output: 1835, CostUSD: cost}, DurationAPIMs: apiMs,
		ModelOutput: map[string]int{"claude-opus-4-1": opus, "claude-haiku-4-5": haiku}})
}

func recordStampLine(recordID string, started time.Time) string {
	return opLine(hostop.SpindriftOp{Op: "dispatch_start", Start: &hostop.DispatchStart{
		RecordID: recordID, Kind: "work", DispatchKey: "42", ClaimTime: stampClaim, Started: started, Driver: "claude"}})
}

func implementLog(recordID string, started time.Time) []string {
	return []string{
		recordStampLine(recordID, started),
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}),
		sessionResult("2026-05-01T08:10:00Z", 1.0, 100000, 5199, 13405),
	}
}

func fixLog(recordID string, started time.Time) []string {
	return []string{
		recordStampLine(recordID, started),
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 2, Role: "fix"}),
		sessionResult("2026-05-01T09:10:00Z", 1.5, 130000, 7034, 13405),
	}
}

// primaryRetryLog is the log of a hold or backoff retry of the primary: it
// resumes the implement session, so its result carries the session's
// cumulative figures.
func primaryRetryLog(recordID string, started time.Time) []string {
	return []string{
		recordStampLine(recordID, started),
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}),
		sessionResult("2026-05-01T09:10:00Z", 1.5, 130000, 7034, 13405),
	}
}

func requirePassIncrement(t *testing.T, rec Record, usd float64, apiMs int64) {
	t.Helper()
	if len(rec.Passes) != 1 {
		t.Fatalf("passes = %d, want 1", len(rec.Passes))
	}
	p := rec.Passes[0]
	if math.Abs(p.USD-usd) > 1e-9 || p.APIDurationMs != apiMs {
		t.Errorf("pass = usd %v api %d, want usd %v api %d", p.USD, p.APIDurationMs, usd, apiMs)
	}
}

func TestParseLog_ResumedSessionAcrossLogsBooksOnlyTheIncrement(t *testing.T) {
	t.Parallel()
	id := RecordID("work", "42", stampClaim)
	root := t.TempDir()
	putLog(t, root, "issue-42.log", implementLog(id, stampClaim)...)
	fix := putLog(t, root, "issue-42-fix-1.log", fixLog(id, stampClaim.Add(time.Hour))...)

	rec, _, err := ParseLog(fix)
	if err != nil {
		t.Fatal(err)
	}
	requirePassIncrement(t, rec, 0.5, 30000)
	for _, a := range rec.Passes[0].Agents {
		if a.Agent == usage.SubagentsAgent && a.OutputTokens != 0 {
			t.Errorf("phantom %s row = %d output tokens", a.Agent, a.OutputTokens)
		}
	}
}

func TestParseLog_ResumedSessionChainsThroughEarlierFixLogs(t *testing.T) {
	t.Parallel()
	id := RecordID("work", "42", stampClaim)
	root := t.TempDir()
	putLog(t, root, "issue-42.log.prior-run.1", implementLog(id, stampClaim)...)
	putLog(t, root, "issue-42-fix-1.log", fixLog(id, stampClaim.Add(time.Hour))...)
	fix2 := putLog(t, root, "issue-42-fix-2.log", recordStampLine(id, stampClaim.Add(2*time.Hour)),
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 3, Role: "fix"}),
		sessionResult("2026-05-01T10:10:00Z", 1.75, 140000, 7034, 13405))

	rec, _, err := ParseLog(fix2)
	if err != nil {
		t.Fatal(err)
	}
	requirePassIncrement(t, rec, 0.25, 10000)
}

// A satellite log whose earlier logs belong to a different record_id starts
// from no baselines, so it books its cumulative figures whole.
func TestParseLog_SatelliteOfAnotherRecordGetsNoSeed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	putLog(t, root, "issue-42.log", implementLog("work:42@other", stampClaim)...)
	fix := putLog(t, root, "issue-42-fix-1.log", fixLog(RecordID("work", "42", stampClaim), stampClaim.Add(time.Hour))...)
	rec, _, err := ParseLog(fix)
	if err != nil {
		t.Fatal(err)
	}
	requirePassIncrement(t, rec, 1.5, 130000)
}

// A hold or backoff retry is a primary that resumes the session the rotated
// issue-42.log.1 ran, under the same record_id.
func TestParseLog_RotatedPrimaryRetryBooksOnlyTheIncrement(t *testing.T) {
	t.Parallel()
	id := RecordID("work", "42", stampClaim)
	root := t.TempDir()
	putLog(t, root, "issue-42.log.1", implementLog(id, stampClaim)...)
	primary := putLog(t, root, "issue-42.log", primaryRetryLog(id, stampClaim.Add(time.Hour))...)
	rec, _, err := ParseLog(primary)
	if err != nil {
		t.Fatal(err)
	}
	requirePassIncrement(t, rec, 0.5, 30000)
	for _, a := range rec.Passes[0].Agents {
		if a.Agent == usage.SubagentsAgent && a.OutputTokens != 0 {
			t.Errorf("phantom %s row = %d output tokens", a.Agent, a.OutputTokens)
		}
	}
}

// A primary whose rotated predecessor belongs to a different record_id starts
// from no baselines, so it books its cumulative figures whole.
func TestParseLog_PrimaryOfAnotherRecordGetsNoSeed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	putLog(t, root, "issue-42.log.1", implementLog("work:42@other", stampClaim)...)
	primary := putLog(t, root, "issue-42.log", fixLog(RecordID("work", "42", stampClaim), stampClaim.Add(time.Hour))...)
	rec, _, err := ParseLog(primary)
	if err != nil {
		t.Fatal(err)
	}
	requirePassIncrement(t, rec, 1.5, 130000)
}

// The fix pass's row in the stored Record is the increment, not the resumed
// implement session's cumulative figures.
func TestStoreResumedFixPassRowIsTheIncrement(t *testing.T) {
	t.Parallel()
	id := RecordID("work", "42", stampClaim)
	root := t.TempDir()
	putLog(t, root, "issue-42.log", implementLog(id, stampClaim)...)
	putLog(t, root, "issue-42-fix-1.log", fixLog(id, stampClaim.Add(time.Hour))...)
	s := openStore(t, root)
	ingest(t, s)
	recs := records(t, s)
	if len(recs) != 1 {
		t.Fatalf("records = %v, want 1", ids(recs))
	}
	var fix *Pass
	for i, p := range recs[0].Passes {
		if p.Role == "fix" {
			fix = &recs[0].Passes[i]
		}
	}
	if fix == nil {
		t.Fatalf("no fix pass in %+v", recs[0].Passes)
	}
	if math.Abs(fix.USD-0.5) > 1e-9 || fix.APIDurationMs != 30000 {
		t.Errorf("fix row = usd %v api %d, want 0.5 / 30000", fix.USD, fix.APIDurationMs)
	}
}
