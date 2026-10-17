package claude

import (
	"encoding/json"
	"strings"
)

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

// toolTarget picks the input field that best identifies what a tool call acted
// on. An unrecognized tool or missing field yields "", which the renderer
// shows as a bare "Name()".
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
