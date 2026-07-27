package claude

import "encoding/json"

// Event is one line of a claude CLI stream-json transcript. This shape, and
// the Task-ID-to-role resolution below, are shared by every consumer that
// walks a Box's transcript — the heartbeat writer and the usage extractor,
// both in this package. A future second Driver carries its own transcript
// shape rather than inheriting this one (ADR 0009).
type Event struct {
	Type            string       `json:"type"`
	Message         *Message     `json:"message,omitempty"`
	NumTurns        int          `json:"num_turns,omitempty"`
	ParentToolUseID string       `json:"parent_tool_use_id,omitempty"`
	SpindriftOp     *SpindriftOp `json:"spindrift_op,omitempty"`
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

// ContentBlock is one block of an assistant or tool-result message's content
// array. ToolUseID, Content, and IsError are populated only on a "tool_result"
// block (a "user"-typed event, per the Claude API's convention of returning
// tool results as a user-role turn) — the transcript renderer's fields, unused
// by the heartbeat writer or usage extractor.
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
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// SpindriftOp is the payload of a synthetic "spindrift_op" stream-json event
// (issue #2027): the orchestrator prints one of these, JSON-encoded, to its
// own stdout at each discrete operation it performs (pass start, reviewer
// verdict observed, a pass ending with no outcome line, loop/stop decision,
// run-state read/write failure) so the heartbeat Writer can surface it live,
// interleaved with driver-exec's own stream-json lines forwarded unchanged.
type SpindriftOp struct {
	// Op names the operation kind: "pass_start", "verdict", "pass_no_outcome",
	// "decision", or "run_state_error".
	Op   string `json:"op"`
	Pass int    `json:"pass,omitempty"`
	// Role names the pass's own role on a pass_start op (issue #2037):
	// "implement" for the first pass, "review" for a code-owned review
	// pass, "fix" for an implement/fix pass a review's BLOCK (or APPROVE,
	// to land) triggered. Empty on every other op kind, and on a pass_start
	// from the legacy single-loop path that never distinguishes roles.
	Role     string `json:"role,omitempty"`
	Verdict  string `json:"verdict,omitempty"`
	Decision string `json:"decision,omitempty"` // "continue" or "stop"
	Reason   string `json:"reason,omitempty"`
	Phase    string `json:"phase,omitempty"` // "read" or "write", for run_state_error
	Error    string `json:"error,omitempty"`
}

// EncodeSpindriftOp returns a single newline-terminated stream-json line
// encoding op as a synthetic "spindrift_op" event, ready to write directly
// onto the same stdout stream driver-exec's own raw output flows through
// (issue #2027) -- the Writer's parseLine recognizes it via Event.Type.
func EncodeSpindriftOp(op SpindriftOp) string {
	b, err := json.Marshal(Event{Type: "spindrift_op", SpindriftOp: &op})
	if err != nil {
		// SpindriftOp's fields are all plain strings and ints, so marshaling
		// them can't practically fail -- but this is a heartbeat/observability
		// path, not a real one, so a failure here degrades to "no marker
		// emitted" rather than crashing the orchestrator's own loop.
		return ""
	}
	return string(b) + "\n"
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
