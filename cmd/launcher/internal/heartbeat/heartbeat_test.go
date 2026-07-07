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
	w := heartbeat.New(&bytes.Buffer{}, "5", &status, time.Hour)

	readEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	narEv := `{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}` + "\n"

	// Write the same event 5 times then trigger with narration.
	for i := 0; i < 5; i++ {
		fmt.Fprint(w, readEv)
	}
	fmt.Fprint(w, narEv)

	out := status.String()
	// Narration + count line = 2 lines total, not 5 per-tool lines.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines (narration + count), got %d: %q", len(lines), out)
	}
	if !strings.Contains(out, "5 read") {
		t.Errorf("count line missing '5 reads': %q", out)
	}
}

// TestWriterEmitsOnNewTool verifies that switching tool kinds emits count lines
// for each phase and kind.
func TestWriterEmitsOnNewTool(t *testing.T) {
	var status bytes.Buffer
	w := heartbeat.New(&bytes.Buffer{}, "11", &status, time.Hour)

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
	w := heartbeat.New(&bytes.Buffer{}, "42", &status, time.Hour)

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

// TestWriterNarrationBeforeTool verifies that narration text appears before the
// count line for accumulated tools in the output.
func TestWriterNarrationBeforeTool(t *testing.T) {
	var status bytes.Buffer
	w := heartbeat.New(&bytes.Buffer{}, "42", &status, time.Hour)

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

// TestWriterHeartbeatIncludesPhase verifies that the count line carries the
// phase tag derived from the tools used.
func TestWriterHeartbeatIncludesPhase(t *testing.T) {
	var status bytes.Buffer
	w := heartbeat.New(&bytes.Buffer{}, "42", &status, time.Hour)

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
	w := heartbeat.New(&bytes.Buffer{}, "11", &status, time.Hour)

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

// TestFormatCountLineShape verifies the output shape from FormatCountLine.
func TestFormatCountLineShape(t *testing.T) {
	cases := []struct {
		issue    string
		phase    string
		counts   map[string]int
		wantSubs []string
	}{
		{"228", "explore", map[string]int{"read": 9, "grep": 5, "subagent": 1}, []string{"#228", "[explore]", "9 reads", "5 greps", "1 subagent"}},
		{"42", "edit", map[string]int{"edit": 3}, []string{"#42", "[edit]", "3 edits"}},
		{"1", "", map[string]int{"read": 1}, []string{"#1", "1 read"}},
		{"5", "explore", map[string]int{"grep": 2, "read": 1}, []string{"#5", "1 read", "2 greps"}},
	}
	for _, tc := range cases {
		got := heartbeat.FormatCountLine(tc.issue, tc.phase, tc.counts)
		for _, sub := range tc.wantSubs {
			if !strings.Contains(got, sub) {
				t.Errorf("FormatCountLine(%q,%q,%v) = %q, missing %q",
					tc.issue, tc.phase, tc.counts, got, sub)
			}
		}
	}
}

// TestWriterCountsResetOnNarration verifies that counts reset after each
// narration so the next window starts fresh.
func TestWriterCountsResetOnNarration(t *testing.T) {
	var status bytes.Buffer
	w := heartbeat.New(&bytes.Buffer{}, "99", &status, time.Hour)

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
	w := heartbeat.New(&bytes.Buffer{}, "42", &status, time.Hour)

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

// TestWriterCountLineOnNarration verifies that accumulated tool events produce
// a count summary line when narration arrives, not one line per tool event.
func TestWriterCountLineOnNarration(t *testing.T) {
	var status bytes.Buffer
	w := heartbeat.New(&bytes.Buffer{}, "228", &status, time.Hour)

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
