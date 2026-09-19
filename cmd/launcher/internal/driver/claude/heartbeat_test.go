package claude_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/landdelta"
	"spindrift.dev/launcher/internal/usage"
)

func newWriter(issue string, status *bytes.Buffer) *claude.Writer {
	return claude.New(&bytes.Buffer{}, issue, status)
}

func newWriterRaw(raw *bytes.Buffer, issue string, status *bytes.Buffer) *claude.Writer {
	return claude.New(raw, issue, status)
}

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

// The two chunks split inside a single JSON object, not on a line boundary.
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

// New takes three arguments: the time-based throttle fallback was removed.
func TestNewNoThrottleArg(t *testing.T) {
	w := claude.New(&bytes.Buffer{}, "1", &bytes.Buffer{})
	if w == nil {
		t.Fatal("New returned nil")
	}
}

func TestWriterBareResultEmitsNothing(t *testing.T) {
	var status bytes.Buffer
	w := newWriter("42", &status)

	event := `{"type":"result"}` + "\n"
	fmt.Fprint(w, event)

	if status.Len() > 0 {
		t.Errorf("bare result must emit nothing, got: %q", status.String())
	}
}

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
	// A line ending in "]" carries only the issue tag and phase, so it is bare.
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.HasSuffix(line, "]") || line == "#42" {
			t.Errorf("bare heartbeat line emitted: %q", line)
		}
	}
}

func TestWriterTolerateMalformedJSON(t *testing.T) {
	var raw bytes.Buffer
	var status bytes.Buffer
	w := newWriterRaw(&raw, "3", &status)

	lines := "not json at all\n{broken: json}\n\x00\x01\x02\n"
	fmt.Fprint(w, lines)

	if raw.String() != lines {
		t.Errorf("raw passthrough broken: got %q, want %q", raw.String(), lines)
	}
}

func TestWriterThrottlesSameToolRepeat(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "5", &status)

	readEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	narEv := `{"type":"assistant","message":{"content":[{"type":"text","text":"Done."}]}}` + "\n"

	for i := 0; i < 5; i++ {
		fmt.Fprint(w, readEv)
	}
	fmt.Fprint(w, narEv)

	out := status.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines (header + narration + count), got %d: %q", len(lines), out)
	}
	if !strings.Contains(out, "5 read") {
		t.Errorf("count line missing '5 reads': %q", out)
	}
}

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

func TestWriterNarrationIncludesPhase(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "42", &status)

	// The phase tag comes from the most recent tool, so a tool event must come first.
	toolEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	fmt.Fprint(w, toolEv)
	status.Reset()

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
	// lines[1] is the narration row "#99 · <text>"; only the text portion is bounded.
	prefix := "#99 \xc2\xb7 "
	if !strings.HasPrefix(lines[1], prefix) {
		t.Errorf("narration line missing prefix %q: %q", prefix, lines[1])
	}
	textPart := strings.TrimPrefix(lines[1], prefix)
	if len(textPart) > 120 {
		t.Errorf("narration text %d chars, want ≤120", len(textPart))
	}
}

// An assistant text block carrying a parent_tool_use_id is subagent output.
func TestWriterSubagentNarrationDropped(t *testing.T) {
	var raw bytes.Buffer
	var status bytes.Buffer
	w := claude.New(&raw, "55", &status)

	event := `{"type":"assistant","parent_tool_use_id":"tu_abc","message":{"content":[{"type":"text","text":"subagent says hello"}]}}` + "\n"
	fmt.Fprint(w, event)

	if strings.Contains(status.String(), "subagent says hello") {
		t.Errorf("subagent narration must not appear in heartbeat: %q", status.String())
	}
	if raw.String() != event {
		t.Errorf("raw passthrough broken: got %q, want %q", raw.String(), event)
	}
}

