package corpus

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// decode parses one scrubbed JSON line, failing the test if Scrub broke it.
func decode(t *testing.T, line []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(bytes.TrimSuffix(line, []byte("\n")), &v); err != nil {
		t.Fatalf("Scrub produced invalid JSON %q: %v", line, err)
	}
	return v
}

func at(v any, path ...any) any {
	for _, p := range path {
		switch k := p.(type) {
		case string:
			v = v.(map[string]any)[k]
		case int:
			v = v.([]any)[k]
		}
	}
	return v
}

func TestScrubBlanksClaudeToolResults(t *testing.T) {
	in := `{"type":"user","message":{"role":"user","content":[` +
		`{"type":"tool_result","tool_use_id":"toolu_1","content":"package acme // private source","is_error":true},` +
		`{"type":"tool_result","tool_use_id":"toolu_2","content":[{"type":"text","text":"secret diff"}]}]},` +
		`"tool_use_result":{"stdout":"private output","stderr":""}}` + "\n"
	got := decode(t, Scrub([]byte(in)))

	for i := range 2 {
		if c := at(got, "message", "content", i, "content"); c != "[REDACTED:tool-output]" {
			t.Errorf("tool_result %d content = %v, want the tool-output placeholder", i, c)
		}
	}
	if id := at(got, "message", "content", 0, "tool_use_id"); id != "toolu_1" {
		t.Errorf("tool_use_id = %v, want it kept", id)
	}
	if e := at(got, "message", "content", 0, "is_error"); e != true {
		t.Errorf("is_error = %v, want it kept", e)
	}
	if r := got["tool_use_result"]; r != "[REDACTED:tool-output]" {
		t.Errorf("tool_use_result = %v, want the tool-output placeholder", r)
	}
}

func TestScrubBlanksFileBodiesButKeepsWhatReadersUse(t *testing.T) {
	in := `{"type":"assistant","message":{"id":"msg_1","content":[` +
		`{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"a.go","old_string":"old private","new_string":"new private"}},` +
		`{"type":"tool_use","id":"t2","name":"Write","input":{"file_path":"b.go","content":"private body"}},` +
		`{"type":"tool_use","id":"t3","name":"Agent","input":{"subagent_type":"worker","description":"fix it","prompt":"private brief"}},` +
		`{"type":"tool_use","id":"t4","name":"Bash","input":{"command":"go test ./..."}},` +
		`{"type":"tool_use","id":"t5","name":"MultiEdit","input":{"file_path":"c.go","edits":[{"old_string":"x private","new_string":"y private"}]}},` +
		`{"type":"thinking","thinking":"private reasoning","signature":"sig"},` +
		`{"type":"text","text":"Done. SPINDRIFT_OUTCOME issue=1 status=success"}]}}` + "\n"
	out := Scrub([]byte(in))
	if bytes.Contains(out, []byte("private")) {
		t.Errorf("Scrub left private text: %s", out)
	}
	got := decode(t, out)
	keep := map[string]any{
		"file_path":     at(got, "message", "content", 0, "input", "file_path"),
		"subagent_type": at(got, "message", "content", 2, "input", "subagent_type"),
		"description":   at(got, "message", "content", 2, "input", "description"),
		"command":       at(got, "message", "content", 3, "input", "command"),
		"text":          at(got, "message", "content", 6, "text"),
	}
	want := map[string]any{
		"file_path":     "a.go",
		"subagent_type": "worker",
		"description":   "fix it",
		"command":       "go test ./...",
		"text":          "Done. SPINDRIFT_OUTCOME issue=1 status=success",
	}
	if !reflect.DeepEqual(keep, want) {
		t.Errorf("kept fields = %v, want %v", keep, want)
	}
	if b := at(got, "message", "content", 1, "input", "content"); b != "[REDACTED:file-body]" {
		t.Errorf("Write content = %v, want the file-body placeholder", b)
	}
	if b := at(got, "message", "content", 5, "thinking"); b != "[REDACTED:agent-text]" {
		t.Errorf("thinking = %v, want the agent-text placeholder", b)
	}
}

