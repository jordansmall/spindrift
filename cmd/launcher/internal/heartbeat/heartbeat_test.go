package heartbeat_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/heartbeat"
)

func newWriter(issue string, status *bytes.Buffer) *heartbeat.Writer {
	return heartbeat.New(&bytes.Buffer{}, issue, status, time.Hour)
}

func newWriterRaw(raw *bytes.Buffer, issue string, status *bytes.Buffer) *heartbeat.Writer {
	return heartbeat.New(raw, issue, status, time.Hour)
}

// TestWriterPassesRawBytesUnchanged verifies the raw log writer receives every
// byte written to the heartbeat writer, byte-for-byte.
func TestWriterPassesRawBytesUnchanged(t *testing.T) {
	var raw bytes.Buffer
	var status bytes.Buffer
	w := newWriterRaw(&raw, "42", &status)

	input := `{"type":"system","session_id":"s1"}` + "\n"
	if _, err := fmt.Fprint(w, input); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if raw.String() != input {
		t.Errorf("raw: got %q, want %q", raw.String(), input)
	}
}

// TestWriterPassesMultiChunkRaw verifies byte-exact passthrough when input
// arrives in multiple Write calls that split across a newline boundary.
func TestWriterPassesMultiChunkRaw(t *testing.T) {
	var raw bytes.Buffer
	var status bytes.Buffer
	w := newWriterRaw(&raw, "1", &status)

	p1 := `{"type":"system"`
	p2 := `}` + "\n"
	fmt.Fprint(w, p1)
	fmt.Fprint(w, p2)

	want := p1 + p2
	if raw.String() != want {
		t.Errorf("raw: got %q, want %q", raw.String(), want)
	}
}

// TestWriterEmitsHeartbeatOnToolChange verifies that when an assistant event
// contains a tool_use block, a heartbeat line is emitted to the status writer.
func TestWriterEmitsHeartbeatOnToolChange(t *testing.T) {
	var status bytes.Buffer
	w := newWriter("42", &status)

	event := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}` + "\n"
	fmt.Fprint(w, event)

	out := status.String()
	if !strings.Contains(out, "#42") {
		t.Errorf("heartbeat missing issue prefix: %q", out)
	}
	if !strings.Contains(out, "Edit") {
		t.Errorf("heartbeat missing tool name: %q", out)
	}
}

// TestWriterIncludesFilePathInTool verifies that the file_path from a tool_use
// input is included in the heartbeat line, e.g. "Edit(main.go)".
func TestWriterIncludesFilePathInTool(t *testing.T) {
	var status bytes.Buffer
	w := newWriter("7", &status)

	event := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go","new_string":"x"}}]}}` + "\n"
	fmt.Fprint(w, event)

	out := status.String()
	if !strings.Contains(out, "Edit(main.go)") {
		t.Errorf("heartbeat should contain Edit(main.go): %q", out)
	}
}

