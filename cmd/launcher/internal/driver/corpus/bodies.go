package corpus

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

// Placeholder names for blanked JSON bodies. No pattern can recognise repo
// content, so these are Scrub-only: the guard never looks for them.
const (
	toolOutput = "tool-output"
	fileBody   = "file-body"
	agentText  = "agent-text"
	inlineCode = "inline-code"
)

var bodyNames = []string{toolOutput, fileBody, agentText, inlineCode}

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
// heredoc bodies in tool commands, thinking text and subagent task text, and
// opencode tool state output and metadata. A line that is not one JSON object, or has nothing to blank,
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
			if cmd, ok := e.(string); ok && k == "command" {
				if out, n := blankHeredocs(cmd); n > 0 {
					x[k] = out
					r.counts[inlineCode] += n
					r.n += n
				}
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

// heredocOpener matches a here-document redirection and its tag. A "<<" that
// follows another "<" is a here-string, which openers skips.
var heredocOpener = regexp.MustCompile(`<<(-?)[ \t]*(?:'([^'\n]+)'|"([^"\n]+)"|\\?([A-Za-z_][A-Za-z0-9_]*))`)

type heredoc struct {
	tag   string
	strip bool // <<- also ends on a tab-indented tag
}

// blankHeredocs replaces each heredoc body in a shell command with one
// placeholder line, keeping the opener and terminator, so the command still
// reads as what it ran (git commit, go test) without the source, file or
// message it fed in. It returns how many bodies it blanked. An unterminated
// body runs to the end of the command.
func blankHeredocs(cmd string) (string, int) {
	p := "[REDACTED:" + inlineCode + "]"
	lines := strings.SplitAfter(cmd, "\n")
	var out strings.Builder
	n := 0
	for i := 0; i < len(lines); i++ {
		out.WriteString(lines[i])
		for _, h := range openers(lines[i]) {
			end := i + 1
			for end < len(lines) && !h.ends(lines[end]) {
				end++
			}
			body := lines[i+1 : end]
			if len(body) == 0 || len(body) == 1 && strings.TrimSuffix(body[0], "\n") == p {
				out.WriteString(strings.Join(body, ""))
			} else {
				out.WriteString(p)
				if end < len(lines) {
					out.WriteString("\n")
				}
				n++
			}
			if end < len(lines) {
				out.WriteString(lines[end])
			}
			i = end
		}
	}
	return out.String(), n
}

func openers(line string) []heredoc {
	var hs []heredoc
	for _, m := range heredocOpener.FindAllStringSubmatchIndex(line, -1) {
		if m[0] > 0 && line[m[0]-1] == '<' {
			continue
		}
		h := heredoc{strip: m[3] > m[2]}
		for g := 2; g <= 4; g++ {
			if m[2*g] >= 0 {
				h.tag = line[m[2*g]:m[2*g+1]]
			}
		}
		hs = append(hs, h)
	}
	return hs
}

func (h heredoc) ends(line string) bool {
	line = strings.TrimSuffix(line, "\n")
	if h.strip {
		line = strings.TrimLeft(line, "\t")
	}
	return line == h.tag
}