func TestScrubBlanksSubagentTaskText(t *testing.T) {
	in := `{"type":"system","subtype":"task_started","task_id":"a1","description":"Review axis","prompt":"private brief"}` + "\n" +
		`{"type":"system","subtype":"task_notification","task_id":"a1","summary":"private summary","status":"completed"}` + "\n"
	out := Scrub([]byte(in))
	if bytes.Contains(out, []byte("private")) {
		t.Errorf("Scrub left private task text: %s", out)
	}
	lines := bytes.SplitAfter(out, []byte("\n"))
	if d := decode(t, lines[0])["description"]; d != "Review axis" {
		t.Errorf("task description = %v, want it kept", d)
	}
	if s := decode(t, lines[1])["status"]; s != "completed" {
		t.Errorf("task status = %v, want it kept", s)
	}
}

func TestScrubBlanksOpencodeToolParts(t *testing.T) {
	in := `{"type":"tool_use","part":{"type":"tool","tool":"edit","callID":"c1","state":{"status":"completed",` +
		`"input":{"filePath":"a.go","oldString":"old private","newString":"new private"},` +
		`"output":"private output","metadata":{"diff":"private diff"},"title":"a.go"}}}` + "\n"
	out := Scrub([]byte(in))
	if bytes.Contains(out, []byte("private")) {
		t.Errorf("Scrub left private text: %s", out)
	}
	got := decode(t, out)
	if p := at(got, "part", "state", "input", "filePath"); p != "a.go" {
		t.Errorf("filePath = %v, want it kept", p)
	}
	if o := at(got, "part", "state", "output"); o != "[REDACTED:tool-output]" {
		t.Errorf("output = %v, want the tool-output placeholder", o)
	}
}

func TestBlankHeredocs(t *testing.T) {
	const p = "[REDACTED:inline-code]"
	cases := []struct {
		name, in, want string
		wantN          int
	}{
		{"quoted tag", "cat > a_test.go <<'EOF'\npackage private\nfunc f() {}\nEOF\ngo test ./...",
			"cat > a_test.go <<'EOF'\n" + p + "\nEOF\ngo test ./...", 1},
		{"double-quoted tag", "python3 - <<\"PY\"\nprivate()\nPY", "python3 - <<\"PY\"\n" + p + "\nPY", 1},
		{"bare tag", "git commit -F - <<MSG\nprivate msg\nMSG\n", "git commit -F - <<MSG\n" + p + "\nMSG\n", 1},
		{"dash strips tabs", "cat <<-EOF\n\tprivate\n\tEOF\necho ok", "cat <<-EOF\n" + p + "\n\tEOF\necho ok", 1},
		{"two on one line", "paste <<A <<'B'\nprivate a\nA\nprivate b\nB", "paste <<A <<'B'\n" + p + "\nA\n" + p + "\nB", 2},
		{"tag text inside body", "cat <<EOF\nnot EOF yet\nEOF", "cat <<EOF\n" + p + "\nEOF", 1},
		{"unterminated", "cat <<EOF\nprivate\nmore private", "cat <<EOF\n" + p, 1},
		{"shift-like openers with no body", `python3 -c "print(1 << a << b)"`, `python3 -c "print(1 << a << b)"`, 0},
		{"unterminated first of two", "cat <<A <<B\nprivate", "cat <<A <<B\n" + p, 1},
		{"empty body", "cat <<EOF\nEOF", "cat <<EOF\nEOF", 0},
		{"here-string", "grep x <<<\"$s\"", "grep x <<<\"$s\"", 0},
		{"shift", "echo $((1<<3))", "echo $((1<<3))", 0},
		{"no heredoc", "go test ./...", "go test ./...", 0},
		{"already blanked", "cat <<EOF\n" + p + "\nEOF", "cat <<EOF\n" + p + "\nEOF", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, n := blankHeredocs(c.in)
			if got != c.want || n != c.wantN {
				t.Errorf("blankHeredocs(%q)\n got: %q, %d\nwant: %q, %d", c.in, got, n, c.want, c.wantN)
			}
		})
	}
}

