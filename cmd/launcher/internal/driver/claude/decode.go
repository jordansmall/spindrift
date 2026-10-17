package claude

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
)

// WriteTool is the claude CLI's file-write tool name.
const WriteTool = "Write"

// NewDecoder returns a fresh claude stream-json decoder. It is stateful (task
// roles, the active top-level role, the result-echo latch), so each scan or
// writer takes its own.
func NewDecoder() driverkit.Decoder { return newDecoderAt(time.Now()) }

// newDecoderAt pins the clock that resolves a plain-text "resets <time>
// (UTC)" suffix, so tests do not depend on the wall clock (issue #2443).
func newDecoderAt(now time.Time) driverkit.Decoder {
	d := &decoder{
		now:       now,
		taskRole:  map[string]string{},
		toolNames: map[string]string{},
	}
	return d.decode
}

type decoder struct {
	now time.Time

	taskRole  map[string]string
	toolNames map[string]string
	topRole   string

	// echoReason and echoPending remember that the latest agent content quoted
	// a marker, so a later type:"result" line echoing it is recognized as that
	// echo rather than a fresh signal (issue #818). The latch must survive any
	// number of intervening non-content lines, such as type:"system"
	// heartbeats (issue #1197).
	echoReason  driverkit.Reason
	echoPending bool
}

// decode turns one line into events. claude's CLI reports API failures as
// plain text and as several differently shaped JSON lines, so every line that
// is not host-written or agent content is a KindError candidate carrying its
// raw text, never KindUnparsed (which the classifier reads for terminal
// markers only).
func (d *decoder) decode(line string) []driverkit.Event {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	evs := d.decodeLine(line)
	if ts, ok := topLevelTimestamp(line); ok {
		for i := range evs {
			evs[i].Timestamp = ts
		}
	}
	return evs
}

// topLevelTimestamp returns the epoch milliseconds of the line's top-level
// "timestamp". A line with no top-level type is ignored, and a timestamp
// nested inside tool_result content is never read, so the usage reader's event
// span covers exactly the lines claude stamps as events.
func topLevelTimestamp(line string) (int64, bool) {
	if !strings.Contains(line, `"timestamp"`) {
		return 0, false
	}
	var ts struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal([]byte(line), &ts) != nil || ts.Type == "" || ts.Timestamp == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, ts.Timestamp)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

