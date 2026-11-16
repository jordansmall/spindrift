package main

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"spindrift.dev/sdk/driver"
	"spindrift.dev/sdk/event"
)

// WriteTool is the claude CLI's file-write tool name.
const WriteTool = "Write"

// field returns the last value of obj's member that name selects, decoded with
// decode as json.Unmarshal would a struct field of that name; ok is false when
// decode rejects any such member. Walking obj is a tape iteration, so each
// probe below costs no re-parse of the line (issue #7107).
func field[T any](obj driver.Value, name string, decode func(driver.Value, *T) bool) (t T, ok bool) {
	for k, v := range obj.Members() {
		if driver.KeyMatches(k, name) && !decode(v, &t) {
			return t, false
		}
	}
	return t, true
}

// toolTarget returns the string value of the input field that fields names for
// tool, "" for an unknown tool, a missing field or a non-string one. It is the
// driver.Value form of sdk/driver.ToolTarget. The key is matched exactly, as a
// map key is.
func toolTarget(fields map[string]string, tool string, input driver.Value) string {
	name, ok := fields[tool]
	if !ok {
		return ""
	}
	var target string
	for k, v := range input.Members() {
		if k == name {
			target = ""
			v.DecodeString(&target)
		}
	}
	return target
}

// NewDecoder returns a fresh claude stream-json decoder. It is stateful (task
// roles, the active top-level role, the result-echo latch), so each scan or
// writer takes its own.
// connectorhost.Load gives every guest wazero's fake clocks, so time.Now() is a
// fixed instant at runtime too, and a "resets <time>" suffix resolves against
// that epoch until the host supplies a real clock (issue #6751).
func NewDecoder() driver.Decoder { return newDecoderAt(time.Now()) }

// newDecoderAt pins the clock that resolves a plain-text "resets <time>
// (UTC)" suffix, so tests do not depend on the wall clock (issue #2443).
func newDecoderAt(now time.Time) driver.Decoder {
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
	echoReason  driver.Reason
	echoPending bool
}

// decode turns one line into events. claude's CLI reports API failures as
// plain text and as several differently shaped JSON lines, so every line that
// is not host-written or agent content is a KindError candidate carrying its
// raw text, never KindUnparsed (which the classifier reads for terminal
// markers only). The line is scanned once; every probe below walks that scan.
func (d *decoder) decode(line string) []event.Event {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	root, ok := driver.Scan(line)
	if !ok {
		return []event.Event{d.errorEvent(line)}
	}
	top := readTopLevel(root)
	evs := d.decodeLine(line, root, top)
	if ts, ok := topLevelTimestamp(line, top); ok {
		for i := range evs {
			evs[i].Timestamp = ts
		}
	}
	return evs
}

// topLevel is the one top-level read of a line that the result check,
// classifyTurn and topLevelTimestamp share (issue #5438). Type is typed because
// every probe needs it as a string; the other fields stay scanned values (the
// zero Value when absent) so each probe decodes only its own, and a mistyped
// field fails only the probe that reads it. Unlike per-probe decodes, a
// repeated key keeps only its last copy; the claude CLI never writes one.
type topLevel struct {
	Type                              string
	Error, Message, Result, Timestamp driver.Value
}

// readTopLevel returns nil when root is neither an object nor null, or has a
// mistyped "type". JSON null reads as an empty topLevel, which no probe
// matches. Keys match case-insensitively, as encoding/json struct fields do.
func readTopLevel(root driver.Value) *topLevel {
	if k := root.Kind(); k != driver.Object && k != driver.Null {
		return nil
	}
	var top topLevel
	for k, v := range root.Members() {
		switch {
		case driver.KeyMatches(k, "type"):
			if !v.DecodeString(&top.Type) {
				return nil
			}
		case driver.KeyMatches(k, "error"):
			top.Error = v
		case driver.KeyMatches(k, "message"):
			top.Message = v
		case driver.KeyMatches(k, "result"):
			top.Result = v
		case driver.KeyMatches(k, "timestamp"):
			top.Timestamp = v
		}
	}
	return &top
}

