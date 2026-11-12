package main

import (
	"bytes"
	"os"
	"path/filepath"
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
