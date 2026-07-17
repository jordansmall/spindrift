package claude_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/claude"
)

func newWriter(issue string, status *bytes.Buffer) *claude.Writer {
	return claude.New(&bytes.Buffer{}, issue, status)
}

func newWriterRaw(raw *bytes.Buffer, issue string, status *bytes.Buffer) *claude.Writer {
	return claude.New(raw, issue, status)
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

// TestWriterEmitsHeartbeatOnToolChange verifies that accumulated tool calls
// produce a count line when a result event arrives.
func TestWriterEmitsHeartbeatOnToolChange(t *testing.T) {
	var status bytes.Buffer
	w := newWriter("42", &status)

	toolEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}` + "\n"
	resultEv := `{"type":"result","num_turns":1}` + "\n"
	fmt.Fprint(w, toolEv)
	fmt.Fprint(w, resultEv)

	out := status.String()
	if !strings.Contains(out, "#42") {
		t.Errorf("heartbeat missing issue prefix: %q", out)
	}
	if !strings.Contains(out, "edit") {
		t.Errorf("heartbeat missing tool kind 'edit': %q", out)
	}
}

// TestWriterToolCountsShowKind verifies that the count line shows tool kinds
// ("1 edit") rather than per-call labels ("Edit(main.go)").
func TestWriterToolCountsShowKind(t *testing.T) {
	var status bytes.Buffer
	w := newWriter("7", &status)

	toolEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go","new_string":"x"}}]}}` + "\n"
	resultEv := `{"type":"result","num_turns":1}` + "\n"
	fmt.Fprint(w, toolEv)
	fmt.Fprint(w, resultEv)

	out := status.String()
	if !strings.Contains(out, "1 edit") {
		t.Errorf("count line missing '1 edit': %q", out)
	}
	if strings.Contains(out, "Edit(main.go)") {
		t.Errorf("count line must not contain per-call label 'Edit(main.go)': %q", out)
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

// TestNewNoThrottleArg verifies that New() accepts exactly three arguments
// (no throttle) after the time-based fallback was removed.
func TestNewNoThrottleArg(t *testing.T) {
	w := claude.New(&bytes.Buffer{}, "1", &bytes.Buffer{})
	if w == nil {
		t.Fatal("New returned nil")
	}
}

// TestWriterBareResultEmitsNothing verifies that a result event with no
// num_turns and no accumulated tool counts emits nothing.
func TestWriterBareResultEmitsNothing(t *testing.T) {
	var status bytes.Buffer
	w := newWriter("42", &status)

	event := `{"type":"result"}` + "\n"
	fmt.Fprint(w, event)

	if status.Len() > 0 {
		t.Errorf("bare result must emit nothing, got: %q", status.String())
	}
}

// TestWriterResultWithoutTurnsFlushesCountsOnly verifies that a result event
// without num_turns flushes accumulated counts but emits no bare heartbeat line.
func TestWriterResultWithoutTurnsFlushesCountsOnly(t *testing.T) {
	var status bytes.Buffer
	w := newWriter("42", &status)

	toolEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	resultEv := `{"type":"result"}` + "\n"
	fmt.Fprint(w, toolEv)
	fmt.Fprint(w, resultEv)

	out := status.String()
	if !strings.Contains(out, "1 read") {
		t.Errorf("count line missing '1 read': %q", out)
	}
	// No bare heartbeat line: no line that is just "#42" or "#42 [explore]" with nothing useful after.
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.HasSuffix(line, "]") || line == "#42" {
			t.Errorf("bare heartbeat line emitted: %q", line)
		}
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

// TestWriterThrottlesSameToolRepeat verifies that repeated same-kind tool calls
// accumulate into a count, not a flood of individual lines.
func TestWriterThrottlesSameToolRepeat(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "5", &status)

	readEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	narEv := `{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}` + "\n"

	// Write the same event 5 times then trigger with narration.
	for i := 0; i < 5; i++ {
		fmt.Fprint(w, readEv)
	}
	fmt.Fprint(w, narEv)

	out := status.String()
	// Header + narration + count = 3 lines total, not 5 per-tool lines.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines (header + narration + count), got %d: %q", len(lines), out)
	}
	if !strings.Contains(out, "5 read") {
		t.Errorf("count line missing '5 reads': %q", out)
	}
}

// TestWriterEmitsOnNewTool verifies that switching tool kinds emits count lines
// for each phase and kind.
func TestWriterEmitsOnNewTool(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "11", &status)

	ev1 := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	ev2 := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"b.go"}}]}}` + "\n"
	resultEv := `{"type":"result","num_turns":2}` + "\n"

	fmt.Fprint(w, ev1)
	fmt.Fprint(w, ev2)
	fmt.Fprint(w, resultEv)

	out := status.String()
	if !strings.Contains(out, "read") {
		t.Errorf("missing 'read' count in heartbeat: %q", out)
	}
	if !strings.Contains(out, "edit") {
		t.Errorf("missing 'edit' count in heartbeat: %q", out)
	}
}

// TestWriterNarrationIncludesPhase verifies that narration lines carry the
// current phase tag derived from the most recent tool.
func TestWriterNarrationIncludesPhase(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "42", &status)

	// First establish a phase via a tool event.
	toolEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	fmt.Fprint(w, toolEv)
	status.Reset()

	// Now send a narration event; it should carry the explore phase.
	narEv := `{"type":"assistant","message":{"content":[{"type":"text","text":"Checking the file."}]}}` + "\n"
	fmt.Fprint(w, narEv)

	out := status.String()
	if !strings.Contains(out, "[explore]") {
		t.Errorf("narration missing [explore] phase tag: %q", out)
	}
	if !strings.Contains(out, "Checking the file") {
		t.Errorf("narration text missing: %q", out)
	}
}

// TestWriterNarrationTrimming verifies that narration text is trimmed to a single
// line bounded to 120 characters.
func TestWriterNarrationTrimming(t *testing.T) {
	long := strings.Repeat("x", 200)
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "99", &status)

	event := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + long + `"}]}}` + "\n"
	fmt.Fprint(w, event)

	out := strings.TrimRight(status.String(), "\n")
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines (header + narration), got %d: %q", len(lines), status.String())
	}
	// lines[1] is the narration: "#99 · <text>"; text portion must be ≤120 chars.
	prefix := "#99 \xc2\xb7 "
	if !strings.HasPrefix(lines[1], prefix) {
		t.Errorf("narration line missing prefix %q: %q", prefix, lines[1])
	}
	textPart := strings.TrimPrefix(lines[1], prefix)
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
	w := claude.New(&raw, "55", &status)

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

