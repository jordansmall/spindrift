package opencode_test

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/opencode"
)

// TestRenderTranscript_SurfacesTextEvents verifies that RenderTranscript
// surfaces every type:"text" event's part.text, including a SPINDRIFT_OUTCOME
// line and a VERDICT: line — the exact prose the orchestrator's
// scanPassLog/scanReviewLog scan the rendered transcript for.
func TestRenderTranscript_SurfacesTextEvents(t *testing.T) {
	logPath := opencode.WriteLog(t,
		`{"type":"text","part":{"text":"Investigating the issue."}}`,
		`{"type":"error","error":"rate_limit_error"}`,
		`{"type":"text","part":{"text":"VERDICT: APPROVE"}}`,
		`{"type":"text","part":{"text":"SPINDRIFT_OUTCOME issue=42 landing=https://example/pr/1 status=ready note=done"}}`,
	)

	got, err := opencode.RenderTranscript(logPath)
	if err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	if !strings.Contains(got, "Investigating the issue.") {
		t.Errorf("missing first text event's prose: %q", got)
	}
	if !strings.Contains(got, "VERDICT: APPROVE") {
		t.Errorf("missing VERDICT line: %q", got)
	}
	if !strings.Contains(got, "SPINDRIFT_OUTCOME issue=42 landing=https://example/pr/1 status=ready note=done") {
		t.Errorf("missing SPINDRIFT_OUTCOME line verbatim: %q", got)
	}
}

// TestRenderTranscript_MissingFile verifies the missing-log-file contract
// shared with the claude Driver's RenderTranscript.
func TestRenderTranscript_MissingFile(t *testing.T) {
	got, err := opencode.RenderTranscript("/does/not/exist.log")
	if err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty string for missing file", got)
	}
}