func TestWriterNarrationBeforeTool(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "42", &status)

	// A narration starts the group and the next narration flushes its counts.
	narEv1 := `{"type":"assistant","message":{"content":[{"type":"text","text":"I will edit the file."}]}}` + "\n"
	toolEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}` + "\n"
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

// A control character, newline, or CSI/OSC escape in role must not break the
// single-line heartbeat row.
func TestFormatHeartbeatSanitizesRole(t *testing.T) {
	got := claude.FormatHeartbeat("42", 3, "Edit", "scout\x1b[2J\nfake-row", "edit")
	want := "#42 scoutfake-row [edit] \xc2\xb7 3 turns \xc2\xb7 Edit"
	if got != want {
		t.Errorf("FormatHeartbeat role not sanitized, got %q, want %q", got, want)
	}
}

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

// A control character, newline, or CSI/OSC escape in role must not break the
// single-line count row.
func TestFormatCountLineSanitizesRole(t *testing.T) {
	got := claude.FormatCountLine("42", "scout\x1b]0;pwn\x07\nfake-row", "explore", map[string]int{"read": 1})
	want := "#42 scoutfake-row [explore] \xc2\xb7 1 read"
	if got != want {
		t.Errorf("FormatCountLine role not sanitized, got %q, want %q", got, want)
	}
}

func TestWriterCountsResetOnNarration(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "99", &status)

	readEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	narEv := `{"type":"assistant","message":{"content":[{"type":"text","text":"First window."}]}}` + "\n"
	editEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"b.go"}}]}}` + "\n"
	nar2Ev := `{"type":"assistant","message":{"content":[{"type":"text","text":"Second window."}]}}` + "\n"

	fmt.Fprint(w, readEv)
	fmt.Fprint(w, readEv)
	fmt.Fprint(w, narEv)
	// The second window must not carry the first window's reads.
	fmt.Fprint(w, editEv)
	fmt.Fprint(w, nar2Ev)

	out := status.String()
	if !strings.Contains(out, "2 read") {
		t.Errorf("first window missing '2 reads': %q", out)
	}
	if !strings.Contains(out, "1 edit") {
		t.Errorf("second window missing '1 edit': %q", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for _, line := range lines {
		if strings.Contains(line, "Second window") {
			// This is the narration line, and its count line follows it.
			continue
		}
		if strings.Contains(line, "1 edit") && strings.Contains(line, "read") {
			t.Errorf("second window count line must not include reads: %q", line)
		}
	}
}

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

func TestWriterSwitchHeader(t *testing.T) {
	const (
		rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	)
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

		fmt.Fprint(w, implTool("Read", "r0"))
		fmt.Fprint(w, implTask("tu_s1", "scout"))
		fmt.Fprint(w, subRead("tu_s1"))
		fmt.Fprint(w, implNar("Back to work."))

		out := status.String()
		if !strings.Contains(out, "scout") {
			t.Errorf("missing scout role header: %q", out)
		}
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

		// The implementor narration between the two scout stints flushes the
		// first stint's counts, so both stints produce a header.
		fmt.Fprint(w, implTask("tu_a", "scout"))
		fmt.Fprint(w, subRead("tu_a"))
		fmt.Fprint(w, implNar("Checking."))
		fmt.Fprint(w, implTask("tu_b", "scout"))
		fmt.Fprint(w, subRead("tu_b"))
		fmt.Fprint(w, implNar("Done."))

		out := status.String()
		if count := strings.Count(out, rule+" scout "); count < 2 {
			t.Errorf("expected ≥2 scout headers, got %d: %q", count, out)
		}
	})

	t.Run("unknown_parent_fallback_subagent", func(t *testing.T) {
		var status bytes.Buffer
		w := claude.New(&bytes.Buffer{}, "5", &status)

		unknown := `{"type":"assistant","parent_tool_use_id":"unknown_id","message":{"content":[{"type":"tool_use","name":"Read","id":"rx","input":{}}]}}` + "\n"
		fmt.Fprint(w, unknown)
		// The implementor narration flushes the pending counts.
		fmt.Fprint(w, implNar("Continuing."))

		out := status.String()
		if !strings.Contains(out, "subagent") {
			t.Errorf("unknown parent must produce 'subagent' role header: %q", out)
		}
	})

	t.Run("suppressed_empty_headers", func(t *testing.T) {
		var status bytes.Buffer
		w := claude.New(&bytes.Buffer{}, "9", &status)

		// The scout sends only narration, which the Writer drops, so the stint
		// produces no counts and must get no header of its own.
		fmt.Fprint(w, implTask("tu_s", "scout"))
		fmt.Fprint(w, subNar("tu_s", "internal scout thought"))
		fmt.Fprint(w, implNar("I reviewed the scout output."))

		out := status.String()
		if strings.Contains(out, rule+" scout ") {
			t.Errorf("empty scout stint must not emit scout header: %q", out)
		}
		if n := strings.Count(out, rule+" implementor "); n != 1 {
			t.Errorf("implementor header must appear exactly once, got %d: %q", n, out)
		}
	})
}

