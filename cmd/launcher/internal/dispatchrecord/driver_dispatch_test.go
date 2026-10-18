package dispatchrecord

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/seamtest"
)

// altDriver names a second registration of the seamtest Strategy. A stamp
// naming it proves the stamped Driver, not the default, reads the log.
const altDriver = "seam-alt"

func driverStamp(driver string) string {
	start := hostop.DispatchStart{
		RecordID:    RecordID("work", "42", stampClaim),
		Kind:        "work",
		DispatchKey: "42",
		ClaimTime:   stampClaim,
		Started:     stampClaim,
		Driver:      driver,
		RoleModels:  map[string]string{"main": "provider/model-x"},
	}
	return opLine(hostop.SpindriftOp{Op: "dispatch_start", Start: &start})
}

func usageCall(msgID, model string, in, out, cacheRead, cacheWrite int, cost float64) string {
	return seamtest.Line(driverkit.Event{Kind: driverkit.KindUsage, MessageID: msgID, Model: model,
		Tokens: driverkit.Tokens{Input: in, Output: out, CacheRead: cacheRead, CacheWrite5m: cacheWrite, CostUSD: cost}})
}

func TestParseLogStampedDriverReadsThePassLog(t *testing.T) {
	id := RecordID("work", "42", stampClaim)
	p := writeLog(t, "issue-42.log",
		driverStamp(altDriver),
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 1, Role: "implement"}),
		usageCall("m1", "", 1, 100, 1000, 200, 0.01),
		usageCall("m2", "", 2, 200, 2000, 300, 0.02),
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 2, Role: "review"}),
		usageCall("m3", "", 4, 400, 4000, 500, 0.03),
		opLine(hostop.SpindriftOp{Op: "pass_start", Pass: 3, Role: "fix"}),
		usageCall("m4", "", 8, 800, 8000, 700, 0.04),
		usageCall("m5", "", 16, 1600, 16000, 900, 0.05),
		outcomeResult("ready"),
		opLine(hostop.SpindriftOp{Op: "dispatch_settled", Settled: &hostop.DispatchSettled{
			RecordID: id, State: "complete", Reason: "merged"}}),
	)
	rec, _, err := ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Driver != altDriver || rec.Attribution != AttributionStamped {
		t.Fatalf("record = %+v", rec)
	}
	if rec.Outcome != "complete" || rec.OutcomeSource != OutcomeSourceSettled || rec.BoxStatus != "ready" {
		t.Fatalf("outcome = %q/%q status %q", rec.Outcome, rec.OutcomeSource, rec.BoxStatus)
	}
	if rec.RoleModels["main"] != "provider/model-x" {
		t.Fatalf("role models = %v", rec.RoleModels)
	}
	var in, out, read, write, calls int
	var usd float64
	for _, ps := range rec.Passes {
		// No call names a model, so each pass names none.
		if ps.Models == nil || len(ps.Models) != 0 {
			t.Fatalf("pass %d models = %#v, want empty", ps.Ordinal, ps.Models)
		}
		in += ps.InputTokens
		out += ps.OutputTokens
		read += ps.CacheReadInputTokens
		write += ps.CacheCreationInputTokens
		calls += ps.APICalls
		usd += ps.USD
	}
	if len(rec.Passes) != 3 || in != 31 || out != 3100 || read != 31000 || write != 2600 || calls != 5 || usd <= 0 {
		t.Fatalf("passes = %+v (in %d out %d read %d write %d calls %d usd %v)", rec.Passes, in, out, read, write, calls, usd)
	}
}

func TestParseLogStampedDriverPassNamesItsModels(t *testing.T) {
	p := writeLog(t, "issue-42.log", driverStamp(altDriver),
		usageCall("m1", "gpt-5", 100, 50, 200, 20, 0.01),
		usageCall("m2", "claude-sonnet-4", 200, 100, 300, 30, 0.02),
		usageCall("m3", "gpt-5", 300, 150, 500, 50, 0.03),
	)
	rec, _, err := ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Passes) != 1 || !reflect.DeepEqual(rec.Passes[0].Models, []string{"claude-sonnet-4", "gpt-5"}) {
		t.Fatalf("passes = %+v", rec.Passes)
	}
}

func TestParseLogUnregisteredDriverIsReported(t *testing.T) {
	lines := append([]string{driverStamp("nope")}, workLog("2026-05-01T09:00:00Z", 1)...)
	p := writeLog(t, "issue-42.log", lines...)
	if _, _, err := ParseLog(p); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("ParseLog err = %v, want one naming the driver", err)
	}

	root := t.TempDir()
	putLog(t, root, "issue-42.log", lines...)
	s := openStore(t, root)
	n, err := s.Ingest()
	if !errors.Is(err, ErrUnreadableLog) || !strings.Contains(err.Error(), "nope") || n != 0 {
		t.Fatalf("Ingest = %d, %v", n, err)
	}
	if recs := records(t, s); len(recs) != 0 {
		t.Fatalf("records = %v, want none", ids(recs))
	}
}

func TestParseLogEmptyDriverStampReadsAsClaude(t *testing.T) {
	lines := append([]string{driverStamp("")}, workLog("2026-05-01T09:00:00Z", 1.5)...)
	rec, _, err := ParseLog(writeLog(t, "issue-42.log", lines...))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Driver != "" || len(rec.Passes) == 0 || rec.Passes[0].USD != 1.5 {
		t.Fatalf("record = %+v", rec)
	}
}

func TestParseLogStampNotFirstTypedLineIsReported(t *testing.T) {
	// A typed JSON line that is not an event precedes the stamp, so ReadStamp
	// cannot see it and would pick the default Driver for a log stamped with another.
	p := writeLog(t, "issue-42.log", `{"type":"unknown"}`+"\n", driverStamp(altDriver), usageCall("m1", "gpt-5", 1, 1, 1, 1, 0))
	if _, _, err := ParseLog(p); err == nil || !strings.Contains(err.Error(), "not first") {
		t.Fatalf("ParseLog err = %v, want a stamp-not-first error", err)
	}
}

func TestParseLogUntimestampedTypedLineBeforeClaudeStampStillStamped(t *testing.T) {
	// ReadStamp cannot see past the typed line, but the loop accepts the stamp;
	// both read it in the claude grammar, so the log ingests as stamped.
	lines := append([]string{`{"type":"system"}` + "\n", driverStamp("claude")}, workLog("2026-05-01T09:00:00Z", 1)...)
	rec, _, err := ParseLog(writeLog(t, "issue-42.log", lines...))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Attribution != AttributionStamped || rec.ID != RecordID("work", "42", stampClaim) {
		t.Fatalf("record = %+v, want the stamped Record", rec)
	}
}
