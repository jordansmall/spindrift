package claude_test

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/claude"
)

func TestRenderTranscript_AssistantNarration_RendersImplementorLine(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"Investigating the failing test."}]}}`
	path := writeLog(t, line)

	got, err := claude.RenderTranscript(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "[implementor] Investigating the failing test.\n"
	if got != want {
		t.Errorf("RenderTranscript = %q, want %q", got, want)
	}
}

func TestRenderTranscript_ToolUse_RendersNameAndTarget(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"main.go"}}]}}`
	path := writeLog(t, line)

	got, err := claude.RenderTranscript(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "[implementor] Read(main.go)\n"
	if got != want {
		t.Errorf("RenderTranscript = %q, want %q", got, want)
	}
}

func TestRenderTranscript_ToolResult_RendersSummarizedResult(t *testing.T) {
	line := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok, file updated"}]}}`
	path := writeLog(t, line)

	got, err := claude.RenderTranscript(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "[implementor]   -> ok, file updated\n"
	if got != want {
		t.Errorf("RenderTranscript = %q, want %q", got, want)
	}
}

func TestRenderTranscript_ToolResultError_PrefixesError(t *testing.T) {
	line := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file not found","is_error":true}]}}`
	path := writeLog(t, line)

	got, err := claude.RenderTranscript(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "[implementor]   -> error: file not found\n"
	if got != want {
		t.Errorf("RenderTranscript = %q, want %q", got, want)
	}
}

func TestRenderTranscript_SubagentNarration_PrefixesSubagentRole(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_scout","name":"Task","input":{"subagent_type":"scout"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Found the seam."}]},"parent_tool_use_id":"toolu_scout"}`,
	}
	path := writeLog(t, lines...)

	got, err := claude.RenderTranscript(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "[implementor] Task(scout)\n[scout] Found the seam.\n"
	if got != want {
		t.Errorf("RenderTranscript = %q, want %q", got, want)
	}
}

func TestRenderTranscript_MissingFile_ReturnsEmpty(t *testing.T) {
	got, err := claude.RenderTranscript(filepath.Join(t.TempDir(), "missing.log"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty for missing log", got)
	}
}