// A spawn block using the real tool name "Agent", not the fallback "Task",
// must still resolve subagent_type to a named role header rather than the
// generic "subagent" label (#2078).
func TestWriterSwitchHeader_AgentToolName(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "284", &status)

	implTool := func(name, id string) string {
		return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"` + name + `","id":"` + id + `","input":{}}]}}` + "\n"
	}
	implAgent := func(id, subagentType string) string {
		return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Agent","id":"` + id + `","input":{"subagent_type":"` + subagentType + `"}}]}}` + "\n"
	}
	subRead := func(parentID string) string {
		return `{"type":"assistant","parent_tool_use_id":"` + parentID + `","message":{"content":[{"type":"tool_use","name":"Read","id":"r1","input":{}}]}}` + "\n"
	}
	implNar := func(text string) string {
		return `{"type":"assistant","message":{"content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
	}

	fmt.Fprint(w, implTool("Read", "r0"))
	fmt.Fprint(w, implAgent("tu_r1", "reviewer"))
	fmt.Fprint(w, subRead("tu_r1"))
	fmt.Fprint(w, implNar("Back to work."))

	out := status.String()
	const rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	if !strings.Contains(out, rule+" reviewer ") {
		t.Errorf("missing reviewer role header for Agent-named spawn block: %q", out)
	}
	if strings.Contains(out, rule+" subagent ") {
		t.Errorf("Agent-named spawn block with known subagent_type must not fall back to the bare \"subagent\" role header: %q", out)
	}
}

// A subagent B spawned by another subagent A, two levels below the
// implementor, must be labeled with B's own subagent_type in the switch
// header, not the generic "subagent" fallback (issue #2079).
func TestWriterSwitchHeader_NestedSubagent(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "2079", &status)

	implAgent := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Agent","id":"toolu_A","input":{"subagent_type":"researcher"}}]}}` + "\n"
	// This one event carries both parent_tool_use_id "toolu_A", making A the
	// actor, and B's own spawn block.
	aSpawnsB := `{"type":"assistant","parent_tool_use_id":"toolu_A","message":{"content":[{"type":"tool_use","name":"Agent","id":"toolu_B","input":{"subagent_type":"worker"}}]}}` + "\n"
	bRead := `{"type":"assistant","parent_tool_use_id":"toolu_B","message":{"content":[{"type":"tool_use","name":"Read","id":"r1","input":{}}]}}` + "\n"
	// The implementor's narration flushes B's pending counts.
	implNar := `{"type":"assistant","message":{"content":[{"type":"text","text":"Back to work."}]}}` + "\n"

	fmt.Fprint(w, implAgent)
	fmt.Fprint(w, aSpawnsB)
	fmt.Fprint(w, bRead)
	fmt.Fprint(w, implNar)

	out := status.String()
	const rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	if !strings.Contains(out, rule+" worker ") {
		t.Errorf("missing worker role header for nested subagent B: %q", out)
	}
	if strings.Contains(out, rule+" subagent ") {
		t.Errorf("nested subagent B must not fall back to the generic \"subagent\" role header: %q", out)
	}
}

// When a result event fires while a subagent is still the acting role (the
// log ends mid-scout, nothing hands control back to the implementor), the
// trailing turns line must name the scout, not the implementor's rolePhase,
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

func TestWriterModelHeader(t *testing.T) {
	const rule = "\xe2\x94\x80\xe2\x94\x80"

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
		fmt.Fprint(w, implToolWithModel("Read", "r1", "claude-sonnet-4-6"))
		fmt.Fprint(w, implNarWithModel("Now switching.", "claude-opus-4-8"))
		out := status.String()
		if !strings.Contains(out, "sonnet") {
			t.Errorf("must contain 'sonnet' header: %q", out)
		}
		if !strings.Contains(out, "opus") {
			t.Errorf("must contain 'opus' header: %q", out)
		}
		si := strings.Index(out, "sonnet")
		oi := strings.Index(out, "opus")
		if si < 0 || oi < 0 || si > oi {
			t.Errorf("sonnet header must precede opus header: %q", out)
		}
	})

	t.Run("no_header_spam_same_role_model", func(t *testing.T) {
		var status bytes.Buffer
		w := claude.New(&bytes.Buffer{}, "4", &status)
		fmt.Fprint(w, implNarWithModel("First.", "claude-sonnet-4-6"))
		fmt.Fprint(w, implNarWithModel("Second.", "claude-sonnet-4-6"))
		out := status.String()
		if n := strings.Count(out, rule+" implementor \xc2\xb7 sonnet "); n != 1 {
			t.Errorf("identical (role,model) must emit header once, got %d: %q", n, out)
		}
	})
}

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

// A control character, newline, or CSI/OSC escape in role must not break the
// single-line header row. The trailing rule pads out from the sanitized role
// length, not the raw one, which is why the want string has 15 rule runes.
func TestFormatRoleHeaderSanitizesRole(t *testing.T) {
	got := claude.FormatRoleHeader("42", "scout\x1b[2J\nfake-row", "")
	want := "#42 \xe2\x94\x80\xe2\x94\x80 scoutfake-row " + strings.Repeat("\xe2\x94\x80", 15)
	if got != want {
		t.Errorf("FormatRoleHeader role not sanitized, got %q, want %q", got, want)
	}
}

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

// The Writer turns a "spindrift_op" stream-json event carrying a verdict op
// into a status row, interleaved with ordinary narration (issue #2027).
func TestWriterEmitsSpindriftOpVerdict(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "7", &status)

	opEv := `{"type":"spindrift_op","spindrift_op":{"op":"verdict","verdict":"BLOCK"}}` + "\n"
	fmt.Fprint(w, opEv)

	out := status.String()
	if !strings.Contains(out, "#7") {
		t.Errorf("status missing issue tag: %q", out)
	}
	if !strings.Contains(out, "verdict: BLOCK") {
		t.Errorf("status missing verdict text: %q", out)
	}
}

