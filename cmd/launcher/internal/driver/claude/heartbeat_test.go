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

// Compile-shape guard: New() takes exactly three arguments, no throttle.
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
	// No bare heartbeat line: no line that is just "#42" or "#42 [explore]" with nothing useful after.
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
	// Header + narration + count = 3 lines total, not 5 per-tool lines.
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

// Subagent output (assistant text carrying a parent_tool_use_id) is dropped
// from the heartbeat stream, but the raw log still receives every byte.
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

func TestWriterNarrationBeforeTool(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "42", &status)

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

// Control characters, newlines, and CSI/OSC escapes in role must not break the
// single-line row.
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

// Control characters, newlines, and CSI/OSC escapes in role must not break the
// single-line row.
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
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for _, line := range lines {
		if strings.Contains(line, "Second window") {
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

		fmt.Fprint(w, implTool("Read", "r0"))
		fmt.Fprint(w, implTask("tu_s1", "scout"))
		// Scout does a read (counts should be separate from implementor's).
		fmt.Fprint(w, subRead("tu_s1"))
		// Implementor resumes with narration.
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

		// Launch scout twice; between them implementor emits a narration so both
		// scout stints produce counts (the second scout header must appear).
		fmt.Fprint(w, implTask("tu_a", "scout"))
		fmt.Fprint(w, subRead("tu_a"))
		fmt.Fprint(w, implNar("Checking."))
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

		unknown := `{"type":"assistant","parent_tool_use_id":"unknown_id","message":{"content":[{"type":"tool_use","name":"Read","id":"rx","input":{}}]}}` + "\n"
		fmt.Fprint(w, unknown)
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

// TestWriterSwitchHeader_AgentToolName verifies that a spawn block using the
// confirmed real tool name "Agent" (not the fallback "Task") still resolves
// the subagent_type to a named role header — "reviewer" here — rather than
// falling back to the generic "subagent" label (#2078).
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
	// Reviewer does a read (counts should be separate from implementor's).
	fmt.Fprint(w, subRead("tu_r1"))
	// Implementor resumes with narration.
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

// TestWriterSwitchHeader_NestedSubagent verifies that a subagent B spawned by
// another subagent A — two levels below the implementor — is labeled with
// B's own subagent_type in the switch header, not the generic "subagent"
// fallback (issue #2079).
func TestWriterSwitchHeader_NestedSubagent(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "2079", &status)

	implAgent := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Agent","id":"toolu_A","input":{"subagent_type":"researcher"}}]}}` + "\n"
	// A's own message spawns B (subagent_type "worker"); this event carries
	// parent_tool_use_id "toolu_A" (A is the actor) AND B's spawn block.
	aSpawnsB := `{"type":"assistant","parent_tool_use_id":"toolu_A","message":{"content":[{"type":"tool_use","name":"Agent","id":"toolu_B","input":{"subagent_type":"worker"}}]}}` + "\n"
	// B's own message, nested two levels deep under the implementor.
	bRead := `{"type":"assistant","parent_tool_use_id":"toolu_B","message":{"content":[{"type":"tool_use","name":"Read","id":"r1","input":{}}]}}` + "\n"
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
		// Two consecutive narrations with the same (role, model) — only one header.
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

// Control characters, newlines, and CSI/OSC escapes in role must not break the
// single-line row, and the trailing rule pads out on the sanitized (not raw)
// role length.
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

// A "spindrift_op" stream-json event carrying a verdict op surfaces as a status
// row (issue #2027), interleaved with ordinary narration.
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

// The trailing separator must be omitted when reason is empty (issue #2027).
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

// The exact seam the orchestrator uses to emit its own operations onto the same
// stdout stream driver-exec's raw output already flows through (issue #2027).
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

// Adding the "spindrift_op" case must not disturb the silent-drop fallback for
// unrecognized JSON event types and bare non-JSON lines (issue #2027).
func TestWriterIgnoresUnrecognizedEventTypesAndNonJSONLines(t *testing.T) {
	var status bytes.Buffer
	w := claude.New(&bytes.Buffer{}, "9", &status)

	fmt.Fprint(w, `{"type":"system","session_id":"s1"}`+"\n")
	fmt.Fprint(w, "not json at all\n")

	if status.String() != "" {
		t.Errorf("status = %q, want empty (unrecognized type and non-JSON line both silently dropped)", status.String())
	}
}

func TestFormatSpindriftOpRunStateError(t *testing.T) {
	got := claude.FormatSpindriftOp("7", claude.SpindriftOp{Op: "run_state_error", Phase: "write", Error: "permission denied"})
	if !strings.Contains(got, "run-state write failed: permission denied") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "run-state write failed: permission denied")
	}
}

// Phase "dispositions_budget" (issue #2550) gets its own wording, not "run-state
// dispositions_budget failed: ..." -- the tripwire is a loud, non-fatal budget
// notice, not a run-state read, write, or append failure.
func TestFormatSpindriftOpDispositionsBudget(t *testing.T) {
	got := claude.FormatSpindriftOp("7", claude.SpindriftOp{Op: "run_state_error", Phase: "dispositions_budget", Error: "round 1 mean 283.0/entry (ceiling 40), total 283 tokens (ceiling 400)"})
	if !strings.Contains(got, "dispositions budget: round 1 mean 283.0/entry (ceiling 40), total 283 tokens (ceiling 400)") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "dispositions budget: round 1 mean 283.0/entry (ceiling 40), total 283 tokens (ceiling 400)")
	}
	if strings.Contains(got, "run-state dispositions_budget failed") {
		t.Errorf("FormatSpindriftOp = %q, must not render the budget tripwire as a run-state failure", got)
	}
}