// TestWriterNarrationBeforeTool verifies that narration text appears before the
// count line for accumulated tools in the output.
func TestWriterNarrationBeforeTool(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "42", &status)

	// First narration starts the group.
	narEv1 := `{"type":"assistant","message":{"content":[{"type":"text","text":"I will edit the file."}]}}` + "\n"
	// Tool accumulates after narration.
	toolEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}` + "\n"
	// Second narration flushes the count.
	narEv2 := `{"type":"assistant","message":{"content":[{"type":"text","text":"Done editing."}]}}` + "\n"

	fmt.Fprint(w, narEv1)
	fmt.Fprint(w, toolEv)
	fmt.Fprint(w, narEv2)

	out := status.String()
	narrationIdx := strings.Index(out, "I will edit")
	countIdx := strings.Index(out, "1 edit")
	if narrationIdx < 0 {
		t.Fatalf("narration not found in output: %q", out)
	}
	if countIdx < 0 {
		t.Fatalf("count line '1 edit' not found in output: %q", out)
	}
	if narrationIdx > countIdx {
		t.Errorf("narration (%d) must appear before count (%d): %q", narrationIdx, countIdx, out)
	}
}

// TestWriterNarrationEmptySkipped verifies that empty or whitespace-only text
// blocks do not produce a heartbeat line.
func TestWriterNarrationEmptySkipped(t *testing.T) {
	for _, txt := range []string{"", "   ", "\t\n"} {
		var status bytes.Buffer
		w := claude.New(&bytes.Buffer{}, "8", &status)
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
		role     string
		phase    string
		wantSubs []string
	}{
		{"42", 15, "Edit(main.go)", "implementor", "edit", []string{"#42", "[edit]", "15 turn", "Edit(main.go)"}},
		{"1", 1, "Bash(ls)", "implementor", "explore", []string{"#1", "[explore]", "1 turn", "Bash(ls)"}},
		{"7", 0, "", "implementor", "explore", []string{"#7", "[explore]"}},
		{"3", 3, "", "implementor", "test", []string{"#3", "[test]", "3 turn"}},
		{"9", 3, "", "scout", "plan", []string{"#9", "scout", "[plan]", "3 turn"}},
	}
	for _, tc := range cases {
		got := claude.FormatHeartbeat(tc.issue, tc.turns, tc.lastTool, tc.role, tc.phase)
		for _, sub := range tc.wantSubs {
			if !strings.Contains(got, sub) {
				t.Errorf("FormatHeartbeat(%q,%d,%q,%q,%q) = %q, missing %q",
					tc.issue, tc.turns, tc.lastTool, tc.role, tc.phase, got, sub)
			}
		}
	}
}

// TestFormatHeartbeatSanitizesRole verifies that control characters,
// newlines, and CSI/OSC escape sequences embedded in role cannot break the
// single-line heartbeat row.
func TestFormatHeartbeatSanitizesRole(t *testing.T) {
	got := claude.FormatHeartbeat("42", 3, "Edit", "scout\x1b[2J\nfake-row", "edit")
	if strings.ContainsAny(got, "\n\x1b") {
		t.Errorf("FormatHeartbeat role not sanitized, got %q", got)
	}
	if !strings.Contains(got, "scout") {
		t.Errorf("FormatHeartbeat dropped legitimate role text, got %q", got)
	}
}

// TestWriterHeartbeatIncludesPhase verifies that the count line carries the
// phase tag derived from the tools used.
func TestWriterHeartbeatIncludesPhase(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "42", &status)

	toolEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}` + "\n"
	resultEv := `{"type":"result","num_turns":1}` + "\n"
	fmt.Fprint(w, toolEv)
	fmt.Fprint(w, resultEv)

	out := status.String()
	if !strings.Contains(out, "[edit]") {
		t.Errorf("heartbeat missing [edit] phase tag: %q", out)
	}
}