// A decision op renders its decision and reason together, and drops the
// trailing separator when the reason is empty (issue #2027).
func TestFormatSpindriftOpDecision(t *testing.T) {
	got := claude.FormatSpindriftOp("7", claude.SpindriftOp{Op: "decision", Decision: "stop", Reason: "max review rounds reached"})
	if !strings.Contains(got, "stop: max review rounds reached") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "stop: max review rounds reached")
	}

	gotNoReason := claude.FormatSpindriftOp("7", claude.SpindriftOp{Op: "decision", Decision: "continue"})
	if !strings.Contains(gotNoReason, "continue") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", gotNoReason, "continue")
	}
	if strings.Contains(gotNoReason, ":") {
		t.Errorf("FormatSpindriftOp = %q, want no trailing separator when reason is empty", gotNoReason)
	}
}

// A delta_review_trigger op renders its own Decision and Reason on both the
// fire and skip cases, rather than falling through to the default arm's bare
// op-name rendering (issue #3246).
func TestFormatSpindriftOpDeltaReviewTrigger(t *testing.T) {
	got := claude.FormatSpindriftOp("7", claude.SpindriftOp{Op: "delta_review_trigger", Decision: "fire", Reason: "land delta touches lines beyond the reviewer's findings: run.go:42"})
	if !strings.Contains(got, "fire: land delta touches lines beyond the reviewer's findings: run.go:42") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the fire decision and reason", got)
	}

	gotSkip := claude.FormatSpindriftOp("7", claude.SpindriftOp{Op: "delta_review_trigger", Decision: "skip", Reason: "max slices reached"})
	if !strings.Contains(gotSkip, "skip: max slices reached") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the skip decision and reason", gotSkip)
	}
}

// EncodeSpindriftOp produces one newline-terminated stream-json line that the
// Writer parses back into a status row. That is the seam the orchestrator uses
// to emit its own operations onto the stdout stream driver-exec's raw output
// already flows through (issue #2027).
func TestEncodeSpindriftOpFeedsWriter(t *testing.T) {
	line := claude.EncodeSpindriftOp(claude.SpindriftOp{Op: "pass_start", Pass: 3})
	if !strings.HasSuffix(line, "\n") {
		t.Fatalf("EncodeSpindriftOp = %q, want a trailing newline", line)
	}
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("EncodeSpindriftOp = %q, want exactly one newline", line)
	}

	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "3", &status)
	fmt.Fprint(w, line)

	if !strings.Contains(status.String(), "pass 3 started") {
		t.Errorf("status = %q, want it to contain %q", status.String(), "pass 3 started")
	}
}

// Adding the "spindrift_op" case must not disturb the parser's fallback: it
// still silently drops an unrecognized JSON event type and a bare non-JSON
// line (issue #2027 AC).
func TestWriterIgnoresUnrecognizedEventTypesAndNonJSONLines(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "9", &status)

	fmt.Fprint(w, `{"type":"system","session_id":"s1"}`+"\n")
	fmt.Fprint(w, "not json at all\n")

	if status.String() != "" {
		t.Errorf("status = %q, want empty (unrecognized type and non-JSON line both silently dropped)", status.String())
	}
}

// A run_state_error op renders its phase and error text (issue #2027).
func TestFormatSpindriftOpRunStateError(t *testing.T) {
	got := claude.FormatSpindriftOp("7", claude.SpindriftOp{Op: "run_state_error", Phase: "write", Error: "permission denied"})
	if !strings.Contains(got, "run-state write failed: permission denied") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "run-state write failed: permission denied")
	}
}

// The "dispositions_budget" phase gets its own wording, not "run-state
// dispositions_budget failed": the tripwire is a loud but non-fatal budget
// notice, not a run-state read, write, or append failure (issue #2550 AC9).
func TestFormatSpindriftOpDispositionsBudget(t *testing.T) {
	got := claude.FormatSpindriftOp("7", claude.SpindriftOp{Op: "run_state_error", Phase: "dispositions_budget", Error: "round 1 mean 283.0/entry (ceiling 40), total 283 tokens (ceiling 400)"})
	if !strings.Contains(got, "dispositions budget: round 1 mean 283.0/entry (ceiling 40), total 283 tokens (ceiling 400)") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "dispositions budget: round 1 mean 283.0/entry (ceiling 40), total 283 tokens (ceiling 400)")
	}
	if strings.Contains(got, "run-state dispositions_budget failed") {
		t.Errorf("FormatSpindriftOp = %q, must not render the budget tripwire as a run-state failure", got)
	}
}

// The implementor-side counterpart of TestFormatSpindriftOpDispositionsBudget:
// the "decisions_budget" phase gets its own wording, not "run-state
// decisions_budget failed" (issue #2695).
func TestFormatSpindriftOpDecisionsBudget(t *testing.T) {
	got := claude.FormatSpindriftOp("7", claude.SpindriftOp{Op: "run_state_error", Phase: "decisions_budget", Error: "round 1 mean 283.0/entry (ceiling 50), total 283 tokens (ceiling 400)"})
	if !strings.Contains(got, "decisions budget: round 1 mean 283.0/entry (ceiling 50), total 283 tokens (ceiling 400)") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "decisions budget: round 1 mean 283.0/entry (ceiling 50), total 283 tokens (ceiling 400)")
	}
	if strings.Contains(got, "run-state decisions_budget failed") {
		t.Errorf("FormatSpindriftOp = %q, must not render the budget tripwire as a run-state failure", got)
	}
}

