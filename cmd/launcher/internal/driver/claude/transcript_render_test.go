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

func TestRenderTranscript_ToolResult_TruncatesOnRuneBoundary_FourByteRune(t *testing.T) {
	long := strings.Repeat("a", 196) + strings.Repeat("🎉", 6)
	line := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"` + long + `"}]}}`
	path := claude.WriteLog(t, line)

	got, err := claude.RenderTranscript(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("RenderTranscript produced invalid UTF-8: %q", got)
	}
	want := "[implementor]   -> " + strings.Repeat("a", 196) + "🎉...\n"
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

func TestRenderTranscript_SubagentNarration_AgentToolName_PrefixesReviewerRole(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_reviewer","name":"Agent","input":{"subagent_type":"reviewer"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Looks good."}]},"parent_tool_use_id":"toolu_reviewer"}`,
	}
	path := claude.WriteLog(t, lines...)

	got, err := claude.RenderTranscript(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "[implementor] Agent(reviewer)\n[reviewer] Looks good.\n"
	if got != want {
		t.Errorf("RenderTranscript = %q, want %q", got, want)
	}
}

func TestRenderTranscript_NestedSubagent_PrefixesOwnRole(t *testing.T) {
	lines := []string{
		// Implementor spawns A (subagent_type "researcher").
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_A","name":"Agent","input":{"subagent_type":"researcher"}}]}}`,
		// A's own message spawns B (subagent_type "worker"); this event carries
		// parent_tool_use_id "toolu_A" (A is the actor) AND B's spawn block.
		`{"type":"assistant","parent_tool_use_id":"toolu_A","message":{"content":[{"type":"tool_use","id":"toolu_B","name":"Agent","input":{"subagent_type":"worker"}}]}}`,
		// B's own message, nested two levels deep under the implementor.
		`{"type":"assistant","parent_tool_use_id":"toolu_B","message":{"content":[{"type":"text","text":"Deep result."}]}}`,
	}
	path := claude.WriteLog(t, lines...)

	got, err := claude.RenderTranscript(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "[worker] Deep result.") {
		t.Errorf("RenderTranscript = %q, want it to contain %q (B's own role, not the generic fallback)", got, "[worker] Deep result.")
	}
	if strings.Contains(got, "[subagent] Deep result.") {
		t.Errorf("RenderTranscript = %q, nested subagent B must not fall back to the generic \"subagent\" role", got)
	}
}

func TestRenderTranscriptWithRole_TopLevelEventsUseGivenRole(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Looks good overall."}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok, file updated"}]}}`,
	}
	path := claude.WriteLog(t, lines...)

	got, err := claude.RenderTranscriptWithRole(path, "reviewer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "[reviewer] Looks good overall.\n[reviewer]   -> ok, file updated\n"
	if got != want {
		t.Errorf("RenderTranscriptWithRole = %q, want %q", got, want)
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