// TestWriterPhaseTransitionEmitsLine verifies that a phase transition emits the
// accumulated count for the prior phase, and the new phase's tools are counted
// separately.
func TestWriterPhaseTransitionEmitsLine(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "11", &status)

	ev1 := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	ev2 := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"b.go"}}]}}` + "\n"
	resultEv := `{"type":"result","num_turns":2}` + "\n"

	fmt.Fprint(w, ev1)
	fmt.Fprint(w, ev2)
	fmt.Fprint(w, resultEv)

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
	w := claude.New(&bytes.Buffer{}, "8", &status)

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

// TestFormatCountLineShape verifies the output shape from FormatCountLine.
func TestFormatCountLineShape(t *testing.T) {
	cases := []struct {
		issue    string
		role     string
		phase    string
		counts   map[string]int
		wantSubs []string
	}{
		{"228", "implementor", "explore", map[string]int{"read": 9, "grep": 5, "subagent": 1}, []string{"#228", "[explore]", "9 reads", "5 greps", "1 subagent"}},
		{"42", "implementor", "edit", map[string]int{"edit": 3}, []string{"#42", "[edit]", "3 edits"}},
		{"1", "implementor", "", map[string]int{"read": 1}, []string{"#1", "1 read"}},
		{"5", "implementor", "explore", map[string]int{"grep": 2, "read": 1}, []string{"#5", "1 read", "2 greps"}},
		{"9", "scout", "explore", map[string]int{"read": 1}, []string{"#9", "scout", "[explore]", "1 read"}},
	}
	for _, tc := range cases {
		got := claude.FormatCountLine(tc.issue, tc.role, tc.phase, tc.counts)
		for _, sub := range tc.wantSubs {
			if !strings.Contains(got, sub) {
				t.Errorf("FormatCountLine(%q,%q,%q,%v) = %q, missing %q",
					tc.issue, tc.role, tc.phase, tc.counts, got, sub)
			}
		}
	}
}

// TestFormatCountLineSanitizesRole verifies that control characters,
// newlines, and CSI/OSC escape sequences embedded in role cannot break the
// single-line count-line row.
func TestFormatCountLineSanitizesRole(t *testing.T) {
	got := claude.FormatCountLine("42", "scout\x1b]0;pwn\x07\nfake-row", "explore", map[string]int{"read": 1})
	if strings.ContainsAny(got, "\n\x1b") {
		t.Errorf("FormatCountLine role not sanitized, got %q", got)
	}
	if !strings.Contains(got, "scout") {
		t.Errorf("FormatCountLine dropped legitimate role text, got %q", got)
	}
}

// TestWriterCountsResetOnNarration verifies that counts reset after each
// narration so the next window starts fresh.
func TestWriterCountsResetOnNarration(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "99", &status)

	readEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	narEv := `{"type":"assistant","message":{"content":[{"type":"text","text":"First window."}]}}` + "\n"
	editEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"b.go"}}]}}` + "\n"
	nar2Ev := `{"type":"assistant","message":{"content":[{"type":"text","text":"Second window."}]}}` + "\n"

	// First window: 2 reads.
	fmt.Fprint(w, readEv)
	fmt.Fprint(w, readEv)
	fmt.Fprint(w, narEv)
	// Second window: 1 edit — counts must NOT carry the reads.
	fmt.Fprint(w, editEv)
	fmt.Fprint(w, nar2Ev)

	out := status.String()
	if !strings.Contains(out, "2 read") {
		t.Errorf("first window missing '2 reads': %q", out)
	}
	if !strings.Contains(out, "1 edit") {
		t.Errorf("second window missing '1 edit': %q", out)
	}
	// The second window must not mention reads.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for _, line := range lines {
		if strings.Contains(line, "Second window") {
			// This is the narration line; the count line follows.
			continue
		}
		if strings.Contains(line, "1 edit") && strings.Contains(line, "read") {
			t.Errorf("second window count line must not include reads: %q", line)
		}
	}
}

