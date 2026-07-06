package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/runner"
)

// writeUsageLog writes a log file containing an outcome line and a result event.
func writeUsageLog(t *testing.T, dir, issNum, outcomeLine, resultEvent string) {
	t.Helper()
	path := filepath.Join(dir, "logs", "issue-"+issNum+".log")
	var lines []string
	if outcomeLine != "" {
		lines = append(lines, outcomeLine)
	}
	if resultEvent != "" {
		lines = append(lines, resultEvent)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPrintOutcomeReport_PostsUsageComment_Blocked verifies that a usage-summary
// comment is posted to the forge when the outcome is "blocked".
func TestPrintOutcomeReport_PostsUsageComment_Blocked(t *testing.T) {
	dir := tempLogDir(t)
	const issNum = "42"
	const prURL = "https://github.com/owner/repo/pull/99"

	outcomeLine := "SPINDRIFT_OUTCOME issue=" + issNum + " pr=" + prURL + " status=blocked note=tests failing"
	resultEvent := `{"type":"result","num_turns":5,"total_cost_usd":0.25,"duration_ms":3000,"duration_api_ms":2000,"usage":{"input_tokens":500,"output_tokens":100,"cache_read_input_tokens":50,"cache_creation_input_tokens":10}}`
	writeUsageLog(t, dir, issNum, outcomeLine, resultEvent)

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	fr := runner.NewFake()
	c := baseConfig()

	printOutcomeReport(c, fc, dir, fr, []issue{{number: issNum, title: "test issue"}})

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted, got %d", len(fc.CommentCalls))
	}
	body := fc.CommentCalls[0].Body
	if !strings.Contains(body, "0.25") {
		t.Errorf("comment should contain cost 0.25; got: %q", body)
	}
	if !strings.Contains(body, "500") {
		t.Errorf("comment should contain input_tokens 500; got: %q", body)
	}
	if !strings.Contains(body, "5") {
		t.Errorf("comment should contain num_turns 5; got: %q", body)
	}
}

// TestPrintOutcomeReport_UsageMissing_NoCrash verifies that when no result event
// is in the log, printOutcomeReport degrades gracefully without crashing and
// still posts a comment (marked "unavailable").
func TestPrintOutcomeReport_UsageMissing_NoCrash(t *testing.T) {
	dir := tempLogDir(t)
	const issNum = "7"
	const prURL = "https://github.com/owner/repo/pull/7"

	// Log has outcome line but no result event.
	outcomeLine := "SPINDRIFT_OUTCOME issue=" + issNum + " pr=" + prURL + " status=blocked note=no result"
	writeUsageLog(t, dir, issNum, outcomeLine, "")

	fc := forge.NewFake()
	fc.SetIssue(forge.Issue{Number: issNum, Labels: []string{"agent-in-progress"}})

	fr := runner.NewFake()
	c := baseConfig()

	// Must not panic or error.
	printOutcomeReport(c, fc, dir, fr, []issue{{number: issNum, title: "test issue"}})

	if len(fc.CommentCalls) != 1 {
		t.Fatalf("want 1 comment posted even without usage data, got %d", len(fc.CommentCalls))
	}
	body := fc.CommentCalls[0].Body
	if !strings.Contains(body, "unavailable") {
		t.Errorf("comment should say unavailable when usage missing; got: %q", body)
	}
}
