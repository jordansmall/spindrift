package corpus

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// Placeholder names for blanked JSON bodies. No pattern can recognise repo
// content, so these are Scrub-only: the guard never looks for them.
const (
	toolOutput = "tool-output"
	fileBody   = "file-body"
	agentText  = "agent-text"
)

var bodyNames = []string{toolOutput, fileBody, agentText}

// fileBodyKeys are the tool-input keys that carry file contents or a free-form
// brief. The readers key off file_path, command, pattern, url, query and
// subagent_type instead, so blanking these moves no golden but the Transcript's.
var fileBodyKeys = map[string]bool{
	"content":    true,
	"old_string": true,
	"new_string": true,
	"oldString":  true,
	"newString":  true,
	"new_source": true,
	"prompt":     true,
}

// taskTextKeys carry a subagent's brief, report or state patch on claude's
// system task_* events.
var taskTextKeys = []string{"prompt", "summary", "patch"}

// redactBodies blanks, line by line, the JSON values that hold repo content:
// claude tool_result content and tool_use_result, file-body tool inputs,
// thinking text and subagent task text, and opencode tool state output and
// metadata. A line that is not one JSON object, or has nothing to blank,
// passes through byte for byte; a blanked line is re-encoded compactly, with
// sorted keys and numbers kept verbatim.
func redactBodies(data []byte) ([]byte, map[string]int) {
	counts := map[string]int{}
	var out []byte
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		body := bytes.TrimSuffix(line, []byte("\n"))
		if blanked, ok := redactLine(body, counts); ok {
			out = append(out, blanked...)
			out = append(out, line[len(body):]...)
			continue
		}
		out = append(out, line...)
	}
	return out, counts
}

func redactLine(line []byte, counts map[string]int) ([]byte, bool) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var ev map[string]any
	if dec.Decode(&ev) != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	r := redactor{counts: counts}
	r.event(ev)
	if r.n == 0 {
		return nil, false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(ev) != nil {
		return nil, false
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), true
}

type redactor struct {
	counts map[string]int
	n      int
}

func (r *redactor) event(ev map[string]any) {
	r.blank(ev, "tool_use_result", toolOutput)
	if sub, _ := ev["subtype"].(string); ev["type"] == "system" && strings.HasPrefix(sub, "task_") {
		for _, k := range taskTextKeys {
			r.blank(ev, k, agentText)
		}
	}
	r.walk(ev)
}

func (r *redactor) walk(v any) {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			r.walk(e)
		}
	case map[string]any:
		switch x["type"] {
		case "tool_result":
			r.blank(x, "content", toolOutput)
		case "tool_use":
			r.inputs(x["input"])
		case "thinking":
			r.blank(x, "thinking", agentText)
		case "tool": // an opencode tool part
			if st, ok := x["state"].(map[string]any); ok {
				r.blank(st, "output", toolOutput)
				r.blank(st, "metadata", toolOutput)
				r.inputs(st["input"])
			}
		}
		for _, e := range x {
			r.walk(e)
		}
	}
}

// inputs blanks file-body keys at any depth of a tool input, so MultiEdit's
// edits array is covered too.
func (r *redactor) inputs(v any) {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			r.inputs(e)
		}
	case map[string]any:
		for k, e := range x {
			if fileBodyKeys[k] {
				r.blank(x, k, fileBody)
				continue
			}
			r.inputs(e)
		}
	}
}

// blank replaces m[key] with name's placeholder unless it is absent, null,
// empty, or already that placeholder, which keeps Scrub idempotent.
func (r *redactor) blank(m map[string]any, key, name string) {
	v, ok := m[key]
	if !ok || v == nil {
		return
	}
	p := "[REDACTED:" + name + "]"
	if s, isStr := v.(string); isStr && (s == "" || s == p) {
		return
	}
	m[key] = p
	r.counts[name]++
	r.n++
}