// topLevelTimestamp returns the epoch milliseconds of the line's top-level
// "timestamp". A line with no top-level type is ignored, and a timestamp
// nested inside tool_result content is never read, so the usage reader's event
// span covers exactly the lines claude stamps as events.
func topLevelTimestamp(line string, top *topLevel) (int64, bool) {
	if top == nil || !strings.Contains(line, `"timestamp"`) {
		return 0, false
	}
	var stamp string
	if top.Type == "" || !top.Timestamp.DecodeString(&stamp) || stamp == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

func (d *decoder) decodeLine(line string, root driver.Value, top *topLevel) []event.Event {
	// Host-written; knob values and the settle note are operator or Box text:
	// scanning them could turn a terminal failure into retries. The host
	// decodes the op itself and discards this output, so only the role
	// tracking is kept.
	if role, ok := driver.HostLineOf(root); ok {
		if role != "" {
			d.topRole = role
		}
		return nil
	}

	content, synthetic := classifyTurn(top)
	switch {
	case content:
		// The agent's own content can quote rate-limit markers verbatim; the
		// KindStart tells the classifier the run continued past any candidate
		// (issue #579), even when no block below decodes.
		d.echoReason, d.echoPending = matchMarker(line)
		return append([]event.Event{{Kind: event.KindStart}}, d.contentEvents(root, top.Type, false)...)
	case synthetic:
		return append([]event.Event{d.errorEvent(line)}, d.contentEvents(root, top.Type, true)...)
	}
	if top == nil || top.Type != "result" {
		return []event.Event{d.errorEvent(line)}
	}
	return d.resultEvents(line, root, top)
}

// resultEvents decodes a type:"result" line. A type error anywhere fails a
// whole struct decode, so the text, is_error, totals and num_turns read
// separately, as the pre-decoder readers did: a mistyped total must not cost
// the result text or the echo check, and a mistyped is_error must cost neither
// the text nor the totals.
func (d *decoder) resultEvents(line string, root driver.Value, top *topLevel) []event.Event {
	var text string
	textOK := top.Result.Kind() == driver.Invalid || top.Result.DecodeString(&text) // a non-string result leaves text ""
	isError, errOK := field(root, "is_error", driver.Value.DecodeBool)
	notError := errOK && !isError
	result := event.Event{Kind: event.KindResult, Text: text}
	if tot, ok := readTotals(root); ok {
		// A result's usage header carries only the flat cache_creation_input_tokens
		// total, so it lands in the 5m bucket like a pre-split assistant message.
		// Output stays the main loop's usage.output_tokens. Whole-session output
		// is the modelUsage outputTokens sum carried in ModelOutput (subagents
		// included); the reader falls back to Output per result when modelUsage
		// is empty or missing (issue #6273).
		result.Tokens = event.Tokens{
			Input:        tot.usage.InputTokens,
			Output:       tot.usage.OutputTokens,
			CacheRead:    tot.usage.CacheReadInputTokens,
			CacheWrite5m: tot.usage.CacheCreationInputTokens,
			CostUSD:      tot.costUSD,
		}
		result.DurationMs, result.DurationAPIMs = int64(tot.durationMs), int64(tot.durationAPIMs)
	} else {
		result.NoTotals = true
	}
	// Read alone: the heartbeat drops a result only for a mistyped
	// num_turns, not for any other bad total.
	if turns, ok := field(root, "num_turns", driver.Value.DecodeInt); ok {
		result.NumTurns = turns
	} else {
		// A mistyped num_turns is a mistyped total too: no totals at all.
		result.Tokens = event.Tokens{}
		result.DurationMs, result.DurationAPIMs = 0, 0
		result.NoTotals, result.NoTurns = true, true
	}
	// Read apart from the totals: a mistyped modelUsage must not mark the
	// result NoTotals.
	models, outputOK, modelsOK := modelUsage(root)
	if modelsOK {
		result.Models = slices.Sorted(maps.Keys(models))
	}
	// Per-model output, read apart again: one mistyped figure drops them
	// all (the log then reads as having none) without touching Models.
	if modelsOK && outputOK && len(models) > 0 {
		result.ModelOutput = models
	}
	// Lenient: a mistyped session_id leaves the result unrebased rather than
	// costing it its totals.
	if sess, ok := field(root, "session_id", driver.Value.DecodeString); ok {
		result.SessionID = sess
	}
	out := []event.Event{result}
	// An is_error result is a genuine failure, never an echo of preceding content.
	if d.echoPending && textOK && notError {
		d.echoPending = false
		if reason, matched := matchMarker(text); matched && reason == d.echoReason {
			return out
		}
	}
	return append(out, d.errorEvent(line))
}

// totals is the numeric header of a result line.
type totals struct {
	costUSD                   float64
	durationMs, durationAPIMs int
	usage                     tokenUsage
}

// readTotals reads the result totals as one decode, so any mistyped total
// (usage's included) fails them all.
func readTotals(root driver.Value) (t totals, ok bool) {
	for k, v := range root.Members() {
		switch {
		case driver.KeyMatches(k, "total_cost_usd"):
			ok = v.DecodeFloat(&t.costUSD)
		case driver.KeyMatches(k, "duration_ms"):
			ok = v.DecodeInt(&t.durationMs)
		case driver.KeyMatches(k, "duration_api_ms"):
			ok = v.DecodeInt(&t.durationAPIMs)
		case driver.KeyMatches(k, "usage"):
			ok = t.usage.merge(v)
		default:
			continue
		}
		if !ok {
			return t, false
		}
	}
	return t, true
}

// modelUsage reads the result's modelUsage map as each model's outputTokens.
// Like a decoded map, a repeated member adds to it and a null resets it; ok is
// false when any member is neither object nor null. outputOK is false once any
// member copy, even one a later copy replaces or a reset drops, had a mistyped
// entry or figure, as with a decoded map. A null entry is a zero one.
func modelUsage(root driver.Value) (models map[string]int, outputOK, ok bool) {
	outputOK = true
	for k, v := range root.Members() {
		if !driver.KeyMatches(k, "modelUsage") {
			continue
		}
		switch v.Kind() {
		case driver.Null:
			models = nil
		case driver.Object:
			if models == nil {
				models = map[string]int{}
			}
			for model, u := range v.Members() {
				tokens, tokensOK := 0, false
				if kind := u.Kind(); kind == driver.Object || kind == driver.Null {
					tokens, tokensOK = field(u, "outputTokens", driver.Value.DecodeInt)
				}
				outputOK = outputOK && tokensOK
				models[model] = tokens
			}
		default:
			return nil, false, false
		}
	}
	return models, outputOK, true
}

func (d *decoder) errorEvent(line string) event.Event {
	return event.Event{Kind: event.KindError, Text: line, ResetAt: extractResetsAt(line, d.now)}
}

// block is the union of the content-block fields the decoder reads. Input,
// Thinking and Content stay scanned values so a stray key of another kind on
// another block type cannot fail that block; only the type that reads one
// decodes it.
type block struct {
	Type, ID, Name, Text, ToolUseID string
	IsError                         bool
	Input, Thinking, Content        driver.Value
}

// readBlock reads one content block, ok being false on any mistyped member.
func readBlock(v driver.Value) (b block, ok bool) {
	for k, m := range v.Members() {
		ok = true
		switch {
		case driver.KeyMatches(k, "type"):
			ok = m.DecodeString(&b.Type)
		case driver.KeyMatches(k, "id"):
			ok = m.DecodeString(&b.ID)
		case driver.KeyMatches(k, "name"):
			ok = m.DecodeString(&b.Name)
		case driver.KeyMatches(k, "input"):
			b.Input = m
		case driver.KeyMatches(k, "text"):
			ok = m.DecodeString(&b.Text)
		case driver.KeyMatches(k, "thinking"):
			b.Thinking = m
		case driver.KeyMatches(k, "tool_use_id"):
			ok = m.DecodeString(&b.ToolUseID)
		case driver.KeyMatches(k, "is_error"):
			ok = m.DecodeBool(&b.IsError)
		case driver.KeyMatches(k, "content"):
			b.Content = m
		}
		if !ok {
			return b, false
		}
	}
	return b, true
}

// message is the envelope's "message" object.
type message struct {
	ID, Model string
	Content   driver.Value
	Usage     tokenUsage
}

// readMessage reads an assistant or user line's parent_tool_use_id and
// message. Like a decoded pointer field, a null message is nil and a repeated
// message object merges into the one before it. ok is false on any mistyped
// member, usage's included.
func readMessage(root driver.Value) (parent string, msg *message, ok bool) {
	for k, v := range root.Members() {
		switch {
		case driver.KeyMatches(k, "parent_tool_use_id"):
			ok = v.DecodeString(&parent)
		case driver.KeyMatches(k, "message"):
			ok = true
			switch v.Kind() {
			case driver.Null:
				msg = nil
			case driver.Object:
				if msg == nil {
					msg = &message{}
				}
				ok = msg.merge(v)
			default:
				ok = false
			}
		default:
			continue
		}
		if !ok {
			return "", nil, false
		}
	}
	return parent, msg, true
}

func (m *message) merge(obj driver.Value) bool {
	for k, v := range obj.Members() {
		ok := true
		switch {
		case driver.KeyMatches(k, "id"):
			ok = v.DecodeString(&m.ID)
		case driver.KeyMatches(k, "model"):
			ok = v.DecodeString(&m.Model)
		case driver.KeyMatches(k, "content"):
			m.Content = v
		case driver.KeyMatches(k, "usage"):
			ok = m.Usage.merge(v)
		}
		if !ok {
			return false
		}
	}
	return true
}

// contentEvents decodes an assistant or user line of the given type: for an
// assistant line one KindModel, then its content blocks, then one KindUsage.
// The envelope is strict: a type error in it (a mistyped usage field, say)
// yields no events, as the pre-decoder readers dropped such a line. Only the
// content is lenient: a user turn whose content is a string, or a block that
// fails to decode, yields no events for that part rather than losing the line.
// An assistant line whose content is a string, or which holds a block that
// fails to decode, still yields its KindModel (so OnModel and the model/role
// switch fire) and its valid blocks, where the pre-decoder heartbeat and usage
// readers dropped it whole: one bad block should not cost the line's model,
// narration and tool calls (issue #5454). Task spawns are recorded here and
// nothing is deduplicated; the usage reader dedups by MessageID.
func (d *decoder) contentEvents(root driver.Value, typ string, synthetic bool) []event.Event {
	parent, msg, ok := readMessage(root)
	if !ok || msg == nil {
		return nil
	}
	var blocks []block
	for elem := range msg.Content.Elems() {
		// A null element reads as the zero block, which no case below keeps.
		if elem.Kind() != driver.Object {
			continue
		}
		if b, ok := readBlock(elem); ok {
			blocks = append(blocks, b)
		}
	}
	if typ == "assistant" {
		d.recordSpawns(blocks)
	}

	subagent := parent != ""
	role := d.topRole
	if subagent {
		role = d.subagentRole(parent)
	}

	var out []event.Event
	if typ == "assistant" {
		// One per line, even with no model: the heartbeat switches role on it.
		out = append(out, event.Event{
			Kind: event.KindModel, MessageID: msg.ID, Model: msg.Model,
			Role: role, InSubagent: subagent,
			Synthetic: synthetic || msg.Model == syntheticModelSentinel,
		})
	}
	for _, b := range blocks {
		ev := event.Event{MessageID: msg.ID, Role: role, InSubagent: subagent, Synthetic: synthetic}
		switch b.Type {
		case "text":
			// The transcript lists the agent's prose, not the user turns
			// (subagent prompts) that also carry text blocks.
			if typ != "assistant" {
				continue
			}
			ev.Kind, ev.Text = event.KindText, b.Text
		case "thinking":
			var thinking string
			if b.Thinking.Kind() != driver.Invalid && !b.Thinking.DecodeString(&thinking) {
				continue
			}
			ev.Kind, ev.Text = event.KindReasoning, thinking
		case "tool_use":
			// A call lives in an assistant turn and its result in the user turn
			// after it; a block in the other turn type was never rendered.
			if typ != "assistant" {
				continue
			}
			d.toolNames[b.ID] = b.Name
			ev.Kind, ev.ToolID, ev.ToolName = event.KindToolCall, b.ID, b.Name
			if b.Input.Kind() != driver.Invalid {
				ev.ToolInput = json.RawMessage(b.Input.Raw())
			}
			ev.ToolTarget = toolTarget(toolTargetField, b.Name, b.Input)
		case "tool_result":
			if typ != "user" {
				continue
			}
			ev.Kind, ev.ToolID, ev.ToolName, ev.IsError = event.KindToolResult, b.ToolUseID, d.toolNames[b.ToolUseID], b.IsError
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
	if typ == "assistant" {
		u := msg.Usage
		tok := event.Tokens{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens}
		if cc := u.CacheCreation; cc != nil {
			tok.CacheWrite5m, tok.CacheWrite1h = cc.Ephemeral5mInputTokens, cc.Ephemeral1hInputTokens
		} else {
			// Pre-TTL-split log: only the flat total is populated. The Messages
			// API's cache_control default TTL is 5m and 1h is opt-in, so an
			// un-split total is almost certainly all-5m.
			tok.CacheWrite5m = u.CacheCreationInputTokens
		}
		out = append(out, event.Event{
			Kind: event.KindUsage, MessageID: msg.ID, Model: msg.Model,
			Role: role, InSubagent: subagent, Tokens: tok,
		})
	}
	return out
}

// tokenUsage is the per-message token accounting in assistant and result lines.
type tokenUsage struct {
	InputTokens              int
	OutputTokens             int
	CacheReadInputTokens     int
	CacheCreationInputTokens int
	CacheCreation            *cacheCreation
}

// cacheCreation splits CacheCreationInputTokens by cache TTL. It is nil when
// the line predates the split, so callers must nil-check before dereferencing.
type cacheCreation struct {
	Ephemeral5mInputTokens int
	Ephemeral1hInputTokens int
}

// merge reads a "usage" value into u: null changes nothing, an object adds its
// members to those already read, and ok is false for any mistyped member. A
// null cache_creation clears the split, an object merges into it.
func (u *tokenUsage) merge(v driver.Value) bool {
	switch v.Kind() {
	case driver.Null:
		return true
	case driver.Object:
	default:
		return false
	}
	for k, m := range v.Members() {
		ok := true
		switch {
		case driver.KeyMatches(k, "input_tokens"):
			ok = m.DecodeInt(&u.InputTokens)
		case driver.KeyMatches(k, "output_tokens"):
			ok = m.DecodeInt(&u.OutputTokens)
		case driver.KeyMatches(k, "cache_read_input_tokens"):
			ok = m.DecodeInt(&u.CacheReadInputTokens)
		case driver.KeyMatches(k, "cache_creation_input_tokens"):
			ok = m.DecodeInt(&u.CacheCreationInputTokens)
		case driver.KeyMatches(k, "cache_creation"):
			switch m.Kind() {
			case driver.Null:
				u.CacheCreation = nil
			case driver.Object:
				if u.CacheCreation == nil {
					u.CacheCreation = &cacheCreation{}
				}
				ok = u.CacheCreation.merge(m)
			default:
				ok = false
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func (c *cacheCreation) merge(obj driver.Value) bool {
	for k, m := range obj.Members() {
		ok := true
		switch {
		case driver.KeyMatches(k, "ephemeral_5m_input_tokens"):
			ok = m.DecodeInt(&c.Ephemeral5mInputTokens)
		case driver.KeyMatches(k, "ephemeral_1h_input_tokens"):
			ok = m.DecodeInt(&c.Ephemeral1hInputTokens)
		}
		if !ok {
			return false
		}
	}
	return true
}

// The subagent-spawn tools. "Task" is the legacy name; "Agent" is the current
// Box claude name, confirmed in a real --output-format stream-json sample
// (issue #2078).
const (
	taskTool  = "Task"
	agentTool = "Agent"
)

// isSubagentSpawnTool reports whether a tool-use block with this name spawns a
// subagent.
func isSubagentSpawnTool(name string) bool {
	return name == taskTool || name == agentTool
}

// toolTargetField names, per Claude tool, the input field that best identifies
// what the call acted on. An unknown tool, or a missing or non-string field,
// yields "", which the renderer shows as a bare "Name()".
var toolTargetField = map[string]string{
	taskTool:       "subagent_type",
	agentTool:      "subagent_type",
	"Read":         "file_path",
	"Edit":         "file_path",
	"Write":        "file_path",
	"NotebookEdit": "file_path",
	"Grep":         "pattern",
	"Glob":         "pattern",
	"Bash":         "command",
	"WebFetch":     "url",
	"WebSearch":    "query",
}

// subagentType reads subagent_type from a spawn tool's input, or "" when it is
// absent or malformed. A mistyped member is skipped, not fatal, as a decode
// whose error is ignored still fills the other fields.
func subagentType(input driver.Value) string {
	var t string
	for k, v := range input.Members() {
		if driver.KeyMatches(k, "subagent_type") {
			v.DecodeString(&t)
		}
	}
	return t
}

// recordSpawns records each Task/Agent tool-use block's subagent_type into
// taskRole, keyed by the spawn block's tool-use ID and defaulting to
// driver.DefaultRole. The key is global rather than per-parent so that a
// nested spawn's role resolves instead of falling back to DefaultRole.
func (d *decoder) recordSpawns(blocks []block) {
	for _, b := range blocks {
		if b.Type != "tool_use" || !isSubagentSpawnTool(b.Name) || b.ID == "" {
			continue
		}
		role := subagentType(b.Input)
		if role == "" {
			role = driver.DefaultRole
		}
		d.taskRole[b.ID] = role
	}
}

// subagentRole returns the role recorded for a spawn ID, or
// driver.DefaultRole when the parent is unknown. A top-level event takes
// topRole instead, left empty here for the readers to default (issue #2092).
func (d *decoder) subagentRole(parent string) string {
	if role, ok := d.taskRole[parent]; ok {
		return role
	}
	return driver.DefaultRole
}