// TestWriterCountsDistinctKinds verifies that different tool kinds are counted
// separately in the count line emitted on narration.
func TestWriterCountsDistinctKinds(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "42", &status)

	readEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	grepEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"query":"foo"}}]}}` + "\n"
	narEv := `{"type":"assistant","message":{"content":[{"type":"text","text":"Checked."}]}}` + "\n"

	fmt.Fprint(w, readEv)
	fmt.Fprint(w, readEv)
	fmt.Fprint(w, grepEv)
	fmt.Fprint(w, narEv)

	out := status.String()
	if !strings.Contains(out, "2 read") {
		t.Errorf("count line missing '2 reads': %q", out)
	}
	if !strings.Contains(out, "1 grep") {
		t.Errorf("count line missing '1 grep': %q", out)
	}
}

// TestWriterSwitchHeader covers all switch-header acceptance criteria:
// implementor-only, role switch sequence, re-invocation, unknown parent, and
// header-spam suppression.
func TestWriterSwitchHeader(t *testing.T) {
	const (
		rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	)
	// Helpers to build JSON stream events.
	implNar := func(text string) string {
		return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
	}
	implTool := func(name, id string) string {
		return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"` + name + `","id":"` + id + `","input":{}}]}}` + "\n"
	}
	implTask := func(id, subagentType string) string {
		return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Task","id":"` + id + `","input":{"subagent_type":"` + subagentType + `"}}]}}` + "\n"
	}
	subRead := func(parentID string) string {
		return `{"type":"assistant","parent_tool_use_id":"` + parentID + `","message":{"content":[{"type":"tool_use","name":"Read","id":"r1","input":{}}]}}` + "\n"
	}
	subNar := func(parentID, text string) string {
		return `{"type":"assistant","parent_tool_use_id":"` + parentID + `","message":{"content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
	}
	_ = subNar

	t.Run("implementor_only_single_header", func(t *testing.T) {
		var status bytes.Buffer
		w := claude.New(&bytes.Buffer{}, "284", &status)
		fmt.Fprint(w, implNar("Now I have a clear understanding."))

		out := status.String()
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != 2 {
			t.Fatalf("want 2 lines (header+narration), got %d: %q", len(lines), out)
		}
		if !strings.Contains(lines[0], "#284") || !strings.Contains(lines[0], rule) || !strings.Contains(lines[0], "implementor") {
			t.Errorf("line 0 must be implementor header, got: %q", lines[0])
		}
		if !strings.Contains(lines[1], "Now I have a clear understanding") {
			t.Errorf("line 1 must be narration, got: %q", lines[1])
		}
	})

	t.Run("implementor_scout_implementor_sequence", func(t *testing.T) {
		var status bytes.Buffer
		w := claude.New(&bytes.Buffer{}, "284", &status)

		// Implementor does a read then launches scout.
		fmt.Fprint(w, implTool("Read", "r0"))
		fmt.Fprint(w, implTask("tu_s1", "scout"))
		// Scout does a read (counts should be separate from implementor's).
		fmt.Fprint(w, subRead("tu_s1"))
		// Implementor resumes with narration.
		fmt.Fprint(w, implNar("Back to work."))

		out := status.String()
		// Must contain scout header and implementor header(s).
		if !strings.Contains(out, "scout") {
			t.Errorf("missing scout role header: %q", out)
		}
		// Scout header must precede implementor's second header.
		scoutIdx := strings.Index(out, "scout")
		implIdx := strings.LastIndex(out, "implementor")
		if scoutIdx < 0 || implIdx < 0 {
			t.Fatalf("headers missing: %q", out)
		}
		if scoutIdx > implIdx {
			t.Errorf("scout header must appear before final implementor header: %q", out)
		}
	})

	t.Run("same_role_reinvoked_no_duplicate_header", func(t *testing.T) {
		var status bytes.Buffer
		w := claude.New(&bytes.Buffer{}, "1", &status)

		// Launch scout twice; between them implementor emits a narration so both
		// scout stints produce counts (the second scout header must appear).
		fmt.Fprint(w, implTask("tu_a", "scout"))
		fmt.Fprint(w, subRead("tu_a"))
		// Implementor narration causes scout counts to flush and implementor header.
		fmt.Fprint(w, implNar("Checking."))
		// Second scout invocation.
		fmt.Fprint(w, implTask("tu_b", "scout"))
		fmt.Fprint(w, subRead("tu_b"))
		fmt.Fprint(w, implNar("Done."))

		out := status.String()
		// "scout" must appear twice (two scout stints that both produce counts).
		if count := strings.Count(out, rule+" scout "); count < 2 {
			t.Errorf("expected ≥2 scout headers, got %d: %q", count, out)
		}
	})

	t.Run("unknown_parent_fallback_subagent", func(t *testing.T) {
		var status bytes.Buffer
		w := claude.New(&bytes.Buffer{}, "5", &status)

		// A message with a parent_tool_use_id that was never registered.
		unknown := `{"type":"assistant","parent_tool_use_id":"unknown_id","message":{"content":[{"type":"tool_use","name":"Read","id":"rx","input":{}}]}}` + "\n"
		fmt.Fprint(w, unknown)
		// Implementor narration triggers flush.
		fmt.Fprint(w, implNar("Continuing."))

		out := status.String()
		if !strings.Contains(out, "subagent") {
			t.Errorf("unknown parent must produce 'subagent' role header: %q", out)
		}
	})

	t.Run("suppressed_empty_headers", func(t *testing.T) {
		var status bytes.Buffer
		w := claude.New(&bytes.Buffer{}, "9", &status)

		// Scout produces zero body output; implementor follows immediately.
		// Must NOT get a scout header followed by implementor header with nothing between.
		fmt.Fprint(w, implTask("tu_s", "scout"))
		// Scout sends only narration (dropped) — no tool calls, no counts.
		fmt.Fprint(w, subNar("tu_s", "internal scout thought"))
		// Implementor resumes.
		fmt.Fprint(w, implNar("I reviewed the scout output."))

		out := status.String()
		// Scout produced no counts so scout header must not appear.
		if strings.Contains(out, rule+" scout ") {
			t.Errorf("empty scout stint must not emit scout header: %q", out)
		}
		// Implementor header must appear exactly once (before the narration).
		if n := strings.Count(out, rule+" implementor "); n != 1 {
			t.Errorf("implementor header must appear exactly once, got %d: %q", n, out)
		}
	})
}

// TestWriterResultLineNamesActingRole verifies that when a result event
// fires while a subagent is still the acting role (the log ends mid-scout,
// no narration or tool call ever hands control back to the implementor),
// the trailing turns line names the scout — not the implementor's rolePhase,
// which was never set (#732).
func TestWriterResultLineNamesActingRole(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "9", &status)

	implTask := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Task","id":"tu_s1","input":{"subagent_type":"scout"}}]}}` + "\n"
	subRead := `{"type":"assistant","parent_tool_use_id":"tu_s1","message":{"content":[{"type":"tool_use","name":"Read","id":"r1","input":{}}]}}` + "\n"
	resultEv := `{"type":"result","num_turns":3}` + "\n"
	fmt.Fprint(w, implTask)
	fmt.Fprint(w, subRead)
	fmt.Fprint(w, resultEv)

	out := status.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "scout") {
		t.Errorf("trailing turns line must name the acting role \"scout\", got: %q", last)
	}
	if !strings.Contains(last, "3 turn") {
		t.Errorf("trailing turns line missing turn count: %q", last)
	}
}