// A pass_no_outcome op names the last verdict seen inline, so an operator can
// tell a mid-turn cutoff after a BLOCK apart from one with no verdict at all
// (issue #2036).
func TestFormatSpindriftOpPassNoOutcome(t *testing.T) {
	got := claude.FormatSpindriftOp("42", claude.SpindriftOp{Op: "pass_no_outcome", Pass: 3, Verdict: "BLOCK", Reason: "exit 0"})
	if !strings.Contains(got, "pass 3 ended with no outcome") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "pass 3 ended with no outcome")
	}
	if !strings.Contains(got, "BLOCK") {
		t.Errorf("FormatSpindriftOp = %q, want the last verdict named inline", got)
	}

	gotNoVerdict := claude.FormatSpindriftOp("42", claude.SpindriftOp{Op: "pass_no_outcome", Pass: 1, Reason: "exit 137"})
	if !strings.Contains(gotNoVerdict, "pass 1 ended with no outcome") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", gotNoVerdict, "pass 1 ended with no outcome")
	}
	if strings.Contains(gotNoVerdict, "last verdict") {
		t.Errorf("FormatSpindriftOp = %q, want no misleading 'last verdict' text when none was ever seen", gotNoVerdict)
	}
}

// A control character, newline, or CSI/OSC escape in a decision's reason or a
// run_state_error's error text must not break the single-line row (issue #2027
// AC: "Operation rows are sanitized to a single line").
func TestFormatSpindriftOpSanitizesDynamicFields(t *testing.T) {
	got := claude.FormatSpindriftOp("42", claude.SpindriftOp{Op: "run_state_error", Phase: "read", Error: "bad\x1b[2J\nfake-row"})
	if strings.Contains(got, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded newline", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded escape sequence", got)
	}

	gotNoOutcome := claude.FormatSpindriftOp("42", claude.SpindriftOp{Op: "pass_no_outcome", Pass: 1, Verdict: "bad\x1b[2J\nfake-row", Reason: "bad\x1b[2J\nfake-row"})
	if strings.Contains(gotNoOutcome, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded newline", gotNoOutcome)
	}
	if strings.Contains(gotNoOutcome, "\x1b") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded escape sequence", gotNoOutcome)
	}
}

// A pass_start op renders as one status row carrying the issue tag and pass
// number (issue #2027).
func TestFormatSpindriftOpPassStart(t *testing.T) {
	got := claude.FormatSpindriftOp("42", claude.SpindriftOp{Op: "pass_start", Pass: 2})
	if !strings.HasPrefix(got, "#42 ") {
		t.Errorf("FormatSpindriftOp = %q, want it to start with issue tag %q", got, "#42 ")
	}
	if !strings.Contains(got, "pass 2 started") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "pass 2 started")
	}
}

// A Writer built via NewWithTopLevelRole attributes a top-level assistant
// event, one with no parent_tool_use_id, to the given topLevelRole: both the
// switch header and the buffered count line bucket under it (issue #2092).
func TestWriterTopLevelRoleAppliesToTopLevelMessage(t *testing.T) {
	const rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	var status bytes.Buffer
	w := claude.NewWithTopLevelRole(&bytes.Buffer{}, "2092", &status, "reviewer")

	toolEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	narEv := `{"type":"assistant","message":{"content":[{"type":"text","text":"Reviewing the change."}]}}` + "\n"
	fmt.Fprint(w, toolEv)
	fmt.Fprint(w, narEv)

	out := status.String()
	if !strings.Contains(out, rule+" reviewer ") {
		t.Errorf("missing reviewer switch header: %q", out)
	}
	if !strings.Contains(out, "1 read") {
		t.Errorf("count line missing '1 read' bucketed under reviewer: %q", out)
	}
	if strings.Contains(out, rule+" implementor ") {
		t.Errorf("top-level message with topLevelRole set must not emit an implementor header: %q", out)
	}
}

// A pass_start op names the pass's role (issue #2037) inline when Role is set,
// so #2027's telemetry can tell a code-owned review pass apart from an
// implement or fix pass. Unlike a legacy single-pass run, either may
// legitimately end with no SPINDRIFT_OUTCOME of its own.
func TestFormatSpindriftOpPassStartWithRole(t *testing.T) {
	got := claude.FormatSpindriftOp("42", claude.SpindriftOp{Op: "pass_start", Pass: 2, Role: "review"})
	if !strings.Contains(got, "pass 2 (review) started") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "pass 2 (review) started")
	}
}

