package claude

import (
	"encoding/json"
	"strings"
)

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
