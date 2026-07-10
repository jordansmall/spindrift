// Package claudetranscript owns the claude CLI stream-json transcript shape
// (ADR 0009: claude-Driver knowledge, not generic) and the Task-ID-to-role
// resolution algorithm shared by every host-side consumer that walks a Box's
// transcript — currently internal/heartbeat and internal/usage. A future
// second Driver carries its own transcript package rather than inheriting
// this one.
package claudetranscript

import "encoding/json"

// Event is one line of a claude CLI stream-json transcript.
type Event struct {
	Type            string   `json:"type"`
	Message         *Message `json:"message,omitempty"`
	NumTurns        int      `json:"num_turns,omitempty"`
	ParentToolUseID string   `json:"parent_tool_use_id,omitempty"`
}

// Message is the "message" object of an assistant stream event. It is a
// union of every field a consumer needs: Model is heartbeat-only (narration
// headers), Usage is usage-only (token accounting) — neither consumer
// requires both to be populated.
type Message struct {
	Content []ContentBlock `json:"content"`
	Model   string         `json:"model,omitempty"`
	Usage   TokenUsage     `json:"usage"`
}

// ContentBlock is one block of an assistant message's content array.
type ContentBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	Text  string          `json:"text,omitempty"`
}

// TaskInput is the input payload of a Task tool-use block.
type TaskInput struct {
	SubagentType string `json:"subagent_type"`
}

// TokenUsage is the per-message token accounting embedded in assistant events.
type TokenUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// ImplementorRole is the role attributed to any message with no
// parent_tool_use_id — the main agent loop, as opposed to a Task subagent.
const ImplementorRole = "implementor"

// DefaultRole is the role attributed to a Task whose input carries no (or
// empty) subagent_type, and to any message whose parent_tool_use_id does not
// match a Task ID collected so far.
const DefaultRole = "subagent"

// CollectTaskRoles scans an implementor event (ParentToolUseID == "") for
// Task tool-use blocks and records each one's subagent role — from its
// subagent_type input field, defaulting to DefaultRole — into taskRole, keyed
// by the tool-use ID. Events with a non-empty ParentToolUseID are ignored:
// only the implementor issues Task calls.
func CollectTaskRoles(ev Event, taskRole map[string]string) {
	if ev.ParentToolUseID != "" || ev.Message == nil {
		return
	}
	for _, block := range ev.Message.Content {
		if block.Type != "tool_use" || block.Name != "Task" || block.ID == "" {
			continue
		}
		var ti TaskInput
		if len(block.Input) > 0 {
			_ = json.Unmarshal(block.Input, &ti)
		}
		role := ti.SubagentType
		if role == "" {
			role = DefaultRole
		}
		taskRole[block.ID] = role
	}
}

// ResolveRole returns the acting role for ev: ImplementorRole when it has no
// parent_tool_use_id, otherwise the role recorded in taskRole for its parent
// Task ID, defaulting to DefaultRole when the parent is unknown.
func ResolveRole(ev Event, taskRole map[string]string) string {
	if ev.ParentToolUseID == "" {
		return ImplementorRole
	}
	if role, ok := taskRole[ev.ParentToolUseID]; ok {
		return role
	}
	return DefaultRole
}
