package claude

import (
	"encoding/json"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
)

// Event is one line of a claude CLI stream-json transcript. A future second
// Driver carries its own transcript shape rather than inheriting this one
// (ADR 0009).
type Event struct {
	Type            string              `json:"type"`
	Message         *Message            `json:"message,omitempty"`
	NumTurns        int                 `json:"num_turns,omitempty"`
	ParentToolUseID string              `json:"parent_tool_use_id,omitempty"`
	SpindriftOp     *hostop.SpindriftOp `json:"spindrift_op,omitempty"`
}

// Message is the "message" object of an assistant stream event. It is the
// union of every consumer's fields, so no consumer sees all of them populated.
type Message struct {
	ID      string         `json:"id,omitempty"`
	Content []ContentBlock `json:"content"`
	Model   string         `json:"model,omitempty"`
	Usage   TokenUsage     `json:"usage"`
}

// ContentBlock is one block of an assistant or tool-result message's content
// array. ToolUseID, Content, and IsError appear only on a "tool_result" block,
// which the Claude API returns as a "user"-typed event.
type ContentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Text      string          `json:"text,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// TaskInput is the input payload of a Task tool-use block.
type TaskInput struct {
	SubagentType string `json:"subagent_type"`
}

// TokenUsage is the per-message token accounting embedded in assistant events.
type TokenUsage struct {
	InputTokens              int            `json:"input_tokens"`
	OutputTokens             int            `json:"output_tokens"`
	CacheReadInputTokens     int            `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int            `json:"cache_creation_input_tokens"`
	CacheCreation            *CacheCreation `json:"cache_creation,omitempty"`
}

// CacheCreation splits CacheCreationInputTokens by cache TTL. It is nil when
// the stream-json event predates the split, so callers must nil-check before
// dereferencing.
type CacheCreation struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
}

const (
	// ImplementorRole is the role attributed to any message with no
	// parent_tool_use_id, meaning the main agent loop rather than a subagent.
	ImplementorRole = driverkit.ImplementorRole

	// ReviewerRole is the role attributed to a top-level orchestrator-owned
	// review pass (issue #2092).
	ReviewerRole = driverkit.ReviewerRole

	// DefaultRole is the role attributed to a Task with an empty or missing
	// subagent_type, and to any message whose parent_tool_use_id matches no
	// Task ID collected so far.
	DefaultRole = driverkit.DefaultRole
)

// isSubagentSpawnTool reports whether a tool-use block with this name spawns a
// subagent. "Task" is the legacy name; "Agent" is the current Box claude name,
// confirmed in a real --output-format stream-json sample (issue #2078).
func isSubagentSpawnTool(name string) bool {
	return name == "Task" || name == "Agent"
}

// CollectTaskRoles records each Task/Agent tool-use block's subagent_type into
// taskRole, keyed by the spawn block's tool-use ID and defaulting to
// DefaultRole. The key is global rather than per-parent so that a nested
// spawn's role resolves instead of falling back to DefaultRole.
func CollectTaskRoles(ev Event, taskRole map[string]string) {
	if ev.Message == nil {
		return
	}
	for _, block := range ev.Message.Content {
		if block.Type != "tool_use" || !isSubagentSpawnTool(block.Name) || block.ID == "" {
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

// AttributionRoleForPass maps a pass_start op's Role to an attribution role:
// "review"/"delta-review" to ReviewerRole, "implement"/"fix"/"land" to
// ImplementorRole, and anything else to "" so a caller can tell "no role info"
// from "implementor".
// The literals are not passmachine's constants because driverExecBin's fileset
// (lib/mkHarness.nix) excludes that package, so importing it breaks the build.
func AttributionRoleForPass(passRole string) string {
	switch passRole {
	case "review", "delta-review":
		return ReviewerRole
	case "implement", "fix", "land":
		return ImplementorRole
	default:
		return ""
	}
}

// nextActiveTopLevelRole returns the top-level attribution role in effect after
// op (issue #2382). An op that maps to no role leaves current unchanged.
func nextActiveTopLevelRole(current string, op *hostop.SpindriftOp) string {
	if op == nil || op.Op != "pass_start" {
		return current
	}
	if role := AttributionRoleForPass(op.Role); role != "" {
		return role
	}
	return current
}

// ResolveRole returns the acting role for ev. A top-level event (no
// parent_tool_use_id) takes topLevelRole, falling back to ImplementorRole when
// it is empty (issue #2092); any other event takes the role recorded in
// taskRole for its parent Task ID, or DefaultRole when the parent is unknown.
func ResolveRole(ev Event, taskRole map[string]string, topLevelRole string) string {
	if ev.ParentToolUseID == "" {
		if topLevelRole != "" {
			return topLevelRole
		}
		return ImplementorRole
	}
	if role, ok := taskRole[ev.ParentToolUseID]; ok {
		return role
	}
	return DefaultRole
}