// A Writer built via plain New, with no static topLevelRole, switches its
// active top-level role mid-stream on a pass_start op whose Role is non-empty:
// a review pass attributes later top-level turns to reviewer, not the
// ImplementorRole default, across header, count, and heartbeat lines alike
// (issue #2382).
func TestWriterPassStartSwitchesActiveTopLevelRole(t *testing.T) {
	const rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "2382", &status)

	passStart := `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":2,"role":"review"}}` + "\n"
	toolEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	narEv := `{"type":"assistant","message":{"content":[{"type":"text","text":"Reviewing the change."}]}}` + "\n"
	resultEv := `{"type":"result","num_turns":3}` + "\n"
	fmt.Fprint(w, passStart)
	fmt.Fprint(w, toolEv)
	fmt.Fprint(w, narEv)
	fmt.Fprint(w, resultEv)

	out := status.String()
	if !strings.Contains(out, rule+" reviewer ") {
		t.Errorf("missing reviewer switch header after review pass_start: %q", out)
	}
	if strings.Contains(out, rule+" implementor ") {
		t.Errorf("must not emit an implementor header after review pass_start: %q", out)
	}
	if !strings.Contains(out, "1 read") {
		t.Errorf("count line missing '1 read' bucketed under reviewer: %q", out)
	}
	if !strings.Contains(out, "#2382 reviewer") || !strings.Contains(out, "3 turns") {
		t.Errorf("missing reviewer-attributed heartbeat line with turn count: %q", out)
	}
}

// A "fix" pass_start after a "review" pass_start switches the active role back
// to implementor: the implement, review, fix sequence a code-owned review's
// BLOCK verdict drives (issue #2382).
func TestWriterPassStartSwitchesBackToImplementorOnFix(t *testing.T) {
	const rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "2382", &status)

	reviewStart := `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":2,"role":"review"}}` + "\n"
	reviewNar := `{"type":"assistant","message":{"content":[{"type":"text","text":"Reviewing the change."}]}}` + "\n"
	fixStart := `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":3,"role":"fix"}}` + "\n"
	fixNar := `{"type":"assistant","message":{"content":[{"type":"text","text":"Applying the fix."}]}}` + "\n"
	fmt.Fprint(w, reviewStart)
	fmt.Fprint(w, reviewNar)
	fmt.Fprint(w, fixStart)
	fmt.Fprint(w, fixNar)

	out := status.String()
	if !strings.Contains(out, rule+" reviewer ") {
		t.Errorf("missing reviewer switch header after review pass_start: %q", out)
	}
	if !strings.Contains(out, rule+" implementor ") {
		t.Errorf("missing implementor switch header after fix pass_start: %q", out)
	}
	if !strings.Contains(out, "Reviewing the change.") {
		t.Errorf("missing review narration: %q", out)
	}
	if !strings.Contains(out, "Applying the fix.") {
		t.Errorf("missing fix narration: %q", out)
	}
}

// A "land" pass_start after a "review" pass_start switches the active role
// back to implementor, per issue #2654 acceptance criterion 1.
func TestWriterPassStartSwitchesBackToImplementorOnLand(t *testing.T) {
	const rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "2654", &status)

	reviewStart := `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":2,"role":"review"}}` + "\n"
	reviewNar := `{"type":"assistant","message":{"content":[{"type":"text","text":"Reviewing the change."}]}}` + "\n"
	landStart := `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":3,"role":"land"}}` + "\n"
	landNar := `{"type":"assistant","message":{"content":[{"type":"text","text":"Landing the change."}]}}` + "\n"
	fmt.Fprint(w, reviewStart)
	fmt.Fprint(w, reviewNar)
	fmt.Fprint(w, landStart)
	fmt.Fprint(w, landNar)

	out := status.String()
	if !strings.Contains(out, rule+" reviewer ") {
		t.Errorf("missing reviewer switch header after review pass_start: %q", out)
	}
	if !strings.Contains(out, rule+" implementor ") {
		t.Errorf("missing implementor switch header after land pass_start: %q", out)
	}
	if !strings.Contains(out, "Reviewing the change.") {
		t.Errorf("missing review narration: %q", out)
	}
	if !strings.Contains(out, "Landing the change.") {
		t.Errorf("missing land narration: %q", out)
	}
}

// A pass_start with no Role, the legacy single-loop dispatch shape, leaves the
// active top-level role unchanged: a later top-level turn is still attributed
// to implementor, exactly as if the pass_start were absent (issue #2382).
func TestWriterPassStartEmptyRoleDoesNotChangeActiveRole(t *testing.T) {
	const rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "2382", &status)

	passStart := `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":1}}` + "\n"
	narEv := `{"type":"assistant","message":{"content":[{"type":"text","text":"Implementing the change."}]}}` + "\n"
	fmt.Fprint(w, passStart)
	fmt.Fprint(w, narEv)

	out := status.String()
	if !strings.Contains(out, rule+" implementor ") {
		t.Errorf("missing implementor switch header after roleless pass_start: %q", out)
	}
	if strings.Contains(out, rule+" reviewer ") {
		t.Errorf("must not emit a reviewer header after roleless pass_start: %q", out)
	}
}

