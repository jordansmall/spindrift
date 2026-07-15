package claude

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/logscan"
)

// RenderTranscript scans the box log at logPath and returns a readable
// rendering of its assistant turns and tool calls — the claude Driver's
// transcript-rendering strategy (ADR 0009), used by a Console drill-in to
// show the work instead of raw stream-json.
//
// Returns ("", nil) when logPath does not exist, matching lastInLog and
// breakdownByRole's not-found contract.
func RenderTranscript(logPath string) (string, error) {
	var lines []string
	taskRole := make(map[string]string)
	err := logscan.ForEachLine(logPath, logscan.SkipOversized, func(line string) {
		s := strings.TrimSpace(line)
		if s == "" {
			return
		}
		var ev Event
		if jsonErr := json.Unmarshal([]byte(s), &ev); jsonErr != nil {
			return
		}
		if ev.Message == nil {
			return
		}
		switch ev.Type {
		case "assistant":
			CollectTaskRoles(ev, taskRole)
			role := ResolveRole(ev, taskRole)
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
			role := ResolveRole(ev, taskRole)
			for _, block := range ev.Message.Content {
				if block.Type != "tool_result" {
					continue
				}
				lines = append(lines, "["+role+"]   -> "+summarizeResult(block))
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

// resultTextMaxLen caps a rendered tool_result summary so one noisy tool
// call (a huge file read, a long command's stdout) can't dominate the
// transcript.
const resultTextMaxLen = 200

// summarizeResult renders a tool_result block's content as a single-line
// summary, capped at resultTextMaxLen runes and prefixed "error: " when the
// block is IsError.
func summarizeResult(block ContentBlock) string {
	text := strings.TrimSpace(strings.ReplaceAll(resultText(block.Content), "\n", " "))
	if len(text) > resultTextMaxLen {
		text = text[:resultTextMaxLen-3] + "..."
	}
	if text == "" {
		text = "(empty result)"
	}
	if block.IsError {
		return "error: " + text
	}
	return text
}

// resultText decodes a tool_result block's content field, which the Claude
// API allows to be either a bare string or an array of {"type":"text",...}
// blocks — joining any text blocks found, space-separated.
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

// formatToolUse returns a tool call rendered as "Name(target)", where target
// is the tool's most identifying input field (file path, command, pattern,
// URL, ...), empty for a tool this function does not special-case.
func formatToolUse(name string, input json.RawMessage) string {
	return name + "(" + toolTarget(name, input) + ")"
}

// toolTarget extracts the input field that best identifies what a tool call
// acted on, per tool name. Returns "" for an unrecognized tool or missing
// field, in which case formatToolUse renders a bare "Name()".
func toolTarget(name string, input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	field := ""
	switch name {
	case "Read", "Edit", "Write", "NotebookEdit":
		field = "file_path"
	case "Grep", "Glob":
		field = "pattern"
	case "Bash":
		field = "command"
	case "WebFetch":
		field = "url"
	case "WebSearch":
		field = "query"
	case "Task", "Agent":
		field = "subagent_type"
	}
	if field == "" {
		return ""
	}
	if v, ok := m[field].(string); ok {
		return v
	}
	return ""
}