func (d *decoder) decodeLine(line string) []driverkit.Event {
	// Host-written; knob values and the settle note are operator or Box text:
	// scanning them could turn a terminal failure into retries.
	var op hostop.Event
	if json.Unmarshal([]byte(line), &op) == nil && op.Type == hostop.TypeSpindriftOp && op.SpindriftOp != nil {
		d.topRole = nextActiveTopLevelRole(d.topRole, op.SpindriftOp)
		return []driverkit.Event{{Kind: driverkit.KindHostOp, HostOp: op.SpindriftOp}}
	}

	content, synthetic := classifyTurn(line)
	switch {
	case content:
		// The agent's own content can quote rate-limit markers verbatim; the
		// KindStart tells the classifier the run continued past any candidate
		// (issue #579), even when no block below decodes.
		d.echoReason, _, d.echoPending = matchMarker(line)
		return append([]driverkit.Event{{Kind: driverkit.KindStart}}, d.contentEvents(line, false)...)
	case synthetic:
		return append([]driverkit.Event{d.errorEvent(line)}, d.contentEvents(line, true)...)
	}

	// A type error anywhere fails the whole Unmarshal, so the text, is_error,
	// totals and num_turns decode separately, as the pre-decoder readers did: a
	// mistyped total must not cost the result text or the echo check, and a
	// mistyped is_error must cost neither the text nor the totals.
	var env struct {
		Type   string          `json:"type"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal([]byte(line), &env) != nil || env.Type != "result" {
		return []driverkit.Event{d.errorEvent(line)}
	}
	var text string
	textOK := len(env.Result) == 0 || json.Unmarshal(env.Result, &text) == nil
	var flags struct {
		IsError bool `json:"is_error"`
	}
	notError := json.Unmarshal([]byte(line), &flags) == nil && !flags.IsError
	var tot struct {
		TotalCostUSD  float64    `json:"total_cost_usd"`
		DurationMs    int64      `json:"duration_ms"`
		DurationAPIMs int64      `json:"duration_api_ms"`
		Usage         tokenUsage `json:"usage"`
	}
	result := driverkit.Event{Kind: driverkit.KindResult, Text: text}
	if json.Unmarshal([]byte(line), &tot) == nil {
		// A result's usage header carries only the flat cache_creation_input_tokens
		// total, so it lands in the 5m bucket like a pre-split assistant message.
		result.Tokens = driverkit.Tokens{
			Input:        tot.Usage.InputTokens,
			Output:       tot.Usage.OutputTokens,
			CacheRead:    tot.Usage.CacheReadInputTokens,
			CacheWrite5m: tot.Usage.CacheCreationInputTokens,
			CostUSD:      tot.TotalCostUSD,
		}
		result.DurationMs, result.DurationAPIMs = tot.DurationMs, tot.DurationAPIMs
	} else {
		result.NoTotals = true
	}
	// Decoded alone: the heartbeat drops a result only for a mistyped
	// num_turns, not for any other bad total.
	var turns struct {
		NumTurns int `json:"num_turns"`
	}
	if json.Unmarshal([]byte(line), &turns) == nil {
		result.NumTurns = turns.NumTurns
	} else {
		// A mistyped num_turns is a mistyped total too: no totals at all.
		result.Tokens = driverkit.Tokens{}
		result.DurationMs, result.DurationAPIMs = 0, 0
		result.NoTotals, result.NoTurns = true, true
	}
	// Decoded apart from the totals: a mistyped modelUsage must not mark the
	// result NoTotals.
	var mu struct {
		ModelUsage map[string]json.RawMessage `json:"modelUsage"`
	}
	if json.Unmarshal([]byte(line), &mu) == nil {
		result.Models = slices.Sorted(maps.Keys(mu.ModelUsage))
	}
	out := []driverkit.Event{result}
	// An is_error result is a genuine failure, never an echo of preceding content.
	if d.echoPending && textOK && notError {
		d.echoPending = false
		if reason, _, matched := matchMarker(text); matched && reason == d.echoReason {
			return out
		}
	}
	return append(out, d.errorEvent(line))
}

func (d *decoder) errorEvent(line string) driverkit.Event {
	return driverkit.Event{Kind: driverkit.KindError, Text: line, ResetAt: extractResetsAt(line, d.now)}
}

// decodedBlock is the union of the content-block fields the decoder reads.
// Thinking stays raw so a stray non-string key on another block type cannot
// fail that block's whole unmarshal; only a thinking block decodes it.
type decodedBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Text      string          `json:"text"`
	Thinking  json.RawMessage `json:"thinking"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

// contentEvents decodes an assistant or user line: for an assistant line one
// KindModel, then its content blocks, then one KindUsage. The envelope is
// strict: a type error in it (a mistyped usage field, say) yields no events,
// as the pre-decoder readers dropped such a line. Only the content is lenient:
// a user turn whose content is a string, or a block that fails to unmarshal,
// yields no events for that part rather than losing the line. An assistant line
// whose content is a string, or which holds a block that fails to unmarshal,
// still yields its KindModel (so OnModel and the model/role switch fire) and
// its valid blocks, where the pre-decoder heartbeat and usage readers
// dropped it whole: one bad block should not cost the line's model, narration
// and tool calls (issue #5454). Task spawns are recorded here and nothing is
// deduplicated; the usage reader dedups by MessageID.
func (d *decoder) contentEvents(line string, synthetic bool) []driverkit.Event {
	var env struct {
		Type            string `json:"type"`
		ParentToolUseID string `json:"parent_tool_use_id"`
		Message         *struct {
			ID      string          `json:"id"`
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
			Usage   tokenUsage      `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &env) != nil {
		return nil
	}
	if env.Message == nil {
		return nil
	}
	var raws []json.RawMessage
	if json.Unmarshal(env.Message.Content, &raws) != nil {
		raws = nil
	}
	blocks := make([]decodedBlock, 0, len(raws))
	for _, raw := range raws {
		var b decodedBlock
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		blocks = append(blocks, b)
	}
	if env.Type == "assistant" {
		d.recordSpawns(blocks)
	}

	subagent := env.ParentToolUseID != ""
	role := d.topRole
	if subagent {
		role = d.subagentRole(env.ParentToolUseID)
	}

	var out []driverkit.Event
	if env.Type == "assistant" {
		// One per line, even with no model: the heartbeat switches role on it.
		out = append(out, driverkit.Event{
			Kind: driverkit.KindModel, MessageID: env.Message.ID, Model: env.Message.Model,
			Role: role, InSubagent: subagent,
			Synthetic: synthetic || env.Message.Model == syntheticModelSentinel,
		})
	}
	for _, b := range blocks {
		ev := driverkit.Event{MessageID: env.Message.ID, Role: role, InSubagent: subagent, Synthetic: synthetic}
		switch b.Type {
		case "text":
			// The transcript lists the agent's prose, not the user turns
			// (subagent prompts) that also carry text blocks.
			if env.Type != "assistant" {
				continue
			}
			ev.Kind, ev.Text = driverkit.KindText, b.Text
		case "thinking":
			var thinking string
			if len(b.Thinking) > 0 && json.Unmarshal(b.Thinking, &thinking) != nil {
				continue
			}
			ev.Kind, ev.Text = driverkit.KindReasoning, thinking
		case "tool_use":
			// A call lives in an assistant turn and its result in the user turn
			// after it; a block in the other turn type was never rendered.
			if env.Type != "assistant" {
				continue
			}
			d.toolNames[b.ID] = b.Name
			ev.Kind, ev.ToolID, ev.ToolName, ev.ToolInput = driverkit.KindToolCall, b.ID, b.Name, b.Input
			ev.ToolTarget = toolTarget(b.Name, b.Input)
		case "tool_result":
			if env.Type != "user" {
				continue
			}
			ev.Kind, ev.ToolID, ev.ToolName, ev.IsError = driverkit.KindToolResult, b.ToolUseID, d.toolNames[b.ToolUseID], b.IsError
			ev.ToolOutput = resultText(b.Content)
			// Key on the block's tool_use_id, not the event's parent: a subagent's
			// final report answers its own spawn ID, which taskRole records, and
			// any other result misses the map.
			ev.Subagent = d.taskRole[b.ToolUseID]
		default:
			continue
		}
		out = append(out, ev)
	}
	if env.Type == "assistant" {
		u := env.Message.Usage
		tok := driverkit.Tokens{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens}
		if cc := u.CacheCreation; cc != nil {
			tok.CacheWrite5m, tok.CacheWrite1h = cc.Ephemeral5mInputTokens, cc.Ephemeral1hInputTokens
		} else {
			// Pre-TTL-split log: only the flat total is populated. The Messages
			// API's cache_control default TTL is 5m and 1h is opt-in, so an
			// un-split total is almost certainly all-5m.
			tok.CacheWrite5m = u.CacheCreationInputTokens
		}
		out = append(out, driverkit.Event{
			Kind: driverkit.KindUsage, MessageID: env.Message.ID, Model: env.Message.Model,
			Role: role, InSubagent: subagent, Tokens: tok,
		})
	}
	return out
}

// taskInput is the input payload of a subagent-spawning tool-use block.
type taskInput struct {
	SubagentType string `json:"subagent_type"`
}

// tokenUsage is the per-message token accounting in assistant and result lines.
type tokenUsage struct {
	InputTokens              int            `json:"input_tokens"`
	OutputTokens             int            `json:"output_tokens"`
	CacheReadInputTokens     int            `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int            `json:"cache_creation_input_tokens"`
	CacheCreation            *cacheCreation `json:"cache_creation,omitempty"`
}

// cacheCreation splits CacheCreationInputTokens by cache TTL. It is nil when
// the line predates the split, so callers must nil-check before dereferencing.
type cacheCreation struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
}

// isSubagentSpawnTool reports whether a tool-use block with this name spawns a
// subagent. "Task" is the legacy name; "Agent" is the current Box claude name,
// confirmed in a real --output-format stream-json sample (issue #2078).
func isSubagentSpawnTool(name string) bool {
	return name == "Task" || name == "Agent"
}

// subagentType reads subagent_type from a spawn tool's input, or "" when it is
// absent or malformed.
func subagentType(input json.RawMessage) string {
	var ti taskInput
	if len(input) > 0 {
		_ = json.Unmarshal(input, &ti)
	}
	return ti.SubagentType
}

// recordSpawns records each Task/Agent tool-use block's subagent_type into
// taskRole, keyed by the spawn block's tool-use ID and defaulting to
// driverkit.DefaultRole. The key is global rather than per-parent so that a
// nested spawn's role resolves instead of falling back to DefaultRole.
func (d *decoder) recordSpawns(blocks []decodedBlock) {
	for _, b := range blocks {
		if b.Type != "tool_use" || !isSubagentSpawnTool(b.Name) || b.ID == "" {
			continue
		}
		role := subagentType(b.Input)
		if role == "" {
			role = driverkit.DefaultRole
		}
		d.taskRole[b.ID] = role
	}
}

// subagentRole returns the role recorded for a spawn ID, or
// driverkit.DefaultRole when the parent is unknown. A top-level event takes
// topRole instead, left empty here for the readers to default (issue #2092).
func (d *decoder) subagentRole(parent string) string {
	if role, ok := d.taskRole[parent]; ok {
		return role
	}
	return driverkit.DefaultRole
}

// AttributionRoleForPass maps a pass_start op's Role to an attribution role:
// "review"/"delta-review" to driverkit.ReviewerRole, "implement"/"fix"/"land"
// to driverkit.ImplementorRole, and anything else to "" so a caller can tell
// "no role info" from "implementor".
// The literals are not passmachine's constants because driverExecBin's fileset
// (lib/mkHarness.nix) excludes that package, so importing it breaks the build.
func AttributionRoleForPass(passRole string) string {
	switch passRole {
	case "review", "delta-review":
		return driverkit.ReviewerRole
	case "implement", "fix", "land":
		return driverkit.ImplementorRole
	default:
		return ""
	}
}

// nextActiveTopLevelRole returns the top-level attribution role in effect after
// op (issue #2382). An op that maps to no role leaves current unchanged.
func nextActiveTopLevelRole(current string, op *hostop.SpindriftOp) string {
	if op == nil || op.Op != hostop.OpPassStart {
		return current
	}
	if role := AttributionRoleForPass(op.Role); role != "" {
		return role
	}
	return current
}