// A pass_usage op renders its pass number, role, and aggregate totals as one
// status row (issue #3156).
func TestFormatSpindriftOpPassUsage(t *testing.T) {
	got := claude.FormatSpindriftOp("42", claude.SpindriftOp{
		Op:   "pass_usage",
		Pass: 2,
		Role: "review",
		Usage: &claude.PassUsage{
			APICalls:                 932,
			UncachedInputTokens:      1200,
			OutputTokens:             240000,
			CacheReadInputTokens:     65000000,
			CacheCreationInputTokens: 1200000,
			OutputIsMainLoopOnly:     true,
		},
	})
	if !strings.HasPrefix(got, "#42 ") {
		t.Errorf("FormatSpindriftOp = %q, want it to start with issue tag %q", got, "#42 ")
	}
	if !strings.Contains(got, "pass 2 (review) usage") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "pass 2 (review) usage")
	}
	if !strings.Contains(got, "932") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the API call count %d", got, 932)
	}
	if !strings.Contains(got, "65000000") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the cache-read total %d", got, 65000000)
	}
	if !strings.Contains(got, "1200000") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the cache-write total %d", got, 1200000)
	}
	if !strings.Contains(got, "240000") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain the output-token total %d", got, 240000)
	}
	if !strings.Contains(got, "out (main loop)") {
		t.Errorf("FormatSpindriftOp = %q, want the out column marked main-loop-only (issue #3213)", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want a single line", got)
	}
}

// The "(main loop)" caveat comes from the payload's own flag, not a hardcoded
// string: a driver whose report carries whole-pass output (opencode) renders
// an unqualified out column, or the caveat would understate real data.
func TestFormatSpindriftOpPassUsagePlainOutWhenNotMainLoopOnly(t *testing.T) {
	got := claude.FormatSpindriftOp("42", claude.SpindriftOp{
		Op:   "pass_usage",
		Pass: 2,
		Usage: &claude.PassUsage{
			APICalls:     7,
			OutputTokens: 4242,
		},
	})
	if !strings.Contains(got, "4242 out,") {
		t.Errorf("FormatSpindriftOp = %q, want an unqualified %q column", got, "4242 out,")
	}
	if strings.Contains(got, "main loop") {
		t.Errorf("FormatSpindriftOp = %q, want no main-loop-only caveat when the payload does not assert it", got)
	}
}

// The per-agent tail renders in the payload's given order, which
// breakdownByAgentFile sets as main loop first then costliest subagent first,
// rather than re-sorting it.
func TestFormatSpindriftOpPassUsageAgentTail(t *testing.T) {
	got := claude.FormatSpindriftOp("42", claude.SpindriftOp{
		Op:   "pass_usage",
		Pass: 2,
		Usage: &claude.PassUsage{
			Agents: []usage.AgentUsage{
				{Agent: usage.MainLoopAgent, UncachedInputTokens: 210},
				{Agent: "worker", UncachedInputTokens: 480},
				{Agent: "scout", UncachedInputTokens: 242},
			},
		},
	})
	iMain := strings.Index(got, "main")
	iWorker := strings.Index(got, "worker")
	iScout := strings.Index(got, "scout")
	if iMain == -1 || iWorker == -1 || iScout == -1 {
		t.Fatalf("FormatSpindriftOp = %q, want all three agent labels present", got)
	}
	if !(iMain < iWorker && iWorker < iScout) {
		t.Errorf("FormatSpindriftOp = %q, want agents rendered in given order main, worker, scout", got)
	}
}

// A pass_usage op with a nil Usage still renders a single line instead of
// panicking: the case of a pass that crashes or produces no usage events
// reaching the render path.
func TestFormatSpindriftOpPassUsageNilUsage(t *testing.T) {
	got := claude.FormatSpindriftOp("42", claude.SpindriftOp{Op: "pass_usage", Pass: 1})
	if !strings.Contains(got, "pass 1 usage") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "pass 1 usage")
	}
	if strings.Contains(got, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want a single line", got)
	}
}

// An agent name carrying a control character or newline is sanitized like
// every other dynamic field (issue #2027 AC): agent labels come from a
// subagent_type field in the Box's own stream, which is untrusted content.
func TestFormatSpindriftOpPassUsageSanitizesAgentNames(t *testing.T) {
	got := claude.FormatSpindriftOp("42", claude.SpindriftOp{
		Op:   "pass_usage",
		Pass: 1,
		Usage: &claude.PassUsage{
			Agents: []usage.AgentUsage{
				{Agent: "bad\x1b[2J\nfake-row"},
			},
		},
	})
	if strings.Contains(got, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded newline", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded escape sequence", got)
	}
}

