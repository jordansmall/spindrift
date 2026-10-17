package claude

import (
	"encoding/json"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
)

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

	var res struct {
		Type          string          `json:"type"`
		IsError       bool            `json:"is_error"`
		Result        json.RawMessage `json:"result"`
		NumTurns      int             `json:"num_turns"`
		TotalCostUSD  float64         `json:"total_cost_usd"`
		DurationMs    int64           `json:"duration_ms"`
		DurationAPIMs int64           `json:"duration_api_ms"`
		Usage         TokenUsage      `json:"usage"`
	}
	// A mistyped field leaves the rest of res filled, so a line that fails to
	// decode contributes no totals at all, as before the decoder.
	if json.Unmarshal([]byte(line), &res) != nil || res.Type != "result" {
		return []driverkit.Event{d.errorEvent(line)}
	}
	var text string
	textOK := len(res.Result) == 0 || json.Unmarshal(res.Result, &text) == nil
	// A result's usage header carries only the flat cache_creation_input_tokens
	// total, so it lands in the 5m bucket like a pre-split assistant message.
	out := []driverkit.Event{{
		Kind: driverkit.KindResult,
		Text: text,
		Tokens: driverkit.Tokens{
			Input:        res.Usage.InputTokens,
			Output:       res.Usage.OutputTokens,
			CacheRead:    res.Usage.CacheReadInputTokens,
			CacheWrite5m: res.Usage.CacheCreationInputTokens,
			CostUSD:      res.TotalCostUSD,
		},
		DurationMs:    res.DurationMs,
		DurationAPIMs: res.DurationAPIMs,
		NumTurns:      res.NumTurns,
	}}
	// An is_error result is a genuine failure, never an echo of preceding content.
	if d.echoPending && textOK && !res.IsError {
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
type decodedBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

// contentEvents decodes an assistant or user line: its content blocks, then
// for an assistant line one KindUsage. It is lenient: a user turn whose content
// is a string, or a block that fails to unmarshal, yields no events for that
// part rather than losing the line. Task spawns are recorded here and nothing
// is deduplicated; the usage reader dedups by MessageID.
func (d *decoder) contentEvents(line string, synthetic bool) []driverkit.Event {
	var env struct {
		Type            string `json:"type"`
		ParentToolUseID string `json:"parent_tool_use_id"`
		Message         *struct {
			ID      string          `json:"id"`
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
			Usage   TokenUsage      `json:"usage"`
		} `json:"message"`
	}
	_ = json.Unmarshal([]byte(line), &env)
	if env.Message == nil {
		return nil
	}
	var raws []json.RawMessage
	if json.Unmarshal(env.Message.Content, &raws) != nil {
		raws = nil
	}
	blocks := make([]decodedBlock, 0, len(raws))
	spawn := make([]ContentBlock, 0, len(raws))
	for _, raw := range raws {
		var b decodedBlock
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		blocks = append(blocks, b)
		spawn = append(spawn, ContentBlock{Type: b.Type, ID: b.ID, Name: b.Name, Input: b.Input})
	}
	CollectTaskRoles(Event{Message: &Message{Content: spawn}}, d.taskRole)

	subagent := env.ParentToolUseID != ""
	role := d.topRole
	if subagent {
		role = ResolveRole(Event{ParentToolUseID: env.ParentToolUseID}, d.taskRole, d.topRole)
	}

	var out []driverkit.Event
	for _, b := range blocks {
		ev := driverkit.Event{MessageID: env.Message.ID, Role: role}
		switch b.Type {
		case "text":
			ev.Kind, ev.Text, ev.Synthetic = driverkit.KindText, b.Text, synthetic
		case "thinking":
			ev.Kind, ev.Text = driverkit.KindReasoning, b.Thinking
		case "tool_use":
			d.toolNames[b.ID] = b.Name
			ev.Kind, ev.ToolID, ev.ToolName, ev.ToolInput = driverkit.KindToolCall, b.ID, b.Name, b.Input
		case "tool_result":
			ev.Kind, ev.ToolID, ev.ToolName, ev.IsError = driverkit.KindToolResult, b.ToolUseID, d.toolNames[b.ToolUseID], b.IsError
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