// The implementor-side counterpart tripwire (issue #2695), mirroring
// TestFormatSpindriftOpDispositionsBudget's assertion shape.
func TestFormatSpindriftOpDecisionsBudget(t *testing.T) {
	got := claude.FormatSpindriftOp("7", claude.SpindriftOp{Op: "run_state_error", Phase: "decisions_budget", Error: "round 1 mean 283.0/entry (ceiling 50), total 283 tokens (ceiling 400)"})
	if !strings.Contains(got, "decisions budget: round 1 mean 283.0/entry (ceiling 50), total 283 tokens (ceiling 400)") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "decisions budget: round 1 mean 283.0/entry (ceiling 50), total 283 tokens (ceiling 400)")
	}
	if strings.Contains(got, "run-state decisions_budget failed") {
		t.Errorf("FormatSpindriftOp = %q, must not render the budget tripwire as a run-state failure", got)
	}
}

// A pass_no_outcome op (issue #2036) names the last verdict seen inline, so an
// operator can tell a mid-turn cutoff after a BLOCK apart from one with no
// verdict at all.
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

// Control characters, newlines, and CSI/OSC escapes in a decision's reason or a
// run_state_error's error text must not break the single-line row (issue #2027).
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

func TestFormatSpindriftOpPassStart(t *testing.T) {
	got := claude.FormatSpindriftOp("42", claude.SpindriftOp{Op: "pass_start", Pass: 2})
	if !strings.HasPrefix(got, "#42 ") {
		t.Errorf("FormatSpindriftOp = %q, want it to start with issue tag %q", got, "#42 ")
	}
	if !strings.Contains(got, "pass 2 started") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "pass 2 started")
	}
}

// worker_finish carries a trailing reason only when Reason is non-empty
// (issue #2059).
func TestFormatSpindriftOpWorkerStartAndFinish(t *testing.T) {
	gotStart := claude.FormatSpindriftOp("42", claude.SpindriftOp{Op: "worker_start", Worker: "slice-a"})
	if !strings.Contains(gotStart, "worker slice-a started") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", gotStart, "worker slice-a started")
	}

	gotFinishNoReason := claude.FormatSpindriftOp("42", claude.SpindriftOp{Op: "worker_finish", Worker: "slice-a", WorkerStatus: "done"})
	if !strings.Contains(gotFinishNoReason, "worker slice-a finished: done") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", gotFinishNoReason, "worker slice-a finished: done")
	}
	if strings.Contains(gotFinishNoReason, "(") {
		t.Errorf("FormatSpindriftOp = %q, want no trailing reason parenthetical when Reason is empty", gotFinishNoReason)
	}

	gotFinishWithReason := claude.FormatSpindriftOp("42", claude.SpindriftOp{Op: "worker_finish", Worker: "slice-b", WorkerStatus: "timed_out", Reason: "timeout after 20m0s"})
	if !strings.Contains(gotFinishWithReason, "worker slice-b finished: timed_out (timeout after 20m0s)") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", gotFinishWithReason, "worker slice-b finished: timed_out (timeout after 20m0s)")
	}
}

// NewWithTopLevelRole attributes a top-level (no parent_tool_use_id) assistant
// event to the given role — switch header and buffered count line alike
// (issue #2092).
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

// Naming the role inline (issue #2037: "implement", "review", "fix") lets
// #2027's telemetry tell a review pass's pass_start apart from an implement/fix
// pass's -- both of which, unlike a legacy single-pass run, may legitimately end
// with no SPINDRIFT_OUTCOME of their own.
func TestFormatSpindriftOpPassStartWithRole(t *testing.T) {
	got := claude.FormatSpindriftOp("42", claude.SpindriftOp{Op: "pass_start", Pass: 2, Role: "review"})
	if !strings.Contains(got, "pass 2 (review) started") {
		t.Errorf("FormatSpindriftOp = %q, want it to contain %q", got, "pass 2 (review) started")
	}
}

// Plain New (no static topLevelRole — the legacy stream that carries no role
// info until the orchestrator starts emitting pass_start ops) must still switch
// its active top-level role mid-stream on a pass_start whose Role is non-empty:
// subsequent top-level turns attribute to reviewer, not the ImplementorRole
// default, across switch-header, count, and heartbeat lines alike (issue #2382).
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

// The implement → review → fix sequence a code-owned review's BLOCK verdict
// drives (issue #2382).
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

// Pins the land role's heartbeat rendering at the Writer surface directly
// (issue #2654): "pass N (land) started", and the active role switches back to
// implementor.
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

// A pass_start with no Role is the legacy single-loop dispatch shape (matching
// TestFormatSpindriftOpPassStart): the active top-level role must stay
// implementor, exactly as if the pass_start were absent (issue #2382).
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