// A land_delta op renders its counted, zero, and unknown cases as three
// distinct lines: zero is never simply omitted, and an unknown delta names its
// own Reason (issue #3244).
func TestFormatSpindriftOpLandDelta(t *testing.T) {
	counted := claude.FormatSpindriftOp("42", claude.SpindriftOp{
		Op:   "land_delta",
		Pass: 5,
		Delta: &landdelta.Delta{
			Known:      true,
			Files:      2,
			Insertions: 41,
			Deletions:  3,
		},
	})
	want := "#42 \xe2\x97\x8b " + (landdelta.Delta{Known: true, Files: 2, Insertions: 41, Deletions: 3}).Summary()
	if counted != want {
		t.Errorf("FormatSpindriftOp = %q, want %q (must match landdelta.Delta.Summary() -- issue #3244)", counted, want)
	}

	zero := claude.FormatSpindriftOp("42", claude.SpindriftOp{
		Op:    "land_delta",
		Pass:  5,
		Delta: &landdelta.Delta{Known: true},
	})
	if !strings.Contains(zero, "none") {
		t.Errorf("FormatSpindriftOp = %q, want the zero delta stated explicitly", zero)
	}

	unknown := claude.FormatSpindriftOp("42", claude.SpindriftOp{
		Op:    "land_delta",
		Pass:  5,
		Delta: &landdelta.Delta{Known: false, Reason: "no reviewed-commit anchor"},
	})
	if !strings.Contains(unknown, "unknown (no reviewed-commit anchor)") {
		t.Errorf("FormatSpindriftOp = %q, want the unknown delta to name its own Reason", unknown)
	}
}

// A land_delta op with a nil Delta degrades to a single line instead of
// panicking, mirroring TestFormatSpindriftOpPassUsageNilUsage.
func TestFormatSpindriftOpLandDeltaNilDelta(t *testing.T) {
	got := claude.FormatSpindriftOp("42", claude.SpindriftOp{Op: "land_delta", Pass: 5})
	if !strings.Contains(got, "unknown") {
		t.Errorf("FormatSpindriftOp = %q, want a nil Delta to degrade to the unknown case", got)
	}
	if strings.Contains(got, "()") {
		t.Errorf("FormatSpindriftOp = %q, want a stand-in reason, not an empty parenthesis", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want a single line", got)
	}
}

// A Box-influenced Reason, such as one naming an unresolved base ref, is
// sanitized like every other dynamic field (issue #2027 AC).
func TestFormatSpindriftOpLandDeltaSanitizesReason(t *testing.T) {
	got := claude.FormatSpindriftOp("42", claude.SpindriftOp{
		Op:    "land_delta",
		Pass:  5,
		Delta: &landdelta.Delta{Known: false, Reason: "bad\x1b[2J\nfake-row"},
	})
	if strings.Contains(got, "\n") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded newline", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("FormatSpindriftOp = %q, want no embedded escape sequence", got)
	}
}

// The nested PassUsage and Agents payload survives the same stream-json
// encode/decode seam every other op kind already does (issue #3156).
func TestEncodeSpindriftOpPassUsageRoundTrip(t *testing.T) {
	want := claude.SpindriftOp{
		Op:   "pass_usage",
		Pass: 2,
		Role: "review",
		Usage: &claude.PassUsage{
			APICalls:                 932,
			UncachedInputTokens:      1200,
			OutputTokens:             240000,
			CacheReadInputTokens:     65000000,
			CacheCreationInputTokens: 1200000,
			Agents: []usage.AgentUsage{
				{Agent: usage.MainLoopAgent, APICalls: 500, UncachedInputTokens: 210},
				{Agent: "worker", APICalls: 300, UncachedInputTokens: 480},
			},
		},
	}
	line := claude.EncodeSpindriftOp(want)

	var ev struct {
		SpindriftOp *claude.SpindriftOp `json:"spindrift_op"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSuffix(line, "\n")), &ev); err != nil {
		t.Fatalf("json.Unmarshal(%q) = %v", line, err)
	}
	if ev.SpindriftOp == nil {
		t.Fatalf("decoded spindrift_op is nil")
	}
	if !reflect.DeepEqual(*ev.SpindriftOp, want) {
		t.Errorf("round-tripped SpindriftOp = %+v, want %+v", *ev.SpindriftOp, want)
	}
}

// The nested landdelta.Delta payload survives the same stream-json
// encode/decode seam TestEncodeSpindriftOpPassUsageRoundTrip covers for
// pass_usage (issue #3244).
func TestEncodeSpindriftOpLandDeltaRoundTrip(t *testing.T) {
	want := claude.SpindriftOp{
		Op:   "land_delta",
		Pass: 5,
		Delta: &landdelta.Delta{
			Known:      true,
			Files:      2,
			Insertions: 41,
			Deletions:  3,
		},
	}
	line := claude.EncodeSpindriftOp(want)

	var ev struct {
		SpindriftOp *claude.SpindriftOp `json:"spindrift_op"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSuffix(line, "\n")), &ev); err != nil {
		t.Fatalf("json.Unmarshal(%q) = %v", line, err)
	}
	if ev.SpindriftOp == nil {
		t.Fatalf("decoded spindrift_op is nil")
	}
	if !reflect.DeepEqual(*ev.SpindriftOp, want) {
		t.Errorf("round-tripped SpindriftOp = %+v, want %+v", *ev.SpindriftOp, want)
	}
}
