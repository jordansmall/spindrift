package claude

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/logscan"
)

// RenderTranscript renders the box log at logPath as readable assistant turns
// and tool calls (ADR 0009). It returns ("", nil) when logPath does not exist,
// matching sumInLog and breakdownByModelFile's not-found contract.
func RenderTranscript(logPath string) (string, error) {
	return RenderTranscriptWithRole(logPath, "")
}

// RenderTranscriptWithRole attributes every top-level (empty
// parent_tool_use_id) event to topLevelRole, so a pass the orchestrator owns
// renders as something other than implementation (issue #2092). An empty
// topLevelRole keeps the ImplementorRole default.
func RenderTranscriptWithRole(logPath, topLevelRole string) (string, error) {
	var lines []string
	taskRole := make(map[string]string)
	activeTopLevelRole := topLevelRole
	err := logscan.ForEachLine(logPath, logscan.SkipOversized, func(line string) {
		s := strings.TrimSpace(line)
		if s == "" {
			return
		}
		var ev Event
		if jsonErr := json.Unmarshal([]byte(s), &ev); jsonErr != nil {
			return
		}
		if ev.Type == "spindrift_op" {
			activeTopLevelRole = nextActiveTopLevelRole(activeTopLevelRole, ev.SpindriftOp)
			return
		}
		if ev.Message == nil {
			return
		}
		switch ev.Type {
		case "assistant":
			CollectTaskRoles(ev, taskRole)
			role := ResolveRole(ev, taskRole, activeTopLevelRole)
			for _, block := range ev.Message.Content {
				switch block.Type {
				case "text":
					if text := strings.TrimSpace(block.Text); text != "" {
						lines = append(lines, "["+role+"] "+text)
					}
				case "tool_use":
					lines = append(lines, "["+role+"] "+formatToolUse(block.Name, block.Input))
				}
			}
		case "user":
			role := ResolveRole(ev, taskRole, activeTopLevelRole)
			for _, block := range ev.Message.Content {
				if block.Type != "tool_result" {
					continue
				}
				summary := summarizeResult(block)
				// Key on block.ToolUseID, not the event's own
				// parent_tool_use_id used for role above: a subagent's final
				// report answers its own spawn ID, which taskRole records.
				// Any other tool_result misses the map and renders unprefixed.
				if subagentRole, ok := taskRole[block.ToolUseID]; ok {
					summary = "[" + subagentRole + "] " + summary
				}
				lines = append(lines, "["+role+"]   -> "+summary)
			}
		}
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	if len(lines) == 0 {
		return "", nil
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// resultTextMaxLen keeps one noisy tool call (a huge file read, a long
// command's stdout) from dominating the transcript.
const resultTextMaxLen = 200

func summarizeResult(block ContentBlock) string {
	text := strings.TrimSpace(strings.ReplaceAll(resultText(block.Content), "\n", " "))
	if len(text) > resultTextMaxLen {
		if runes := []rune(text); len(runes) > resultTextMaxLen {
			text = string(runes[:resultTextMaxLen-3]) + "..."
		}
	}
	if text == "" {
		text = "(empty result)"
	}
	if block.IsError {
		return "error: " + text
	}
	return text
}

// resultText decodes a tool_result block's content field, which the Claude API
// allows to be either a bare string or an array of {"type":"text",...} blocks.
func resultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return string(raw)
}

func formatToolUse(name string, input json.RawMessage) string {
	return name + "(" + toolTarget(name, input) + ")"
}

// toolTarget picks the input field that best identifies what a tool call acted
// on. An unrecognized tool or missing field yields "", which formatToolUse
// renders as a bare "Name()".
func toolTarget(name string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	field := ""
	switch {
	case isSubagentSpawnTool(name):
		field = "subagent_type"
	case name == "Read", name == "Edit", name == "Write", name == "NotebookEdit":
		field = "file_path"
	case name == "Grep", name == "Glob":
		field = "pattern"
	case name == "Bash":
		field = "command"
	case name == "WebFetch":
		field = "url"
	case name == "WebSearch":
		field = "query"
	}
	if field == "" {
		return ""
	}
	if v, ok := m[field].(string); ok {
		return v
	}
	return ""
}
