package claude_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/landdelta"
	"spindrift.dev/launcher/internal/usage"
)

// newHeartbeat is the claude Driver's heartbeat writer over raw, reporting to out.
func newHeartbeat(t *testing.T, raw io.Writer, issue string, out io.Writer, opts driverkit.RenderOptions) io.Writer {
	t.Helper()
	return newDriver(t).NewHeartbeatWriter(raw, issue, out, opts)
}

func newWriter(t *testing.T, issue string, status *bytes.Buffer) io.Writer {
	t.Helper()
	return newHeartbeat(t, &bytes.Buffer{}, issue, status, driverkit.RenderOptions{})
}

func newWriterRaw(t *testing.T, raw *bytes.Buffer, issue string, status *bytes.Buffer) io.Writer {
	t.Helper()
	return newHeartbeat(t, raw, issue, status, driverkit.RenderOptions{})
}

func TestWriterPassesRawBytesUnchanged(t *testing.T) {
	var raw bytes.Buffer
	var status bytes.Buffer
	w := newWriterRaw(t, &raw, "42", &status)

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
	w := newWriterRaw(t, &raw, "1", &status)

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
	w := newWriter(t, "42", &status)

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
	w := newWriter(t, "7", &status)

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

// Pins the shared count formatter's fixed kind order and the "searches" plural.
// Tools arrive out of display order so the ordering is proven; a phase change
// flushes the counts, so kinds that share a phase share a line (edit, search
// and explore never do, hence three lines).
func TestWriterToolCountsExactLines(t *testing.T) {
	var status bytes.Buffer
	w := newWriter(t, "7", &status)

	tool := func(name, input string) {
		fmt.Fprint(w, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"`+name+`","input":`+input+`}]}}`+"\n")
	}
	tool("Skill", `{"skill":"x"}`)
	tool("Task", `{"subagent_type":"general"}`)
	tool("Bash", `{"command":"ls"}`)
	tool("Read", `{"file_path":"a.go"}`)
	tool("Read", `{"file_path":"b.go"}`)
	tool("WebSearch", `{"query":"a"}`)
	tool("WebSearch", `{"query":"b"}`)
	for range 3 {
		tool("Grep", `{"pattern":"p"}`)
	}
	tool("Edit", `{"file_path":"main.go"}`)
	fmt.Fprint(w, `{"type":"result","num_turns":1}`+"\n")

	want := "#7 ── implementor ──────────────────\n" +
		"#7 [explore] · 2 reads, 1 bash, 1 subagent, 1 skill\n" +
		"#7 [search] · 3 greps, 2 searches\n" +
		"#7 [edit] · 1 edit\n" +
		"#7 [edit] · 1 turn\n"
	if got := status.String(); got != want {
		t.Errorf("heartbeat output:\n got %q\nwant %q", got, want)
	}
}

func TestWriterToolCountSurvivesNonStringThinkingKey(t *testing.T) {
	var status bytes.Buffer
	w := newWriter(t, "7", &status)

	fmt.Fprint(w, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"},"thinking":0}]}}`+"\n")
	fmt.Fprint(w, `{"type":"result","num_turns":1}`+"\n")

	if out := status.String(); !strings.Contains(out, "1 edit") {
		t.Errorf("count line missing '1 edit': %q", out)
	}
}

func TestWriterEmitsOnResultEvent(t *testing.T) {
	var status bytes.Buffer
	w := newWriter(t, "9", &status)

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
	w := newHeartbeat(t, &bytes.Buffer{}, "1", &bytes.Buffer{}, driverkit.RenderOptions{})
	if w == nil {
		t.Fatal("New returned nil")
	}
}

func TestWriterBareResultEmitsNothing(t *testing.T) {
	var status bytes.Buffer
	w := newWriter(t, "42", &status)

	event := `{"type":"result"}` + "\n"
	fmt.Fprint(w, event)

	if status.Len() > 0 {
		t.Errorf("bare result must emit nothing, got: %q", status.String())
	}
}

