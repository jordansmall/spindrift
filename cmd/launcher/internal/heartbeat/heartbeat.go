// Package heartbeat provides a streaming stream-json parser that emits
// per-issue status lines to the launcher terminal at natural event boundaries
// (narration, phase change, result) while forwarding all bytes to the raw log
// writer unchanged.
package heartbeat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

// Writer wraps a raw io.Writer (the log file) and emits heartbeat lines to
// out (the launcher terminal) at natural event boundaries. Every byte written
// to Writer is forwarded to raw unchanged; heartbeat emission is a side-effect.
type Writer struct {
	raw   io.Writer
	issue string
	out   io.Writer

	mu         sync.Mutex
	buf        []byte
	turns      int
	lastPhase  string
	toolCounts map[string]int // accumulated counts per tool kind since last narration/phase reset
}

// New returns a Writer that passes all bytes to raw unchanged and emits
// heartbeat lines to out at natural boundaries (narration, phase change, result).
func New(raw io.Writer, issue string, out io.Writer) *Writer {
	return &Writer{
		raw:        raw,
		issue:      issue,
		out:        out,
		toolCounts: make(map[string]int),
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
	switch ev.Type {
	case "assistant":
		if ev.Message != nil {
			// Subagent narration (parent_tool_use_id != "") is dropped: subagent
			// text is implementation detail of the spawning tool, not operator intent.
			if ev.ParentToolUseID == "" {
				// Narration: emit text line, then emit count line for accumulated tools.
				for _, block := range ev.Message.Content {
					if block.Type == "text" {
						if narration := trimNarration(block.Text); narration != "" {
							var line string
							if w.lastPhase != "" {
								line = "#" + w.issue + " [" + w.lastPhase + "] " + narration
							} else {
								line = "#" + w.issue + " \xc2\xb7 " + narration
							}
							fmt.Fprintln(w.out, line)
							if hasCounts(w.toolCounts) {
								fmt.Fprintln(w.out, FormatCountLine(w.issue, w.lastPhase, w.toolCounts))
								clearCounts(w.toolCounts)
							}
						}
						break
					}
				}
			}
			// Accumulate tool counts; emit count line on phase transition.
			for _, block := range ev.Message.Content {
				if block.Type == "tool_use" {
					phase := toolToPhase(block.Name, block.Input)
					if phase != w.lastPhase {
						if hasCounts(w.toolCounts) {
							fmt.Fprintln(w.out, FormatCountLine(w.issue, w.lastPhase, w.toolCounts))
							clearCounts(w.toolCounts)
						}
						w.lastPhase = phase
					}
					w.toolCounts[toolKind(block.Name)]++
					break
				}
			}
		}
	case "result":
		if ev.NumTurns > 0 {
			w.turns = ev.NumTurns
		}
		w.emit()
		return
	}
}

func (w *Writer) emit() {
	if hasCounts(w.toolCounts) {
		fmt.Fprintln(w.out, FormatCountLine(w.issue, w.lastPhase, w.toolCounts))
		clearCounts(w.toolCounts)
	}
	if w.turns > 0 {
		fmt.Fprintln(w.out, FormatHeartbeat(w.issue, w.turns, "", w.lastPhase))
	}
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

// FormatCountLine returns a count summary line for accumulated tool calls.
// Example: "#42 [explore] · 3 reads, 2 greps"
func FormatCountLine(issue, phase string, counts map[string]int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%s", issue)
	if phase != "" {
		fmt.Fprintf(&sb, " [%s]", phase)
	}
	fmt.Fprintf(&sb, " \xc2\xb7 %s", formatCounts(counts))
	return sb.String()
}

// toolKind maps a tool name to its human-readable count kind.
func toolKind(name string) string {
	switch name {
	case "Read":
		return "read"
	case "Edit", "Write", "NotebookEdit":
		return "edit"
	case "Grep", "Glob":
		return "grep"
	case "WebSearch", "WebFetch":
		return "search"
	case "Agent":
		return "subagent"
	default:
		return strings.ToLower(name)
	}
}

// formatCounts returns a comma-separated count string, e.g. "3 reads, 2 greps".
// Kinds are emitted in a fixed display order so output is deterministic.
func formatCounts(counts map[string]int) string {
	order := []string{"read", "edit", "grep", "search", "bash", "subagent"}
	seen := make(map[string]bool, len(order))
	var parts []string
	for _, kind := range order {
		if n := counts[kind]; n > 0 {
			seen[kind] = true
			parts = append(parts, fmt.Sprintf("%d %s", n, pluralKind(kind, n)))
		}
	}
	// Append any kinds not in the fixed order, sorted for determinism.
	var extra []string
	for kind := range counts {
		if !seen[kind] && counts[kind] > 0 {
			extra = append(extra, kind)
		}
	}
	sort.Strings(extra)
	for _, kind := range extra {
		n := counts[kind]
		parts = append(parts, fmt.Sprintf("%d %s", n, pluralKind(kind, n)))
	}
	return strings.Join(parts, ", ")
}

// pluralKind returns the plural form of a tool kind label for count n.
func pluralKind(kind string, n int) string {
	if n == 1 {
		return kind
	}
	switch kind {
	case "search":
		return "searches"
	default:
		return kind + "s"
	}
}

// hasCounts reports whether any tool kind has a non-zero count.
func hasCounts(counts map[string]int) bool {
	for _, n := range counts {
		if n > 0 {
			return true
		}
	}
	return false
}

// clearCounts resets all counts to zero by deleting every key.
func clearCounts(counts map[string]int) {
	for k := range counts {
		delete(counts, k)
	}
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
