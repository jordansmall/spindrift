package claude_test

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/driver/claude"
)

func TestRenderTranscript_AssistantNarration_RendersImplementorLine(t *testing.T) {
	line := `{"type":"assistant","message":{"content":[{"type":"text","text":"Investigating the failing test."}]}}`
	path := claude.WriteLog(t, line)

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
	path := claude.WriteLog(t, line)

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
	path := claude.WriteLog(t, line)

	got, err := claude.RenderTranscript(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "[implementor]   -> ok, file updated\n"
	if got != want {
		t.Errorf("RenderTranscript = %q, want %q", got, want)
	}
}

func TestRenderTranscript_ToolResult_TruncatesOnRuneBoundary(t *testing.T) {
	long := strings.Repeat("a", 196) + strings.Repeat("€", 6)
	line := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"` + long + `"}]}}`
	path := claude.WriteLog(t, line)

	got, err := claude.RenderTranscript(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("RenderTranscript produced invalid UTF-8: %q", got)
	}
	want := "[implementor]   -> " + strings.Repeat("a", 196) + "€...\n"
	if got != want {
		t.Errorf("RenderTranscript = %q, want %q", got, want)
	}
}

func TestRenderTranscript_ToolResultError_PrefixesError(t *testing.T) {
	line := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"file not found","is_error":true}]}}`
	path := claude.WriteLog(t, line)

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
	path := claude.WriteLog(t, lines...)

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
