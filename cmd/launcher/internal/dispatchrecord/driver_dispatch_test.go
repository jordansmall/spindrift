package dispatchrecord

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/hostop"
)

const opencodeCorpus = "../driver/testdata/corpus/opencode/"

func readCorpus(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(opencodeCorpus + name + "/pass.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func driverStamp(driver string) string {
	start := hostop.DispatchStart{
		RecordID:    RecordID("work", "42", stampClaim),
		Kind:        "work",
		DispatchKey: "42",
		ClaimTime:   stampClaim,
		Started:     stampClaim,
		Driver:      driver,
	}
	return opLine(hostop.SpindriftOp{Op: "dispatch_start", Start: &start})
}

func TestParseLogReadsOpencodePassLogThroughItsDriver(t *testing.T) {
	p := writeLog(t, "issue-longest-line.log", readCorpus(t, "real-normal"))
	rec, _, err := ParseLog(p)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Driver != "opencode" || rec.Attribution != AttributionStamped {
		t.Fatalf("record = %+v", rec)
	}
	if rec.Outcome != "complete" || rec.OutcomeSource != OutcomeSourceSettled || rec.BoxStatus != "ready" {
		t.Fatalf("outcome = %q/%q status %q", rec.Outcome, rec.OutcomeSource, rec.BoxStatus)
	}
	if rec.RoleModels["main"] != "github-copilot/claude-sonnet-5.5" {
		t.Fatalf("role models = %v", rec.RoleModels)
	}
	var in, out, read, write, calls int
	var usd float64
	for _, ps := range rec.Passes {
		// The corpus reports no model on any step, so each pass names none.
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
	if len(rec.Passes) != 3 || in != 32 || out != 2489 || read != 221311 || write != 54014 || calls != 13 || usd <= 0 {
		t.Fatalf("passes = %+v (in %d out %d read %d write %d calls %d usd %v)", rec.Passes, in, out, read, write, calls, usd)
	}
}

func TestParseLogOpencodePassNamesItsModels(t *testing.T) {
	p := writeLog(t, "issue-42.log", driverStamp("opencode"), readCorpus(t, "run-usage-sample"))
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
	// cannot see it and would pick the claude grammar for an opencode log.
	p := writeLog(t, "issue-42.log", `{"type":"unknown"}`+"\n", driverStamp("opencode"), readCorpus(t, "run-usage-sample"))
	if _, _, err := ParseLog(p); err == nil || !strings.Contains(err.Error(), "not first") {
		t.Fatalf("ParseLog err = %v, want a stamp-not-first error", err)
	}
}
