package claude

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// FormatRoleHeader returns a switch-header line for the acting role, e.g.
// "#284 ── implementor · opus ──────────". A non-empty model follows the role.
func FormatRoleHeader(issue, role, model string) string {
	const targetWidth = 36
	const minTrail = 4
	label := driverkit.SanitizeLine(role)
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

// FormatHeartbeat returns a coarse status line for one running issue, e.g.
// "#42 scout [plan] · 3 turns". It names any role other than the implementor
// first so the line is never mistaken for implementor output.
func FormatHeartbeat(issue string, turns int, lastTool, role, phase string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%s", issue)
	if role != "" && role != ImplementorRole {
		fmt.Fprintf(&sb, " %s", driverkit.SanitizeLine(role))
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

// FormatCountLine returns a count summary line for accumulated tool calls,
// e.g. "#42 scout [explore] · 1 read". It names any role other than the
// implementor first so the line is never mistaken for implementor output.
func FormatCountLine(issue, role, phase string, counts map[string]int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "#%s", issue)
	if role != "" && role != ImplementorRole {
		fmt.Fprintf(&sb, " %s", driverkit.SanitizeLine(role))
	}
	if phase != "" {
		fmt.Fprintf(&sb, " [%s]", phase)
	}
	fmt.Fprintf(&sb, " \xc2\xb7 %s", formatCounts(counts))
	return sb.String()
}

// ModelFamily shortens a full model ID to its family label ("claude-opus-…"
// becomes "opus"), returning the id unchanged when no known family matches.
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

func toolKind(name string) string {
	if isSubagentSpawnTool(name) {
		return "subagent"
	}
	switch name {
	case "Read":
		return "read"
	case "Edit", "Write", "NotebookEdit":
		return "edit"
	case "Grep", "Glob":
		return "grep"
	case "WebSearch", "WebFetch":
		return "search"
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

// toolToPhase maps a tool name and its input to the current work phase. It is
// the single authoritative place for phase heuristics.
func toolToPhase(name string, input json.RawMessage) string {
	if isSubagentSpawnTool(name) {
		var ti TaskInput
		if len(input) > 0 {
			_ = json.Unmarshal(input, &ti)
		}
		switch strings.ToLower(ti.SubagentType) {
		case "reviewer", "review-axis":
			return "review"
		case "scout", "plan":
			return "plan"
		}
		return "explore"
	}
	switch name {
	case "Edit", "Write", "NotebookEdit":
		return "edit"
	case "Grep", "Glob", "WebSearch", "WebFetch":
		return "search"
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
