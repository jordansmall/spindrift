// Package heartbeat provides a streaming stream-json parser that emits
// throttled per-issue status lines to the launcher terminal while forwarding
// all bytes to the raw log writer unchanged.
package heartbeat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// DefaultThrottle is the minimum interval between heartbeat lines when no
// tool change has been detected.
const DefaultThrottle = 5 * time.Second

// Writer wraps a raw io.Writer (the log file) and emits throttled heartbeat
// lines to out (the launcher terminal). Every byte written to Writer is
// forwarded to raw unchanged; heartbeat emission is a side-effect.
type Writer struct {
	raw      io.Writer
	issue    string
	out      io.Writer
	throttle time.Duration

	mu        sync.Mutex
	buf       []byte
	turns     int
	lastTool  string
	lastPhase string
	lastEmit  time.Time
}

// New returns a Writer that passes all bytes to raw unchanged and emits
// throttled heartbeat lines to out. throttle controls the minimum time
// between heartbeat emissions when no new tool is detected; use
// DefaultThrottle if unsure.
func New(raw io.Writer, issue string, out io.Writer, throttle time.Duration) *Writer {
	return &Writer{
		raw:      raw,
		issue:    issue,
		out:      out,
		throttle: throttle,
	}
}

// Write implements io.Writer. All bytes are forwarded to raw unchanged, then
// complete lines are parsed for heartbeat events.
func (w *Writer) Write(p []byte) (int, error) {
	n, err := w.raw.Write(p)
	if err != nil {
		return n, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p[:n]...)
	for {
		nl := bytes.IndexByte(w.buf, '\n')
		if nl < 0 {
			break
		}
		line := string(w.buf[:nl])
		w.buf = w.buf[nl+1:]
		w.parseLine(line)
	}
	return n, nil
}

type streamEvent struct {
	Type            string        `json:"type"`
	Message         *messageBlock `json:"message,omitempty"`
	NumTurns        int           `json:"num_turns,omitempty"`
	ParentToolUseID string        `json:"parent_tool_use_id,omitempty"`
}

type messageBlock struct {
	Content []contentBlock `json:"content"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	Text  string          `json:"text,omitempty"`
}

func (w *Writer) parseLine(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return
	}
	changed := false
	switch ev.Type {
	case "assistant":
		if ev.Message != nil {
			// Subagent narration (parent_tool_use_id != "") is dropped: subagent
			// text is implementation detail of the spawning tool, not operator intent.
			if ev.ParentToolUseID == "" {
				// Emit narration before tool line when both are present.
				for _, block := range ev.Message.Content {
					if block.Type == "text" {
						if narration := trimNarration(block.Text); narration != "" {
							fmt.Fprintln(w.out, "#"+w.issue+" \xc2\xb7 "+narration)
							w.lastEmit = time.Now()
						}
						break
					}
				}
			}
			for _, block := range ev.Message.Content {
				if block.Type == "tool_use" {
					tool := formatTool(block.Name, block.Input)
					if tool != w.lastTool {
						w.lastTool = tool
						changed = true
					}
					// Use first tool_use block per assistant event.
					break
				}
			}
		}
		if changed {
			w.emit()
			return
		}
	case "result":
		if ev.NumTurns > 0 {
			w.turns = ev.NumTurns
		}
		w.emit()
		return
	}
	// Time-based fallback: emit if throttle duration has elapsed and we have state.
	if (w.lastTool != "" || w.turns > 0) && time.Since(w.lastEmit) >= w.throttle {
		w.emit()
	}
}

func (w *Writer) emit() {
	fmt.Fprintln(w.out, FormatHeartbeat(w.issue, w.turns, w.lastTool, w.lastPhase))
	w.lastEmit = time.Now()
}

// FormatHeartbeat returns a coarse status line for one running issue.
// Example: "#42 [edit] · 15 turns · Edit(main.go)"
func FormatHeartbeat(issue string, turns int, lastTool, phase string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%s", issue)
	if phase != "" {
		fmt.Fprintf(&sb, " [%s]", phase)
	}
	if turns > 0 {
		plural := "s"
		if turns == 1 {
			plural = ""
		}
		fmt.Fprintf(&sb, " \xc2\xb7 %d turn%s", turns, plural)
	}
	if lastTool != "" {
		fmt.Fprintf(&sb, " \xc2\xb7 %s", lastTool)
	}
	return sb.String()
}

// trimNarration returns the first sentence of text, capped at 120 characters, with
// leading/trailing whitespace removed. Returns "" for empty or whitespace-only input.
// Subagent text (parent_tool_use_id != "") is handled by the caller — this function
// only trims; it does not decide whether to emit.
func trimNarration(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// Trim to first sentence boundary or newline.
	if i := strings.IndexAny(text, ".!?\n"); i >= 0 {
		text = strings.TrimSpace(text[:i+1])
		// Strip trailing newline that was the boundary character.
		if len(text) > 0 && text[len(text)-1] == '\n' {
			text = strings.TrimSpace(text[:len(text)-1])
		}
	}
	if len(text) > 120 {
		text = text[:117] + "..."
	}
	return text
}

// toolToPhase maps a tool name and its input to the current work phase.
// The mapping is the single authoritative place for phase heuristics.
func toolToPhase(name string, input json.RawMessage) string {
	switch name {
	case "Edit", "Write", "NotebookEdit":
		return "edit"
	case "Bash":
		var m map[string]interface{}
		if len(input) > 0 {
			if err := json.Unmarshal(input, &m); err == nil {
				if cmd, ok := m["command"].(string); ok {
					if strings.Contains(cmd, "go test") {
						return "test"
					}
					if strings.Contains(cmd, "git commit") {
						return "commit"
					}
				}
			}
		}
		return "explore"
	default:
		return "explore"
	}
}

// formatTool returns a compact label for a tool_use block, e.g. "Edit(main.go)".
// It checks well-known input keys in priority order; falls back to the tool
// name alone when no string argument can be extracted.
func formatTool(name string, input json.RawMessage) string {
	if len(input) == 0 {
		return name
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return name
	}
	for _, key := range []string{"file_path", "command", "path", "query"} {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				if len(s) > 40 {
					s = s[:37] + "..."
				}
				return name + "(" + s + ")"
			}
		}
	}
	return name
}