// TestModelFamily verifies model ID shortening to family labels.
func TestModelFamily(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"claude-haiku-4-5-20251001", "haiku"},
		{"claude-sonnet-4-6", "sonnet"},
		{"claude-opus-4-8", "opus"},
		{"claude-opus-4-8-20250514", "opus"},
		{"claude-fable-5", "claude-fable-5"},
		{"gpt-4o", "gpt-4o"},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			got := claude.ModelFamily(tc.id)
			if got != tc.want {
				t.Errorf("ModelFamily(%q) = %q, want %q", tc.id, got, tc.want)
			}
		})
	}
}

// TestWriterModelHeader covers model extraction, header format, missing-model tolerance,
// and same-role model switch producing a new header.
func TestWriterModelHeader(t *testing.T) {
	const rule = "\xe2\x94\x80\xe2\x94\x80"

	// Helper: implementor assistant event with optional model field.
	implNarWithModel := func(text, model string) string {
		modelJSON := ""
		if model != "" {
			modelJSON = `,"model":"` + model + `"`
		}
		return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]` + modelJSON + `}}` + "\n"
	}
	implToolWithModel := func(name, id, model string) string {
		modelJSON := ""
		if model != "" {
			modelJSON = `,"model":"` + model + `"`
		}
		return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"` + name + `","id":"` + id + `","input":{}}]` + modelJSON + `}}` + "\n"
	}

	t.Run("model_in_header", func(t *testing.T) {
		var status bytes.Buffer
		w := claude.New(&bytes.Buffer{}, "1", &status)
		fmt.Fprint(w, implNarWithModel("Planning.", "claude-opus-4-8"))
		out := status.String()
		if !strings.Contains(out, rule+" implementor \xc2\xb7 opus ") {
			t.Errorf("header must contain 'implementor · opus': %q", out)
		}
	})

	t.Run("missing_model_role_only", func(t *testing.T) {
		var status bytes.Buffer
		w := claude.New(&bytes.Buffer{}, "2", &status)
		fmt.Fprint(w, implNarWithModel("Planning.", ""))
		out := status.String()
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) == 0 {
			t.Fatal("no output")
		}
		header := lines[0]
		if strings.Contains(header, "\xc2\xb7") {
			t.Errorf("header with no model must not contain '·': %q", header)
		}
		if !strings.Contains(header, rule+" implementor ") {
			t.Errorf("header must contain 'implementor': %q", header)
		}
	})

	t.Run("same_role_model_switch_new_header", func(t *testing.T) {
		var status bytes.Buffer
		w := claude.New(&bytes.Buffer{}, "3", &status)
		// Implementor uses sonnet, accumulates a read, then switches to opus.
		fmt.Fprint(w, implToolWithModel("Read", "r1", "claude-sonnet-4-6"))
		fmt.Fprint(w, implNarWithModel("Now switching.", "claude-opus-4-8"))
		out := status.String()
		// Both model headers must appear.
		if !strings.Contains(out, "sonnet") {
			t.Errorf("must contain 'sonnet' header: %q", out)
		}
		if !strings.Contains(out, "opus") {
			t.Errorf("must contain 'opus' header: %q", out)
		}
		// sonnet header must precede opus header.
		si := strings.Index(out, "sonnet")
		oi := strings.Index(out, "opus")
		if si < 0 || oi < 0 || si > oi {
			t.Errorf("sonnet header must precede opus header: %q", out)
		}
	})

	t.Run("no_header_spam_same_role_model", func(t *testing.T) {
		var status bytes.Buffer
		w := claude.New(&bytes.Buffer{}, "4", &status)
		// Two consecutive narrations with the same (role, model) — only one header.
		fmt.Fprint(w, implNarWithModel("First.", "claude-sonnet-4-6"))
		fmt.Fprint(w, implNarWithModel("Second.", "claude-sonnet-4-6"))
		out := status.String()
		if n := strings.Count(out, rule+" implementor \xc2\xb7 sonnet "); n != 1 {
			t.Errorf("identical (role,model) must emit header once, got %d: %q", n, out)
		}
	})
}

