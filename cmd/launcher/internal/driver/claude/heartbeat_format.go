package claude

import (
	"encoding/json"
	"strings"
)

// ModelFamily shortens a full model ID to its family label ("claude-opus-…"
// becomes "opus"), returning the id unchanged when no known family matches.
// The rank orders the final per-model table: opus, haiku, sonnet, then the
// rest (including "unknown"). That table stays keyed by exact id, so the rank
// only orders; within a rank, rows sort by raw id. The table lists families in
// match order, not rank order: the first family named in the id wins.
func ModelFamily(id string) (label string, rank int) {
	for _, f := range []struct {
		label string
		rank  int
	}{{"haiku", 1}, {"sonnet", 2}, {"opus", 0}} {
		if strings.Contains(id, f.label) {
			return f.label, f.rank
		}
	}
	return id, 3
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

// toolToPhase maps a tool name and its input to the current work phase. It is
// the single authoritative place for phase heuristics.
func toolToPhase(name string, input json.RawMessage) string {
	if isSubagentSpawnTool(name) {
		switch strings.ToLower(subagentType(input)) {
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
