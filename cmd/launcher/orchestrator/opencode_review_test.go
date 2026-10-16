package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// opencodeTextLine is a single opencode `--format json` text event, the
// opencode counterpart of streamJSONOutcomeLine. text is a JSON string body, so
// newlines are written as the two characters \n like the claude fixtures.
func opencodeTextLine(text string) string {
	return `{"type":"text","part":{"text":"` + text + `"}}` + "\n"
}

// TestScanReviewLogOpencode covers the opencode half of issue #5332: a review
// verdict in opencode's text events must reach scanReviewLog with its findings
// bounded to the verdict event, as for claude.
func TestScanReviewLogOpencode(t *testing.T) {
	tests := []struct {
		name         string
		content      string
		wantVerdict  string
		wantFindings []string
		notFindings  []string
	}{
		{
			name: "block bounds findings at the next event",
			content: opencodeTextLine("VERDICT: BLOCK\\n\\n## Blocking\\n- run.go:42 -- missing nil check\\n\\n## Non-blocking\\n- none") +
				opencodeTextLine("unrelated trailing narration"),
			wantVerdict:  "BLOCK",
			wantFindings: []string{"VERDICT: BLOCK", "## Blocking", "run.go:42 -- missing nil check", "## Non-blocking"},
			notFindings:  []string{"unrelated trailing narration", "[reviewer]"},
		},
		{
			name:        "approve",
			content:     opencodeTextLine("VERDICT: APPROVE\\n\\n## Blocking\\n- none"),
			wantVerdict: "APPROVE",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "stream.log")
			if err := os.WriteFile(logPath, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			verdict, findings := scanReviewLog(logPath, "opencode")
			if verdict != tc.wantVerdict {
				t.Errorf("verdict = %q, want %q", verdict, tc.wantVerdict)
			}
			for _, want := range tc.wantFindings {
				if !strings.Contains(findings, want) {
					t.Errorf("findings = %q, want it to contain %q", findings, want)
				}
			}
			for _, bad := range tc.notFindings {
				if strings.Contains(findings, bad) {
					t.Errorf("findings = %q, want it not to contain %q", findings, bad)
				}
			}
		})
	}
}

// TestRunWithReviewPassSequenceOpencodeBlockThenApprove drives the
// implement, review-BLOCK, fix, review-APPROVE, land loop with opencode-shaped
// logs: the BLOCK's findings must seed the fix pass's brief and the APPROVE
// must reach the land pass (issue #5332).
func TestRunWithReviewPassSequenceOpencodeBlockThenApprove(t *testing.T) {
	chdirToFreshGitRepo(t)
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	// The orchestrator deletes a seeded brief once its pass ends, so the fake
	// fix pass copies it aside for the assertion below.
	fixBriefCopy := filepath.Join(dir, "fix-brief.txt")
	copyBrief := fmt.Sprintf(`prev=""; for a in "$@"; do [ "$prev" = "--prompt-file" ] && cp "$a" %q; prev="$a"; done`, fixBriefCopy)
	body := reviewLoopDriverBody(callLog, opencodeTextLine, opencodeTextLine, "run.go:1 -- bug", "none", "none", copyBrief)
	writeFakeDriverExec(t, dir, callLog, body)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	promptFile := filepath.Join(dir, "prompt.txt")
	reviewPromptFile := filepath.Join(dir, "review-prompt.txt")
	sessionFile := filepath.Join(dir, "session.txt")
	for path, content := range map[string]string{
		promptFile:       "ORIGINAL PROMPT TEXT",
		reviewPromptFile: "REVIEW PROMPT TEXT",
		sessionFile:      "--session-id fake-id",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var stdout bytes.Buffer
	rc, err := run(config{
		driver:           "opencode",
		promptFile:       promptFile,
		reviewPromptFile: reviewPromptFile,
		sessionFile:      sessionFile,
		logPath:          filepath.Join(dir, "stream.log"),
		stateFile:        filepath.Join(dir, "run-state.json"),
		maxReviewRounds:  3,
		maxSlices:        10,
	}, &stdout)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rc != 0 {
		t.Errorf("exit code = %d, want 0 (stdout: %q)", rc, stdout.String())
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(calls), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("driver-exec invocation count = %d, want 5 -- BLOCK then APPROVE should run a fix pass then land (log: %q)", len(lines), calls)
	}
	fixBrief, err := os.ReadFile(fixBriefCopy)
	if err != nil {
		t.Fatalf("read fix pass brief: %v", err)
	}
	if !strings.Contains(string(fixBrief), "run.go:1 -- bug") {
		t.Errorf("fix pass brief = %q, want it to carry the review's findings", fixBrief)
	}
	if !strings.Contains(stdout.String(), "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready") {
		t.Errorf("stdout = %q, want the land pass's outcome", stdout.String())
	}
}