func TestWriterResultWithoutTurnsFlushesCountsOnly(t *testing.T) {
	var status bytes.Buffer
	w := newWriter(t, "42", &status)

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

func TestWriterResultWithMistypedOtherTotalsStillPrintsTurns(t *testing.T) {
	toolEv := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}` + "\n"
	for _, field := range []string{`"total_cost_usd":"x"`, `"duration_ms":"x"`, `"usage":"x"`} {
		var status bytes.Buffer
		w := newWriter(t, "42", &status)
		fmt.Fprint(w, toolEv)
		fmt.Fprint(w, `{"type":"result","num_turns":3,`+field+`}`+"\n")

		out := status.String()
		if !strings.Contains(out, "1 read") || !strings.Contains(out, "3 turns") {
			t.Errorf("%s: want count line and turn line, got %q", field, out)
		}
	}
}

func TestWriterResultWithMistypedTurnsPrintsNothing(t *testing.T) {
	var status bytes.Buffer
	w := newWriter(t, "42", &status)
	fmt.Fprint(w, `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"a.go"}}]}}`+"\n")
	fmt.Fprint(w, `{"type":"result","num_turns":"3"}`+"\n")

	if out := status.String(); strings.Contains(out, "read") || strings.Contains(out, "turns") {
		t.Errorf("mistyped num_turns must drop the result line, got %q", out)
	}
}

func TestWriterTolerateMalformedJSON(t *testing.T) {
	var raw bytes.Buffer
	var status bytes.Buffer
	w := newWriterRaw(t, &raw, "3", &status)

	lines := "not json at all\n{broken: json}\n\x00\x01\x02\n"
	fmt.Fprint(w, lines)

	if raw.String() != lines {
		t.Errorf("raw passthrough broken: got %q, want %q", raw.String(), lines)
	}
}

func TestWriterThrottlesSameToolRepeat(t *testing.T) {
	var status bytes.Buffer
	w := newHeartbeat(t, &bytes.Buffer{}, "5", &status, driverkit.RenderOptions{})

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
	w := newHeartbeat(t, &bytes.Buffer{}, "11", &status, driverkit.RenderOptions{})

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
	w := newHeartbeat(t, &bytes.Buffer{}, "42", &status, driverkit.RenderOptions{})

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

// narrationLine feeds one assistant text block and returns the narration row
// (the line after the header).
func narrationLine(t *testing.T, issue, text string) string {
	t.Helper()
	var status bytes.Buffer
	w := newWriter(t, issue, &status)
	b, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(w, `{"type":"assistant","message":{"content":[{"type":"text","text":`+string(b)+`}]}}`+"\n")
	lines := strings.Split(strings.TrimRight(status.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines (header + narration), got %d: %q", len(lines), status.String())
	}
	return lines[1]
}

// Agent-controlled narration text must not carry escape sequences or control
// characters to the operator terminal, and a punctuation byte inside an escape
// sequence must not end the sentence mid-sequence.
func TestWriterNarrationSanitized(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"escapes and control", "\x1b[2J\x1b]0;pwned\x07hi", "hi"},
		{"lone CR is a line break", "hi\rX", "hi"},
		{"CSI with question mark", "\x1b[?25lHello there.", "Hello there."},
		{"OSC hyperlink with dot", "\x1b]8;;https://x.com\alink text.", "link text."},
		{"escape-only first line skipped", "\x1b[0m\nHello.", "Hello."},
		{"line separator is dropped, not a break", "foo\u2028bar", "foobar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := narrationLine(t, "7", tc.in)
			if want := "#7 \xc2\xb7 " + tc.want; got != want {
				t.Errorf("narration = %q, want %q", got, want)
			}
		})
	}
}

// The 120-character cap counts runes, so it never splits a multi-byte rune.
func TestWriterNarrationCapIsRuneSafe(t *testing.T) {
	prefix := "#7 \xc2\xb7 "
	cases := []struct{ name, in, want string }{
		{"under cap", strings.Repeat("é", 70), strings.Repeat("é", 70)},
		{"exactly at cap", strings.Repeat("é", 120), strings.Repeat("é", 120)},
		{"one over cap", strings.Repeat("é", 121), strings.Repeat("é", 117) + "..."},
		{"well over cap", strings.Repeat("é", 130), strings.Repeat("é", 117) + "..."},
		{"cut just after a space", strings.Repeat("é", 116) + " " + strings.Repeat("é", 10), strings.Repeat("é", 116) + "..."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := narrationLine(t, "7", tc.in)
			if !utf8.ValidString(got) {
				t.Errorf("narration is not valid UTF-8: %q", got)
			}
			if text := strings.TrimPrefix(got, prefix); text != tc.want {
				t.Errorf("narration = %q, want %q", text, tc.want)
			}
		})
	}
}

// An assistant text block carrying a parent_tool_use_id is subagent output.
func TestWriterSubagentNarrationDropped(t *testing.T) {
	var raw bytes.Buffer
	var status bytes.Buffer
	w := newHeartbeat(t, &raw, "55", &status, driverkit.RenderOptions{})

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
	w := newHeartbeat(t, &bytes.Buffer{}, "42", &status, driverkit.RenderOptions{})

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
		w := newHeartbeat(t, &bytes.Buffer{}, "8", &status, driverkit.RenderOptions{})
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
		got := driverkit.FormatHeartbeat(tc.issue, tc.turns, tc.lastTool, tc.role, tc.phase)
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
	got := driverkit.FormatHeartbeat("42", 3, "Edit", "scout\x1b[2J\nfake-row", "edit")
	want := "#42 scoutfake-row [edit] \xc2\xb7 3 turns \xc2\xb7 Edit"
	if got != want {
		t.Errorf("FormatHeartbeat role not sanitized, got %q, want %q", got, want)
	}
}

func TestWriterHeartbeatIncludesPhase(t *testing.T) {
	var status bytes.Buffer
	w := newHeartbeat(t, &bytes.Buffer{}, "42", &status, driverkit.RenderOptions{})

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
	w := newHeartbeat(t, &bytes.Buffer{}, "11", &status, driverkit.RenderOptions{})

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
	w := newHeartbeat(t, &bytes.Buffer{}, "8", &status, driverkit.RenderOptions{})

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

func TestWriterNarrationSurvivesNonStringThinkingKey(t *testing.T) {
	var status bytes.Buffer
	w := newWriter(t, "8", &status)

	fmt.Fprint(w, `{"type":"assistant","message":{"content":[{"type":"text","text":"hello","thinking":0}]}}`+"\n")

	if out := status.String(); !strings.Contains(out, "hello") {
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
		got := driverkit.FormatCountLine(tc.issue, tc.role, tc.phase, tc.counts)
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
	got := driverkit.FormatCountLine("42", "scout\x1b]0;pwn\x07\nfake-row", "explore", map[string]int{"read": 1})
	want := "#42 scoutfake-row [explore] \xc2\xb7 1 read"
	if got != want {
		t.Errorf("FormatCountLine role not sanitized, got %q, want %q", got, want)
	}
}

func TestWriterCountsResetOnNarration(t *testing.T) {
	var status bytes.Buffer
	w := newHeartbeat(t, &bytes.Buffer{}, "99", &status, driverkit.RenderOptions{})

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
	w := newHeartbeat(t, &bytes.Buffer{}, "42", &status, driverkit.RenderOptions{})

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
		w := newHeartbeat(t, &bytes.Buffer{}, "284", &status, driverkit.RenderOptions{})
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
		w := newHeartbeat(t, &bytes.Buffer{}, "284", &status, driverkit.RenderOptions{})

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
		w := newHeartbeat(t, &bytes.Buffer{}, "1", &status, driverkit.RenderOptions{})

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
		w := newHeartbeat(t, &bytes.Buffer{}, "5", &status, driverkit.RenderOptions{})

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
		w := newHeartbeat(t, &bytes.Buffer{}, "9", &status, driverkit.RenderOptions{})

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
	w := newHeartbeat(t, &bytes.Buffer{}, "284", &status, driverkit.RenderOptions{})

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
	w := newHeartbeat(t, &bytes.Buffer{}, "2079", &status, driverkit.RenderOptions{})

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
	w := newHeartbeat(t, &bytes.Buffer{}, "9", &status, driverkit.RenderOptions{})

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
		w := newHeartbeat(t, &bytes.Buffer{}, "1", &status, driverkit.RenderOptions{})
		fmt.Fprint(w, implNarWithModel("Planning.", "claude-opus-4-8"))
		out := status.String()
		if !strings.Contains(out, rule+" implementor \xc2\xb7 opus ") {
			t.Errorf("header must contain 'implementor · opus': %q", out)
		}
	})

	t.Run("missing_model_role_only", func(t *testing.T) {
		var status bytes.Buffer
		w := newHeartbeat(t, &bytes.Buffer{}, "2", &status, driverkit.RenderOptions{})
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
		w := newHeartbeat(t, &bytes.Buffer{}, "3", &status, driverkit.RenderOptions{})
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
		w := newHeartbeat(t, &bytes.Buffer{}, "4", &status, driverkit.RenderOptions{})
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
	h := driverkit.FormatRoleHeader("42", "scout", "haiku")
	if !strings.Contains(h, rule+" scout \xc2\xb7 haiku ") {
		t.Errorf("header missing 'scout · haiku': %q", h)
	}
	hNoModel := driverkit.FormatRoleHeader("42", "scout", "")
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
	got := driverkit.FormatRoleHeader("42", "scout\x1b[2J\nfake-row", "")
	want := "#42 \xe2\x94\x80\xe2\x94\x80 scoutfake-row " + strings.Repeat("\xe2\x94\x80", 15)
	if got != want {
		t.Errorf("FormatRoleHeader role not sanitized, got %q, want %q", got, want)
	}
}

func TestWriterCountLineOnNarration(t *testing.T) {
	var status bytes.Buffer
	w := newHeartbeat(t, &bytes.Buffer{}, "228", &status, driverkit.RenderOptions{})

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
	w := newHeartbeat(t, &bytes.Buffer{}, "7", &status, driverkit.RenderOptions{})

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

// hostop.EncodeSpindriftOp produces one newline-terminated stream-json line
// that the Writer parses back into a status row. That is the seam the
// orchestrator uses to emit its own operations onto the stdout stream
// driver-exec's raw output already flows through (issue #2027).
func TestEncodeSpindriftOpFeedsWriter(t *testing.T) {
	line := hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: "pass_start", Pass: 3})
	if !strings.HasSuffix(line, "\n") {
		t.Fatalf("EncodeSpindriftOp = %q, want a trailing newline", line)
	}
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("EncodeSpindriftOp = %q, want exactly one newline", line)
	}

	var status bytes.Buffer
	w := newHeartbeat(t, &bytes.Buffer{}, "3", &status, driverkit.RenderOptions{})
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
	w := newHeartbeat(t, &bytes.Buffer{}, "9", &status, driverkit.RenderOptions{})

	fmt.Fprint(w, `{"type":"system","session_id":"s1"}`+"\n")
	fmt.Fprint(w, "not json at all\n")

	if status.String() != "" {
		t.Errorf("status = %q, want empty (unrecognized type and non-JSON line both silently dropped)", status.String())
	}
}

// A heartbeat built with a TopLevelRole attributes a top-level assistant
// event, one with no parent_tool_use_id, to the given topLevelRole: both the
// switch header and the buffered count line bucket under it (issue #2092).
func TestWriterTopLevelRoleAppliesToTopLevelMessage(t *testing.T) {
	const rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	var status bytes.Buffer
	w := newHeartbeat(t, &bytes.Buffer{}, "2092", &status, driverkit.RenderOptions{TopLevelRole: "reviewer"})

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

// A Writer built via plain New, with no static topLevelRole, switches its
// active top-level role mid-stream on a pass_start op whose Role is non-empty:
// a review pass attributes later top-level turns to reviewer, not the
// ImplementorRole default, across header, count, and heartbeat lines alike
// (issue #2382).
func TestWriterPassStartSwitchesActiveTopLevelRole(t *testing.T) {
	const rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	var status bytes.Buffer
	w := newHeartbeat(t, &bytes.Buffer{}, "2382", &status, driverkit.RenderOptions{})

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
	w := newHeartbeat(t, &bytes.Buffer{}, "2382", &status, driverkit.RenderOptions{})

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
	w := newHeartbeat(t, &bytes.Buffer{}, "2654", &status, driverkit.RenderOptions{})

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

// A "delta-review" pass_start after a "land" pass_start switches the active
// role to reviewer: the run-once delta-review pass that can follow land
// (issue #3246) is a review, not implementor work (issue #4425).
func TestWriterPassStartSwitchesToReviewerOnDeltaReviewAfterLand(t *testing.T) {
	const rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	var status bytes.Buffer
	w := newHeartbeat(t, &bytes.Buffer{}, "4425", &status, driverkit.RenderOptions{})

	landStart := `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":3,"role":"land"}}` + "\n"
	landNar := `{"type":"assistant","message":{"content":[{"type":"text","text":"Landing the change."}]}}` + "\n"
	deltaStart := `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":4,"role":"delta-review"}}` + "\n"
	deltaNar := `{"type":"assistant","message":{"content":[{"type":"text","text":"Reviewing the delta."}]}}` + "\n"
	fmt.Fprint(w, landStart)
	fmt.Fprint(w, landNar)
	fmt.Fprint(w, deltaStart)
	fmt.Fprint(w, deltaNar)

	out := status.String()
	if !strings.Contains(out, rule+" implementor ") {
		t.Errorf("missing implementor header for the land pass: %q", out)
	}
	if !strings.Contains(out, rule+" reviewer ") {
		t.Errorf("missing reviewer switch header after delta-review pass_start: %q", out)
	}
	if !strings.Contains(out, "Reviewing the delta.") {
		t.Errorf("missing delta-review narration: %q", out)
	}
	land := strings.Index(out, "Landing the change.")
	rev := strings.Index(out, rule+" reviewer ")
	delta := strings.Index(out, "Reviewing the delta.")
	if land >= rev || rev >= delta {
		t.Errorf("reviewer header must sit between the land and delta-review narration: %q", out)
	}
}

