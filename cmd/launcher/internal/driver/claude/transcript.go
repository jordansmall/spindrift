package claude

import (
	"encoding/json"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

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
// headers), Usage is usage-only (token accounting), ID is usage-dedup-only
// (breakdownByModelFile collapses re-emitted same-id lines) — no consumer
// requires every field to be populated.
type Message struct {
	ID      string         `json:"id,omitempty"`
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
	InputTokens              int            `json:"input_tokens"`
	OutputTokens             int            `json:"output_tokens"`
	CacheReadInputTokens     int            `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int            `json:"cache_creation_input_tokens"`
	CacheCreation            *CacheCreation `json:"cache_creation,omitempty"`
}

// CacheCreation splits CacheCreationInputTokens by cache TTL. It is nil when
// the stream-json event predates the split (or a consumer doesn't care about
// the breakdown) — callers must nil-check before dereferencing.
type CacheCreation struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
}

// SpindriftOp is the payload of a synthetic "spindrift_op" stream-json event
// (issue #2027): the orchestrator prints one of these, JSON-encoded, to its
// own stdout at each discrete operation it performs (pass start, reviewer
// verdict observed, a pass ending with no outcome line, loop/stop decision,
// run-state read/write failure) so the heartbeat Writer can surface it live,
// interleaved with driver-exec's own stream-json lines forwarded unchanged.
type SpindriftOp struct {
	// Op names the operation kind: "pass_start", "verdict", "pass_no_outcome",
	// "decision", "run_state_error", "worker_start", or "worker_finish".
	Op   string `json:"op"`
	Pass int    `json:"pass,omitempty"`
	// Role names the pass's own role on a pass_start op (issue #2037):
	// "implement" for the first pass, "review" for a code-owned review
	// pass, "fix" for a review-BLOCK-triggered pass that can loop back into
	// another review, or "land" for the terminal pass (issue #2457,
	// #2654) -- reached either because a review APPROVEd or because a cap
	// committed the run to land -- that runs exactly once per run and
	// cannot re-enter the review cycle. Empty on every other op kind, and
	// on a pass_start from the legacy single-loop path that never
	// distinguishes roles.
	Role     string `json:"role,omitempty"`
	Verdict  string `json:"verdict,omitempty"`
	Decision string `json:"decision,omitempty"` // "continue" or "stop"
	Reason   string `json:"reason,omitempty"`
	Phase    string `json:"phase,omitempty"` // "read", "write", "findings_log", "dispositions_log", "dispositions_budget", "decisions_log", or "decisions_budget", for run_state_error
	Error    string `json:"error,omitempty"`
	// Worker names the slice a worker_start/worker_finish op concerns
	// (issue #2059) -- empty on every other op kind.
	Worker string `json:"worker,omitempty"`
	// WorkerStatus is the WorkerStatus word ("done", "timed_out", "crashed")
	// on a worker_finish op -- empty on worker_start and every other op kind.
	WorkerStatus string `json:"worker_status,omitempty"`
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

const (
	// ImplementorRole is the role attributed to any message with no
	// parent_tool_use_id — the main agent loop, as opposed to a Task subagent.
	ImplementorRole = driverkit.ImplementorRole

	// ReviewerRole is the role attributed to a top-level orchestrator-owned
	// review pass (issue #2092).
	ReviewerRole = driverkit.ReviewerRole

	// DefaultRole is the role attributed to a Task whose input carries no (or
	// empty) subagent_type, and to any message whose parent_tool_use_id does
	// not match a Task ID collected so far.
	DefaultRole = driverkit.DefaultRole
)

// isSubagentSpawnTool reports whether a tool-use block with this name
// spawns a subagent. "Task" is the legacy name; "Agent" is the current
// Box `claude` name — a confirmed real --output-format stream-json sample
// carries subagent spawns as "Agent" blocks (issue #2078). This is the
// single source of truth shared by CollectTaskRoles, toolToPhase, and
// toolKind.
func isSubagentSpawnTool(name string) bool {
	return name == "Task" || name == "Agent"
}

// CollectTaskRoles scans an event — whether issued by the implementor
// (ParentToolUseID == "") or by a subagent at any nesting depth — for
// Task/Agent tool-use blocks and records each one's subagent role — from its
// subagent_type input field, defaulting to DefaultRole — into taskRole, keyed
// globally by the spawn block's tool-use ID. Keying globally lets a nested
// spawn's role resolve correctly instead of falling back to DefaultRole.
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

// AttributionRoleForPass maps a pass_start SpindriftOp's Role field (the
// orchestrator's own pass vocabulary: "implement", "review", "fix", "land" —
// see SpindriftOp.Role) to the attribution role constants console surfaces
// use (ImplementorRole or ReviewerRole): "review" becomes ReviewerRole;
// "implement", "fix", and "land" all become ImplementorRole, since a fix
// pass and a land pass are both implementor passes from the attribution
// surface's point of view. An empty passRole (legacy pass_start with no
// role, or any op that isn't a pass_start) and any unrecognized value both
// map to "" rather than ImplementorRole — collapsing "no role info" into
// "explicitly implementor" would make it impossible for a caller to tell
// "use the default" apart from "the default was chosen"; the caller decides
// what "no change"/"use default" means. These four cases are bare string
// literals rather than passmachine's RoleReview/RoleImplement/RoleFix/
// RoleLand constants deliberately: driver-exec's own Nix build (lib/mkHarness.nix
// driverExecBin) sources this package through a fileset that excludes
// internal/passmachine on purpose, so pulling that package in here would
// break the box's own image build, not just add a dependency.
func AttributionRoleForPass(passRole string) string {
	switch passRole {
	case "review":
		return ReviewerRole
	case "implement", "fix", "land":
		return ImplementorRole
	default:
		return ""
	}
}

// nextActiveTopLevelRole returns the top-level attribution role that should
// be active after observing op, given the role currently in effect
// (issue #2382). A pass_start op whose Role maps to a non-empty attribution
// role (via AttributionRoleForPass) switches to that role; every other case —
// a different op kind, a nil op, or a pass_start whose Role maps to ""—
// leaves current unchanged. Both the heartbeat Writer and the transcript
// renderer drive their live activeTopLevelRole through this one function.
func nextActiveTopLevelRole(current string, op *SpindriftOp) string {
	if op == nil || op.Op != "pass_start" {
		return current
	}
	if role := AttributionRoleForPass(op.Role); role != "" {
		return role
	}
	return current
}

// ResolveRole returns the acting role for ev: when it has no
// parent_tool_use_id (a top-level pass), topLevelRole if non-empty,
// otherwise ImplementorRole — so an empty topLevelRole preserves the
// long-standing ImplementorRole default (issue #2092). Otherwise it returns
// the role recorded in taskRole for its parent Task ID, defaulting to
// DefaultRole when the parent is unknown; a real (non-empty)
// parent_tool_use_id is unaffected by topLevelRole.
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