// TestWriterEmitsOnResultEvent verifies that on a result event the turn count
// is reflected and a heartbeat is emitted.
func TestWriterEmitsOnResultEvent(t *testing.T) {
	var status bytes.Buffer
	w := newWriter("9", &status)

	event := `{"type":"result","num_turns":7,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	fmt.Fprint(w, event)

	out := status.String()
	if !strings.Contains(out, "#9") {
		t.Errorf("heartbeat missing issue: %q", out)
	}
	if !strings.Contains(out, "7 turn") {
		t.Errorf("heartbeat missing turns: %q", out)
	}
}

// TestWriterTolerateMalformedJSON verifies that non-JSON and malformed lines do
// not cause a panic and do not disrupt the raw passthrough.
func TestWriterTolerateMalformedJSON(t *testing.T) {
	var raw bytes.Buffer
	var status bytes.Buffer
	w := newWriterRaw(&raw, "3", &status)

	lines := "not json at all\n{broken: json}\n\x00\x01\x02\n"
	fmt.Fprint(w, lines)

	if raw.String() != lines {
		t.Errorf("raw passthrough broken: got %q, want %q", raw.String(), lines)
	}
	// No panic — test passes if we reach here.
}

// TestWriterThrottlesSameToolRepeat verifies that repeated events with the same
// tool do not produce a heartbeat line for every event when throttle is high.
func TestWriterThrottlesSameToolRepeat(t *testing.T) {
	var status bytes.Buffer
	// Use 1-hour throttle so time-based emission cannot trigger.
	w := heartbeat.New(&bytes.Buffer{}, "5", &status, time.Hour)

	event := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"

	// Write the same event 5 times.
	for i := 0; i < 5; i++ {
		fmt.Fprint(w, event)
	}

	// Only the first emission should have been triggered (tool change).
	lines := strings.Split(strings.TrimRight(status.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 heartbeat line, got %d: %q", len(lines), status.String())
	}
}

// TestWriterEmitsOnNewTool verifies that switching to a different tool triggers
// a new heartbeat emission even under a high throttle.
func TestWriterEmitsOnNewTool(t *testing.T) {
	var status bytes.Buffer
	w := heartbeat.New(&bytes.Buffer{}, "11", &status, time.Hour)

	ev1 := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	ev2 := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"b.go"}}]}}` + "\n"

	fmt.Fprint(w, ev1)
	fmt.Fprint(w, ev2)

	out := status.String()
	if !strings.Contains(out, "Read") {
		t.Errorf("missing Read in heartbeat: %q", out)
	}
	if !strings.Contains(out, "Edit") {
		t.Errorf("missing Edit in heartbeat: %q", out)
	}
}

// TestWriterNarrationTrimming verifies that narration text is trimmed to a single
// line bounded to 120 characters.
func TestWriterNarrationTrimming(t *testing.T) {
	long := strings.Repeat("x", 200)
	var status bytes.Buffer
	w := heartbeat.New(&bytes.Buffer{}, "99", &status, time.Hour)

	event := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + long + `"}]}}` + "\n"
	fmt.Fprint(w, event)

	out := strings.TrimRight(status.String(), "\n")
	lines := strings.Split(out, "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 heartbeat line, got %d: %q", len(lines), status.String())
	}
	// Line = "#99 · <text>\n"; text portion must be ≤120 chars.
	prefix := "#99 \xc2\xb7 "
	if !strings.HasPrefix(lines[0], prefix) {
		t.Errorf("line missing prefix %q: %q", prefix, lines[0])
	}
	textPart := strings.TrimPrefix(lines[0], prefix)
	if len(textPart) > 120 {
		t.Errorf("narration text %d chars, want ≤120", len(textPart))
	}
}

// TestWriterSubagentNarrationDropped verifies that assistant text blocks that
// carry a parent_tool_use_id (subagent output) are silently dropped — they are
// not emitted as heartbeat lines. The raw log still receives every byte.
func TestWriterSubagentNarrationDropped(t *testing.T) {
	var raw bytes.Buffer
	var status bytes.Buffer
	w := heartbeat.New(&raw, "55", &status, time.Hour)

	event := `{"type":"assistant","parent_tool_use_id":"tu_abc","message":{"content":[{"type":"text","text":"subagent says hello"}]}}` + "\n"
	fmt.Fprint(w, event)

	// Subagent narration must not appear in the heartbeat stream.
	if strings.Contains(status.String(), "subagent says hello") {
		t.Errorf("subagent narration must not appear in heartbeat: %q", status.String())
	}
	// Raw log must still receive every byte.
	if raw.String() != event {
		t.Errorf("raw passthrough broken: got %q, want %q", raw.String(), event)
	}
}

// TestWriterNarrationBeforeTool verifies that when an assistant message contains
// both a text block and a tool_use block, the narration line appears before the
// tool line in the output.
func TestWriterNarrationBeforeTool(t *testing.T) {
	var status bytes.Buffer
	w := heartbeat.New(&bytes.Buffer{}, "42", &status, time.Hour)

	// Message with text then tool_use.
	event := `{"type":"assistant","message":{"content":[{"type":"text","text":"I will edit the file."},{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}` + "\n"
	fmt.Fprint(w, event)

	out := status.String()
	narrationIdx := strings.Index(out, "I will edit")
	toolIdx := strings.Index(out, "Edit(main.go)")
	if narrationIdx < 0 {
		t.Fatalf("narration not found in output: %q", out)
	}
	if toolIdx < 0 {
		t.Fatalf("tool not found in output: %q", out)
	}
	if narrationIdx > toolIdx {
		t.Errorf("narration (%d) must appear before tool (%d): %q", narrationIdx, toolIdx, out)
	}
}