// A pass_start with no Role, the legacy single-loop dispatch shape, leaves the
// active top-level role unchanged: a later top-level turn is still attributed
// to implementor, exactly as if the pass_start were absent (issue #2382).
func TestWriterPassStartEmptyRoleDoesNotChangeActiveRole(t *testing.T) {
	const rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	var status bytes.Buffer
	w := newHeartbeat(t, &bytes.Buffer{}, "2382", &status, driverkit.RenderOptions{})

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

// The nested hostop.PassUsage and Agents payload survives the same
// stream-json encode/decode seam every other op kind already does (issue
// #3156).
func TestEncodeSpindriftOpPassUsageRoundTrip(t *testing.T) {
	want := hostop.SpindriftOp{
		Op:   "pass_usage",
		Pass: 2,
		Role: "review",
		Usage: &hostop.PassUsage{
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
	line := hostop.EncodeSpindriftOp(want)

	var ev struct {
		SpindriftOp *hostop.SpindriftOp `json:"spindrift_op"`
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
	want := hostop.SpindriftOp{
		Op:   "land_delta",
		Pass: 5,
		Delta: &landdelta.Delta{
			Known:      true,
			Files:      2,
			Insertions: 41,
			Deletions:  3,
		},
	}
	line := hostop.EncodeSpindriftOp(want)

	var ev struct {
		SpindriftOp *hostop.SpindriftOp `json:"spindrift_op"`
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

// The signal op's kind, size, hash and decision survive the same stream-json
// encode/decode seam every other op kind already does (issue #3724).
func TestEncodeSpindriftOpSignalRoundTrip(t *testing.T) {
	want := hostop.SpindriftOp{
		Op:       "signal",
		Kind:     "pr-intent",
		Size:     412,
		Hash:     "sha256:9f2c",
		Decision: "accept",
	}
	line := hostop.EncodeSpindriftOp(want)

	for _, key := range []string{`"kind":"pr-intent"`, `"size":412`, `"hash":"sha256:9f2c"`, `"decision":"accept"`} {
		if !strings.Contains(line, key) {
			t.Errorf("EncodeSpindriftOp = %q, want it to contain %s", line, key)
		}
	}

	var ev struct {
		SpindriftOp *hostop.SpindriftOp `json:"spindrift_op"`
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

// OnModel fires once per change of the (role, exact model id) pair behind the
// streamed assistant messages, never per message.
func TestWriterOnModelFiresOnPairChangeOnly(t *testing.T) {
	type call struct{ model, role string }
	var got []call
	w := newHeartbeat(t, &bytes.Buffer{}, "4744", &bytes.Buffer{}, driverkit.RenderOptions{OnModel: func(model, role string) {
		got = append(got, call{model, role})
	}})

	top := func(model, text string) string {
		return `{"type":"assistant","message":{"model":"` + model + `","content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
	}
	spawn := `{"type":"assistant","message":{"model":"claude-sonnet-5-5","content":[{"type":"tool_use","name":"Task","id":"tu_w","input":{"subagent_type":"scout"}}]}}` + "\n"
	sub := func(model string) string {
		return `{"type":"assistant","parent_tool_use_id":"tu_w","message":{"model":"` + model + `","content":[{"type":"tool_use","name":"Read","id":"r","input":{}}]}}` + "\n"
	}
	for _, line := range []string{
		top("claude-sonnet-5-5", "one"),
		spawn,
		top("claude-sonnet-5-5", "two"),
		sub("claude-haiku-4-5"),
		sub("claude-haiku-4-5"),
		top("claude-sonnet-5-5", "three"),
		top("claude-sonnet-5-5", "four"),
		`{"type":"assistant","message":{"content":[{"type":"text","text":"no model"}]}}` + "\n",
		`{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"limit"}]},"error":"rate_limit"}` + "\n",
		top("claude-sonnet-5-5", "five"),
	} {
		fmt.Fprint(w, line)
	}

	want := []call{
		{"claude-sonnet-5-5", driverkit.ImplementorRole},
		{"claude-haiku-4-5", "scout"},
		{"claude-sonnet-5-5", driverkit.ImplementorRole},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OnModel calls = %v, want %v", got, want)
	}
}

// A non-string top-level "error" must not demote an assistant line to a bare
// KindError: the header, narration, tool count and OnModel still fire (issue #5453).
func TestWriterObjectErrorAssistantLineKeepsHeartbeat(t *testing.T) {
	var status bytes.Buffer
	var models []string
	w := newHeartbeat(t, &bytes.Buffer{}, "4744", &status, driverkit.RenderOptions{OnModel: func(model, _ string) {
		models = append(models, model)
	}})

	fmt.Fprint(w, `{"type":"assistant","error":{"x":1},"message":{"model":"claude-opus-4","content":[{"type":"text","text":"reading it"},{"type":"tool_use","name":"Read","id":"r","input":{"file_path":"a.go"}}]}}`+"\n")
	fmt.Fprint(w, `{"type":"result","num_turns":1}`+"\n")

	out := status.String()
	for _, want := range []string{"#4744", driverkit.ImplementorRole, "reading it", "1 read"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
	if !reflect.DeepEqual(models, []string{"claude-opus-4"}) {
		t.Errorf("OnModel models = %v, want [claude-opus-4]", models)
	}
}

// An assistant line whose content is a string, or that holds a block failing
// to unmarshal, still yields its model and valid blocks (issue #5454); the
// result must not depend on how the log is split across Writes.
func TestWriterLenientAssistantContentKeepsHeartbeat(t *testing.T) {
	type call struct{ model, role string }
	const rule = "\xe2\x94\x80\xe2\x94\x80" // ──
	for _, tc := range []struct {
		name, line, exact string
		want, absent      []string
	}{
		{
			name: "string_content",
			line: `{"type":"assistant","message":{"id":"m1","model":"claude-opus-4-1","content":"hello there"}}`,
			// No role header and no narration: only the result line.
			exact: "#5454 \xc2\xb7 1 turn\n",
		},
		{
			name: "malformed_block",
			line: `{"type":"assistant","message":{"id":"m1","model":"claude-opus-4-1","content":[{"type":"text","text":5},{"type":"text","text":"I will read the file"},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/x"}}]}}`,
			want: []string{
				"#5454 " + rule + " " + driverkit.ImplementorRole + " \xc2\xb7 opus ",
				"#5454 \xc2\xb7 I will read the file\n",
				"#5454 [explore] \xc2\xb7 1 read\n",
			},
			absent: []string{"#5454 \xc2\xb7 5\n"},
		},
	} {
		log := tc.line + "\n" + `{"type":"result","num_turns":1}` + "\n"
		for _, size := range []int{1, 7, len(log)} {
			t.Run(fmt.Sprintf("%s/chunk_%d", tc.name, size), func(t *testing.T) {
				var status bytes.Buffer
				var got []call
				w := newHeartbeat(t, &bytes.Buffer{}, "5454", &status, driverkit.RenderOptions{OnModel: func(model, role string) {
					got = append(got, call{model, role})
				}})
				for rest := log; rest != ""; {
					n := min(size, len(rest))
					if _, err := io.WriteString(w, rest[:n]); err != nil {
						t.Fatal(err)
					}
					rest = rest[n:]
				}
				out := status.String()
				if tc.exact != "" && out != tc.exact {
					t.Errorf("output = %q, want %q", out, tc.exact)
				}
				for _, want := range tc.want {
					if !strings.Contains(out, want) {
						t.Errorf("output missing %q: %q", want, out)
					}
				}
				for _, absent := range tc.absent {
					if strings.Contains(out, absent) {
						t.Errorf("output must not contain %q: %q", absent, out)
					}
				}
				if want := []call{{"claude-opus-4-1", driverkit.ImplementorRole}}; !reflect.DeepEqual(got, want) {
					t.Errorf("OnModel calls = %v, want %v", got, want)
				}
			})
		}
	}
}