// TestFormatRoleHeaderModel verifies that FormatRoleHeader includes model when provided.
func TestFormatRoleHeaderModel(t *testing.T) {
	const rule = "\xe2\x94\x80\xe2\x94\x80"
	h := claude.FormatRoleHeader("42", "scout", "haiku")
	if !strings.Contains(h, rule+" scout \xc2\xb7 haiku ") {
		t.Errorf("header missing 'scout · haiku': %q", h)
	}
	hNoModel := claude.FormatRoleHeader("42", "scout", "")
	if strings.Contains(hNoModel, "\xc2\xb7") {
		t.Errorf("header with empty model must not contain '·': %q", hNoModel)
	}
	if !strings.Contains(hNoModel, rule+" scout ") {
		t.Errorf("header missing 'scout': %q", hNoModel)
	}
}

// TestFormatRoleHeaderSanitizesRole verifies that control characters,
// newlines, and CSI/OSC escape sequences embedded in role cannot break the
// single-line header row, and that the trailing rule still pads out based
// on the sanitized (not raw) role length.
func TestFormatRoleHeaderSanitizesRole(t *testing.T) {
	got := claude.FormatRoleHeader("42", "scout\x1b[2J\nfake-row", "")
	if strings.ContainsAny(got, "\n\x1b") {
		t.Errorf("FormatRoleHeader role not sanitized, got %q", got)
	}
	if !strings.Contains(got, "scout") {
		t.Errorf("FormatRoleHeader dropped legitimate role text, got %q", got)
	}
	if !strings.Contains(got, "\xe2\x94\x80") {
		t.Errorf("FormatRoleHeader missing trailing rule, got %q", got)
	}
}

// TestWriterCountLineOnNarration verifies that accumulated tool events produce
// a count summary line when narration arrives, not one line per tool event.
func TestWriterCountLineOnNarration(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "228", &status)

	readEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	narEv := `{"type":"assistant","message":{"content":[{"type":"text","text":"Exploring."}]}}` + "\n"

	for i := 0; i < 3; i++ {
		fmt.Fprint(w, readEv)
	}
	fmt.Fprint(w, narEv)

	out := status.String()
	if !strings.Contains(out, "3 read") {
		t.Errorf("count line missing '3 read': %q", out)
	}
	if !strings.Contains(out, "Exploring") {
		t.Errorf("narration missing: %q", out)
	}
}