// TestWriterNarrationEmptySkipped verifies that empty or whitespace-only text
// blocks do not produce a heartbeat line.
func TestWriterNarrationEmptySkipped(t *testing.T) {
	for _, txt := range []string{"", "   ", "\t\n"} {
		var status bytes.Buffer
		w := heartbeat.New(&bytes.Buffer{}, "8", &status, time.Hour)
		// JSON-encode the text value to handle whitespace safely.
		import_txt := fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"text","text":%q}]}}`, txt)
		fmt.Fprintln(w, import_txt)
		if status.Len() > 0 {
			t.Errorf("text=%q: unexpected heartbeat: %q", txt, status.String())
		}
	}
}

// TestFormatHeartbeatShape verifies the output shape from FormatHeartbeat.
func TestFormatHeartbeatShape(t *testing.T) {
	cases := []struct {
		issue    string
		turns    int
		lastTool string
		phase    string
		wantSubs []string
	}{
		{"42", 15, "Edit(main.go)", "edit", []string{"#42", "[edit]", "15 turn", "Edit(main.go)"}},
		{"1", 1, "Bash(ls)", "explore", []string{"#1", "[explore]", "1 turn", "Bash(ls)"}},
		{"7", 0, "", "explore", []string{"#7", "[explore]"}},
		{"3", 3, "", "test", []string{"#3", "[test]", "3 turn"}},
	}
	for _, tc := range cases {
		got := heartbeat.FormatHeartbeat(tc.issue, tc.turns, tc.lastTool, tc.phase)
		for _, sub := range tc.wantSubs {
			if !strings.Contains(got, sub) {
				t.Errorf("FormatHeartbeat(%q,%d,%q,%q) = %q, missing %q",
					tc.issue, tc.turns, tc.lastTool, tc.phase, got, sub)
			}
		}
	}
}

// TestWriterHeartbeatIncludesPhase verifies that the heartbeat line includes
// the phase tag derived from the tool used.
func TestWriterHeartbeatIncludesPhase(t *testing.T) {
	var status bytes.Buffer
	w := heartbeat.New(&bytes.Buffer{}, "42", &status, time.Hour)

	event := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}` + "\n"
	fmt.Fprint(w, event)

	out := status.String()
	if !strings.Contains(out, "[edit]") {
		t.Errorf("heartbeat missing [edit] phase tag: %q", out)
	}
}

// TestWriterPhaseTransitionEmitsLine verifies that switching to a different
// phase triggers a heartbeat emission even when the throttle is high.
func TestWriterPhaseTransitionEmitsLine(t *testing.T) {
	var status bytes.Buffer
	w := heartbeat.New(&bytes.Buffer{}, "11", &status, time.Hour)

	ev1 := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	ev2 := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"b.go"}}]}}` + "\n"

	fmt.Fprint(w, ev1)
	fmt.Fprint(w, ev2)

	out := status.String()
	if !strings.Contains(out, "[explore]") {
		t.Errorf("missing [explore] phase tag: %q", out)
	}
	if !strings.Contains(out, "[edit]") {
		t.Errorf("missing [edit] phase tag after transition: %q", out)
	}
}

// TestWriterNarrationText verifies that a text content block in an assistant
// event emits a heartbeat line containing the narration text.
func TestWriterNarrationText(t *testing.T) {
	var status bytes.Buffer
	w := heartbeat.New(&bytes.Buffer{}, "8", &status, time.Hour)

	event := `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}` + "\n"
	fmt.Fprint(w, event)

	out := status.String()
	if !strings.Contains(out, "#8") {
		t.Errorf("heartbeat missing issue prefix: %q", out)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("heartbeat missing narration text: %q", out)
	}
}
