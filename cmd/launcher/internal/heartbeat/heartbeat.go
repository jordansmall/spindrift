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

	mu              sync.Mutex
	buf             []byte
	turns           int
	taskRole        map[string]string         // Task tool-use id → subagent role
	currentRole     string                    // role of the message being parsed
	currentModel    string                    // shortened model family of the current message
	lastHeader      string                    // role of last emitted switch header
	lastHeaderModel string                    // model of last emitted switch header
	roleCounts      map[string]map[string]int // tool counts per role
	rolePhase       map[string]string         // current phase per role
}

// New returns a Writer that passes all bytes to raw unchanged and emits
// heartbeat lines to out at natural boundaries (narration, phase change, result).
func New(raw io.Writer, issue string, out io.Writer) *Writer {
	return &Writer{
		raw:        raw,
		issue:      issue,
		out:        out,
		taskRole:   make(map[string]string),
		roleCounts: make(map[string]map[string]int),
		rolePhase:  make(map[string]string),
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
	Model   string         `json:"model,omitempty"`
}

type contentBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	Text  string          `json:"text,omitempty"`
}

type taskInput struct {
	SubagentType string `json:"subagent_type"`
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
			// Collect Task tool-use IDs → subagent role from implementor messages
			// (online, single-pass; mirrors usage.BreakdownByRole pass 1).
			if ev.ParentToolUseID == "" {
				for _, block := range ev.Message.Content {
					if block.Type == "tool_use" && block.Name == "Task" && block.ID != "" {
						var ti taskInput
						if len(block.Input) > 0 {
							_ = json.Unmarshal(block.Input, &ti)
						}
						role := ti.SubagentType
						if role == "" {
							role = "subagent"
						}
						w.taskRole[block.ID] = role
					}
				}
			}

			// Resolve acting role from parent_tool_use_id.
			role := "implementor"
			if ev.ParentToolUseID != "" {
				if r, ok := w.taskRole[ev.ParentToolUseID]; ok {
					role = r
				} else {
					role = "subagent"
				}
			}
			model := ModelFamily(ev.Message.Model)

			// On (role, model) change, flush the departing role's pending counts.
			if role != w.currentRole || model != w.currentModel {
				w.flushCounts(w.currentRole)
				w.currentRole = role
				w.currentModel = model
			}

			// Subagent narration (parent_tool_use_id != "") is dropped; only
			// implementor text is emitted.
			if ev.ParentToolUseID == "" {
				for _, block := range ev.Message.Content {
					if block.Type == "text" {
						if narration := trimNarration(block.Text); narration != "" {
							phase := w.rolePhase[w.currentRole]
							var narLine string
							if phase != "" {
								narLine = "#" + w.issue + " [" + phase + "] " + narration
							} else {
								narLine = "#" + w.issue + " \xc2\xb7 " + narration
							}
							w.ensureHeader()
							fmt.Fprintln(w.out, narLine)
							if w.hasCurrCounts() {
								fmt.Fprintln(w.out, FormatCountLine(w.issue, phase, w.currCounts()))
								clearCounts(w.currCounts())
							}
						}
						break
					}
				}
			}

			// Accumulate tool counts per role; emit count line on phase transition.
			for _, block := range ev.Message.Content {
				if block.Type == "tool_use" {
					phase := toolToPhase(block.Name, block.Input)
					currPhase := w.rolePhase[w.currentRole]
					if phase != currPhase {
						if w.hasCurrCounts() {
							w.ensureHeader()
							fmt.Fprintln(w.out, FormatCountLine(w.issue, currPhase, w.currCounts()))
							clearCounts(w.currCounts())
						}
						w.rolePhase[w.currentRole] = phase
					}
					if w.roleCounts[w.currentRole] == nil {
						w.roleCounts[w.currentRole] = make(map[string]int)
					}
					w.roleCounts[w.currentRole][toolKind(block.Name)]++
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
	if w.hasCurrCounts() {
		w.ensureHeader()
		fmt.Fprintln(w.out, FormatCountLine(w.issue, w.rolePhase[w.currentRole], w.currCounts()))
		clearCounts(w.currCounts())
	}
	if w.turns > 0 {
		fmt.Fprintln(w.out, FormatHeartbeat(w.issue, w.turns, "", w.rolePhase["implementor"]))
	}
}

// ensureHeader emits a switch header for currentRole if the last emitted header
// is for a different role. It is a no-op when the acting role header was already
// emitted and no intervening header was needed.
func (w *Writer) ensureHeader() {
	if w.currentRole != "" && (w.currentRole != w.lastHeader || w.currentModel != w.lastHeaderModel) {
		fmt.Fprintln(w.out, FormatRoleHeader(w.issue, w.currentRole, w.currentModel))
		w.lastHeader = w.currentRole
		w.lastHeaderModel = w.currentModel
	}
}

// flushCounts emits the pending count line for role, preceded by a switch
// header if needed. It is a no-op when role is empty or has no accumulated counts.
func (w *Writer) flushCounts(role string) {
	if role == "" {
		return
	}
	counts := w.roleCounts[role]
	if !hasCounts(counts) {
		return
	}
	if w.lastHeader != role || w.lastHeaderModel != w.currentModel {
		fmt.Fprintln(w.out, FormatRoleHeader(w.issue, role, w.currentModel))
		w.lastHeader = role
		w.lastHeaderModel = w.currentModel
	}
	fmt.Fprintln(w.out, FormatCountLine(w.issue, w.rolePhase[role], counts))
	clearCounts(counts)
}

func (w *Writer) hasCurrCounts() bool {
	return hasCounts(w.roleCounts[w.currentRole])
}

func (w *Writer) currCounts() map[string]int {
	return w.roleCounts[w.currentRole]
}

// FormatRoleHeader returns a switch-header line for the acting role.
// When model is non-empty, appends "· <model>" after the role.
// Example: "#284 ── implementor · opus ──────────"
// Example: "#284 ── scout ──────────────────────"
func FormatRoleHeader(issue, role, model string) string {
	const targetWidth = 36
	const minTrail = 4
	label := role
	if model != "" {
		label = role + " \xc2\xb7 " + model
	}
	prefix := "#" + issue + " \xe2\x94\x80\xe2\x94\x80 " + label + " "
	trail := targetWidth - len([]rune(prefix))
	if trail < minTrail {
		trail = minTrail
	}
	return prefix + strings.Repeat("\xe2\x94\x80", trail)
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

// ModelFamily shortens a full model ID to its family label.
// "claude-haiku-…" → "haiku", "claude-sonnet-…" → "sonnet", "claude-opus-…" → "opus".
// Returns the id unchanged if no known family matches; returns "" for empty input.
func ModelFamily(id string) string {
	if id == "" {
		return ""
	}
	for _, family := range []string{"haiku", "sonnet", "opus"} {
		if strings.Contains(id, family) {
			return family
		}
	}
	return id
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
	case "Grep", "Glob", "WebSearch", "WebFetch":
		return "search"
	case "Task", "Agent":
		var ti taskInput
		if len(input) > 0 {
			_ = json.Unmarshal(input, &ti)
		}
		switch strings.ToLower(ti.SubagentType) {
		case "reviewer":
			return "review"
		case "scout", "plan":
			return "plan"
		}
		return "explore"
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
					if strings.Contains(cmd, "git ") || strings.Contains(cmd, "gh pr") {
						return "git"
					}
				}
			}
		}
		return "explore"
	default:
		return "explore"
	}
}