func TestScrubBlanksInlineCodeInCommands(t *testing.T) {
	in := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"cat > a.go <<'EOF'\npackage private\nEOF\ngit commit -q","description":"write file"}}]}}` + "\n" +
		`{"type":"tool_use","part":{"type":"tool","tool":"bash","callID":"c1","state":{"status":"completed","input":{"command":"python3 - <<'PY'\nprivate()\nPY"}}}}` + "\n"
	out := Scrub([]byte(in))
	if bytes.Contains(out, []byte("private")) {
		t.Errorf("Scrub left inline code: %s", out)
	}
	lines := bytes.SplitAfter(out, []byte("\n"))
	if c := at(decode(t, lines[0]), "message", "content", 0, "input", "command"); c != "cat > a.go <<'EOF'\n[REDACTED:inline-code]\nEOF\ngit commit -q" {
		t.Errorf("claude command = %q, want the heredoc body blanked and the rest kept", c)
	}
	if c := at(decode(t, lines[1]), "part", "state", "input", "command"); c != "python3 - <<'PY'\n[REDACTED:inline-code]\nPY" {
		t.Errorf("opencode command = %q, want the heredoc body blanked", c)
	}
	if got := Tally([]byte(in))[inlineCode]; got != 2 {
		t.Errorf("Tally[%s] = %d, want 2", inlineCode, got)
	}
	if twice := Scrub(out); !bytes.Equal(twice, out) {
		t.Errorf("Scrub is not idempotent over inline code:\nfirst:  %q\nsecond: %q", out, twice)
	}
}

func TestScrubLeavesLinesWithoutBodiesByteIdentical(t *testing.T) {
	in := strings.Join([]string{
		`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":1,"role":"implement"}}`,
		`{"type":"result","result":"SPINDRIFT_OUTCOME issue=1 status=success","total_cost_usd":0.123456789,"duration_ms":12345678901234567}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"a <b> & c"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"","signature":"sig"}]}}`,
		`==> claude implement`,
		`not json {`,
		``,
	}, "\n")
	if got := Scrub([]byte(in)); string(got) != in {
		t.Errorf("Scrub altered lines with nothing to blank:\ngot:  %q\nwant: %q", got, in)
	}
}

func TestScrubKeepsNumbersAndTextOnBlankedLines(t *testing.T) {
	in := `{"type":"user","timestamp":"2026-10-12T17:10:19.188Z","n":12345678901234567890,"f":0.1,"message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]},"note":"a <b> & c"}` + "\n"
	out := Scrub([]byte(in))
	for _, want := range []string{`12345678901234567890`, `0.1`, `"a <b> & c"`, `"2026-10-12T17:10:19.188Z"`} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("Scrub(%s) = %s, lost %s", in, out, want)
		}
	}
	if !bytes.HasSuffix(out, []byte("}\n")) || bytes.Count(out, []byte("\n")) != 1 {
		t.Errorf("Scrub changed the line framing: %q", out)
	}
}

func TestScrubOfBodiesIsIdempotent(t *testing.T) {
	in := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]},"tool_use_result":{"stdout":"y"}}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"a","content":"z"}}]}}` + "\n"
	once := Scrub([]byte(in))
	if twice := Scrub(once); !bytes.Equal(twice, once) {
		t.Errorf("Scrub is not idempotent:\nfirst:  %q\nsecond: %q", once, twice)
	}
}

func TestBodyPlaceholdersAreDistinctAndPatternSafe(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range patterns {
		seen[p.name] = true
	}
	for _, name := range bodyNames {
		if seen[name] {
			t.Errorf("body placeholder %q reuses a pattern name", name)
		}
		seen[name] = true
		for _, p := range patterns {
			if p.re.Find([]byte("[REDACTED:"+name+"]")) != nil {
				t.Errorf("pattern %q matches the %q placeholder", p.name, name)
			}
		}
	}
}

func TestTallyCountsPatternsAndBodies(t *testing.T) {
	in := `{"type":"user","author":"someone@example.com","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]}}` + "\n"
	want := map[string]int{"email": 1, "tool-output": 1}
	if got := Tally([]byte(in)); !reflect.DeepEqual(got, want) {
		t.Errorf("Tally = %v, want %v", got, want)
	}
	if got := Tally(Scrub([]byte(in))); len(got) != 0 {
		t.Errorf("Tally(Scrub(x)) = %v, want none", got)
	}
}
