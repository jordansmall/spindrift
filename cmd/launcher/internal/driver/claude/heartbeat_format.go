package claude

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// sanitizeRole strips C0/C1 control characters, ANSI CSI/OSC escape
// sequences, and newlines/tabs from role before it is interpolated into a
// heartbeat line. role is agent-controlled (it traces back to the Task
// tool's subagent_type input) and a heartbeat row has no legitimate
// embedded newline or escape sequence — unlike the transcript pane, every
// heartbeat line must stay single-line, so nothing here is preserved.
func sanitizeRole(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == 0x1b:
			i += size
			if i >= len(s) {
				continue
			}
			switch s[i] {
			case '[':
				i++
				for i < len(s) && !(s[i] >= 0x40 && s[i] <= 0x7e) {
					i++
				}
				if i < len(s) {
					i++
				}
			case ']':
				i++
				for i < len(s) && s[i] != 0x07 {
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
				if i < len(s) && s[i] == 0x07 {
					i++
				}
			}
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			i += size
		default:
			b.WriteRune(r)
			i += size
		}
	}
	return b.String()
}

// FormatRoleHeader returns a switch-header line for the acting role.
// When model is non-empty, appends "· <model>" after the role.
// Example: "#284 ── implementor · opus ──────────"
// Example: "#284 ── scout ──────────────────────"
func FormatRoleHeader(issue, role, model string) string {
	const targetWidth = 36
	const minTrail = 4
	label := sanitizeRole(role)
	if model != "" {
		label = label + " \xc2\xb7 " + model
	}
	prefix := "#" + issue + " \xe2\x94\x80\xe2\x94\x80 " + label + " "
	trail := targetWidth - len([]rune(prefix))
	if trail < minTrail {
		trail = minTrail
	}
	return prefix + strings.Repeat("\xe2\x94\x80", trail)
}

// FormatHeartbeat returns a coarse status line for one running issue. When
// role is non-empty and not the implementor, it is named right after the
// issue tag so the line is never mistaken for implementor output.
// Example: "#42 [edit] · 15 turns · Edit(main.go)"
// Example: "#42 scout [plan] · 3 turns"
func FormatHeartbeat(issue string, turns int, lastTool, role, phase string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%s", issue)
	if role != "" && role != ImplementorRole {
		fmt.Fprintf(&sb, " %s", sanitizeRole(role))
	}
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
// When role is non-empty and not the implementor, it is named right after
// the issue tag so the line is never mistaken for implementor output.
// Example: "#42 [explore] · 3 reads, 2 greps"
// Example: "#42 scout [explore] · 1 read"
func FormatCountLine(issue, role, phase string, counts map[string]int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%s", issue)
	if role != "" && role != ImplementorRole {
		fmt.Fprintf(&sb, " %s", sanitizeRole(role))
	}
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
		var ti TaskInput
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
