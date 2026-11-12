package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/passmachine"
)

// countDecodes wraps the decodeLog seam and returns the running decode count.
func countDecodes(t *testing.T) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	orig := decodeLog
	decodeLog = func(d driver.Driver, path string) (driverkit.PassLog, error) {
		n.Add(1)
		return orig(d, path)
	}
	t.Cleanup(func() { decodeLog = orig })
	return &n
}

// A Pass's reads (verdict, outcome, review findings, usage) all consume one
// decode of its log.
func TestPassReadsShareOneDecode(t *testing.T) {
	decodes := countDecodes(t)
	logPath := filepath.Join(t.TempDir(), "stream.log")
	content := streamJSONOutcomeLine("VERDICT: APPROVE\\n\\n## Non-blocking\\n- none") +
		streamJSONFinalResult("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done") +
		streamJSONResultLine(10, 5, 0.25)
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	pl := decodePassLog(logPath, "claude")
	scanPassLog(pl, passmachine.KindLegacy)
	scanPassOutcome(pl)
	scanReviewLog(pl)
	report := config{driver: "claude"}.passReport(pl)

	if got := decodes.Load(); got != 1 {
		t.Errorf("decodes = %d, want 1 (one decode for every read of the pass)", got)
	}
	if report.Totals.TotalTokens() != 15 {
		t.Errorf("passReport tokens = %d, want 15", report.Totals.TotalTokens())
	}
}

// A whole run decodes each Pass's log once: six passes, six decodes, with the
// delta-review gate reusing the land pass's decode for its outcome.
func TestRunDecodesEachPassLogOnce(t *testing.T) {
	chdirToFreshGitRepo(t)
	decodes := countDecodes(t)
	f := newDeltaReviewLoopFixture(t)
	deltaApprove := teeStreamJSONStep(streamJSONOutcomeLine("VERDICT: APPROVE\\n\\n## Blocking\\n- none\\n\\n## Non-blocking\\n- none"))
	writeFakeDriverExec(t, f.dir, f.callLog, deltaReviewFakeDriverBody(f.callLog, "none", "", "", deltaApprove))
	t.Setenv("PATH", f.dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout bytes.Buffer
	if _, err := run(f.config(10), &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}

	passes := f.callCount(t)
	if passes != 6 {
		t.Fatalf("driver-exec invocation count = %d, want 6 (delta-review fires)", passes)
	}
	if got := decodes.Load(); got != int64(passes) {
		t.Errorf("decodes = %d, want %d (one decode per pass)", got, passes)
	}
}

// runDeltaFixture runs the six-call delta-review loop with resultTextFile set
// and returns the file's path, the decode count, and what each fake driver-exec
// call saw of the file when it started.
func runDeltaFixture(t *testing.T, deltaReviewStep string) (f deltaReviewLoopFixture, resultFile string, decodes *atomic.Int64, seen []string) {
	t.Helper()
	chdirToFreshGitRepo(t)
	decodes = countDecodes(t)
	f = newDeltaReviewLoopFixture(t)
	resultFile = filepath.Join(f.dir, "result-text")
	seenLog := filepath.Join(f.dir, "seen.log")
	probe := `if [ -e "` + resultFile + `" ]; then echo present >> "` + seenLog + `"; else echo absent >> "` + seenLog + `"; fi
`
	writeFakeDriverExec(t, f.dir, f.callLog, probe+deltaReviewFakeDriverBody(f.callLog, "none", "", "", deltaReviewStep))
	t.Setenv("PATH", f.dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cfg := f.config(10)
	cfg.resultTextFile = resultFile
	var stdout bytes.Buffer
	if _, err := run(cfg, &stdout); err != nil {
		t.Fatalf("run: %v", err)
	}
	b, err := os.ReadFile(seenLog)
	if err != nil {
		t.Fatal(err)
	}
	return f, resultFile, decodes, strings.Fields(string(b))
}

// The result-text file holds the final Pass's result text, written from the
// Pass's one decode, and is gone while a Pass is running so it never outlives
// the log content it was decoded from.
func TestResultTextFileHoldsLastPassFromItsOneDecode(t *testing.T) {
	deltaApprove := teeStreamJSONStep(streamJSONOutcomeLine("VERDICT: APPROVE\\n\\n## Blocking\\n- none\\n\\n## Non-blocking\\n- none"))
	f, resultFile, decodes, seen := runDeltaFixture(t, deltaApprove)

	got, err := os.ReadFile(resultFile)
	if err != nil {
		t.Fatalf("result-text file: %v", err)
	}
	if want := "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc"; !strings.Contains(string(got), want) {
		t.Errorf("result-text file = %q, want it to carry %q", got, want)
	}
	if n := int64(f.callCount(t)); decodes.Load() != n {
		t.Errorf("decodes = %d, want %d (writing the file must not decode again)", decodes.Load(), n)
	}
	for i, s := range seen[:5] {
		if s != "absent" {
			t.Errorf("pass %d saw the result-text file %s at start, want absent", i+1, s)
		}
	}
}

// A delta-review BLOCK appends a corrective result to the land pass's log
// after it was decoded, so the file no longer matches and must go.
func TestResultTextFileRemovedWhenDeltaBlockRewritesTheLog(t *testing.T) {
	block := teeStreamJSONStep(streamJSONOutcomeLine("VERDICT: BLOCK\\n\\n## Blocking\\n- run.go:1 -- bug\\n\\n## Non-blocking\\n- none"))
	_, resultFile, _, _ := runDeltaFixture(t, block)

	if _, err := os.Stat(resultFile); !os.IsNotExist(err) {
		t.Errorf("result-text file after a delta BLOCK: stat err = %v, want not-exist", err)
	}
}
