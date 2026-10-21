package pi

import (
	"encoding/json"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// WriteTool is the pi CLI's file-write tool name.
const WriteTool = "write"

// NewDecoder returns a pi decoder with fresh state for one scan: it remembers
// how many of each subagent result's child messages it has emitted, since the
// extension's tool_execution_updates repeat them cumulatively.
func NewDecoder() driverkit.Decoder {
	d := &decoder{seen: map[seenKey]int{}}
	return d.decode
}

// Decode decodes one line with fresh state, so a subagent update's child
// messages are all new. A scan over a log uses NewDecoder.
func Decode(line string) []driverkit.Event { return NewDecoder()(line) }

// decoder holds the child messages already emitted, keyed by tool call and
// result index.
type decoder struct{ seen map[seenKey]int }

type seenKey struct {
	call   string
	result int
}

// decode turns one line of a pi `--print --mode json` Pass log into canonical
// events. Text that does not parse as a JSON object is stderr sharing the log
// and yields one KindUnparsed; a blank line, a literal `null` or an event type
// outside the grammar yields none.
//
// Only an assistant message_end and a tool_execution_end carry events. pi
// repeats the same assistant message on message_update, turn_end and agent_end,
// and its usage with it, so reading any of those too would count a call more
// than once; the retry events repeat the failed message's errorMessage.
//
// A subagent tool_execution_update or tool_execution_end also unpacks
// details.results[].messages into InSubagent events with the agent as Role,
// each child message once (see decodeSubagent). Reading the update, not just
// the end, keeps the usage of a run killed mid-subagent.
//
// Every field the readers consume is strict except the tool fields and the
// error payload, which are lenient (see the driverkit.Decoder contract).
func (d *decoder) decode(line string) []driverkit.Event {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var l logLine
	if json.Unmarshal([]byte(line), &l) != nil {
		return []driverkit.Event{{Kind: driverkit.KindUnparsed, Text: line}}
	}
	// auto_retry_start/end and agent_end are not decoded: they repeat the failed
	// message_end's errorMessage, which already classifies the failure.
	switch l.Type {
	case "message_end":
		if l.Message.Role == "assistant" {
			return decodeAssistant(l.Message, scope{})
		}
	case "tool_execution_update":
		if l.ToolName == subagentTool {
			children, _ := d.decodeSubagent(l.ToolCallID, l.PartialResult, false)
			return children
		}
	case "tool_execution_end":
		ev := driverkit.Event{
			Kind:       driverkit.KindToolResult,
			ToolID:     l.ToolCallID,
			ToolName:   l.ToolName,
			ToolOutput: resultText(l.Result),
			IsError:    l.IsError,
		}
		if l.ToolName == subagentTool {
			children, agents := d.decodeSubagent(l.ToolCallID, l.Result, true)
			ev.Subagent = agents
			return append(children, ev)
		}
		return []driverkit.Event{ev}
	}
	return nil
}

// subagentTool is the tool name of pi's official subagent extension.
const subagentTool = "subagent"

// subagentResult is the part of a subagent result or partialResult
// decodeSubagent reads. The per-child usage the extension sums beside the
// messages repeats what the child messages already carry, so reading it too
// would count the cost twice.
type subagentResult struct {
	Details subagentDetails `json:"details"`
}

type subagentDetails struct {
	Results []childResult `json:"results"`
}

type childResult struct {
	Agent    string    `json:"agent"`
	Messages []message `json:"messages"`
}

// decodeSubagent returns the child events of a subagent result not yet emitted,
// and the agents it names joined: the model, text, tool calls and usage of a
// child's assistant messages and its tool results, as InSubagent events with
// the agent as Role. The caller puts them ahead of the subagent tool's own
// result, as in claude's log order. The readers keep InSubagent text out of the
// heartbeat narration and the last-message result. A result with no parsable
// details yields nothing.
//
// A child message is appended only once it ends, so a message seen is complete
// and the prefix a cumulative update repeats is skipped by count. The final
// result (the tool_execution_end) forgets the call's counts, so a provider
// reusing a toolCallId for a later subagent call starts that call from zero.
func (d *decoder) decodeSubagent(toolCallID string, raw json.RawMessage, final bool) (children []driverkit.Event, agents string) {
	var res subagentResult
	if json.Unmarshal(raw, &res) != nil {
		return nil, ""
	}
	agents = eachChild(res.Details.Results, func(i int, r childResult) {
		key := seenKey{toolCallID, i}
		children = append(children, childEvents(r, i, "", min(d.seen[key], len(r.Messages)))...)
		if final {
			delete(d.seen, key)
		} else {
			d.seen[key] = max(d.seen[key], len(r.Messages))
		}
	})
	return children, agents
}

// eachChild calls emit for each named result and returns the agents joined, the
// one place the rule for a result without an agent lives.
func eachChild(results []childResult, emit func(i int, r childResult)) string {
	var names []string
	for i, r := range results {
		if r.Agent == "" {
			continue
		}
		names = append(names, r.Agent)
		emit(i, r)
	}
	return strings.Join(names, ",")
}

// childEvents decodes result i's messages from index from on. The index keeps a
// parallel run's children, whose timestamps can collide, and a chain's repeated
// agent apart in the message ids; parentPrefix extends a grandchild's ids past
// its parent's, so they cannot collide with the child's at the same timestamp.
//
// A child that delegates further holds its own subagent toolResult, whose
// details carry the grandchildren's messages: they come out ahead of that
// toolResult event, to any depth. A toolResult is whole once present, so a
// nested one needs no seen-state. The extension appends a child's message only
// when it ends, so a grandchild's usage reaches the log once its parent's
// subagent call completes; a kill during a grandchild's run loses that cost.
func childEvents(r childResult, i int, parentPrefix string, from int) []driverkit.Event {
	prefix := parentPrefix + r.Agent + ":" + strconv.Itoa(i) + ":"
	var events []driverkit.Event
	for _, m := range r.Messages[from:] {
		switch m.Role {
		case "assistant":
			events = append(events, decodeAssistant(m, scope{role: r.Agent, idPrefix: prefix})...)
		case "toolResult":
			ev := driverkit.Event{
				Kind:       driverkit.KindToolResult,
				Role:       r.Agent,
				InSubagent: true,
				ToolID:     m.ToolCallID,
				ToolName:   m.ToolName,
				ToolOutput: contentText(m.Content, driverkit.CompactJSON(m.Content)),
				IsError:    m.IsError,
			}
			if m.ToolName == subagentTool {
				var nested subagentDetails
				if json.Unmarshal(m.Details, &nested) == nil {
					ev.Subagent = eachChild(nested.Results, func(j int, g childResult) {
						events = append(events, childEvents(g, j, prefix, 0)...)
					})
				}
			}
			events = append(events, ev)
		}
	}
	return events
}

// logLine is one Pass-log line. The message's content stays raw because only an
// assistant's is an array (a system message's is a string).
type logLine struct {
	Type       string  `json:"type"`
	Message    message `json:"message"`
	ToolCallID string  `json:"toolCallId"`
	ToolName   string  `json:"toolName"`
	// Result is raw so a malformed one costs only itself rather than the line.
	Result json.RawMessage `json:"result"`
	// PartialResult is a tool_execution_update's counterpart of Result.
	PartialResult json.RawMessage `json:"partialResult"`
	IsError       bool            `json:"isError"`
}

type message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Provider   string          `json:"provider"`
	Model      string          `json:"model"`
	Usage      *usage          `json:"usage"`
	StopReason string          `json:"stopReason"`
	// Raw for the same reason as logLine.Result.
	ErrorMessage  json.RawMessage `json:"errorMessage"`
	Timestamp     int64           `json:"timestamp"`
	DurationMs    float64         `json:"durationMs"`
	ThinkingLevel string          `json:"thinkingLevel"`
	// ToolCallID, ToolName and IsError are a toolResult message's, which only
	// a subagent's child messages carry.
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	IsError    bool   `json:"isError"`
	// Details is a subagent toolResult's own details, raw so a malformed one
	// costs only its nested results.
	Details json.RawMessage `json:"details"`
}

// scope attributes a decoded assistant message: the zero value is the main
// loop, and a child's names its agent and keys its message ids.
type scope struct {
	role     string
	idPrefix string
}

type usage struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cacheRead"`
	CacheWrite int `json:"cacheWrite"`
	Cost       struct {
		Total float64 `json:"total"`
	} `json:"cost"`
}

// block is one assistant content block. Arguments is raw for the same reason.
type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// decodeAssistant maps one assistant message_end, pi's one record per model
// call, to its events: a start, the model in force, the content blocks in
// order, an error for a failed turn and the call's usage. A failed turn has no
// content, so it emits no start and recovers nothing: only a later attempt's
// content shows progress.
//
// The message id is its timestamp: pi's responseId is absent on a failed turn
// and constant across a stub's, so it cannot key the usage dedup or the
// last-message boundary.
func decodeAssistant(m message, sc scope) []driverkit.Event {
	var blocks []block
	if len(m.Content) > 0 && json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	ts := m.Timestamp
	id := ""
	if ts != 0 {
		id = sc.idPrefix + strconv.FormatInt(ts, 10)
	}
	model := observedModel(m)
	var events []driverkit.Event
	if ts != 0 && m.StopReason != "error" {
		events = append(events, driverkit.Event{Kind: driverkit.KindStart, Timestamp: ts, MessageID: id})
	}
	if model != "" {
		events = append(events, driverkit.Event{Kind: driverkit.KindModel, Timestamp: ts, MessageID: id, Model: model, Effort: m.ThinkingLevel})
	}
	for _, b := range blocks {
		switch b.Type {
		case "thinking":
			events = append(events, driverkit.Event{Kind: driverkit.KindReasoning, Timestamp: ts, MessageID: id, Text: b.Thinking})
		case "text":
			events = append(events, driverkit.Event{Kind: driverkit.KindText, Timestamp: ts, MessageID: id, Text: b.Text})
		case "toolCall":
			events = append(events, driverkit.Event{
				Kind:       driverkit.KindToolCall,
				Timestamp:  ts,
				MessageID:  id,
				ToolID:     b.ID,
				ToolName:   b.Name,
				ToolInput:  b.Arguments,
				ToolTarget: driverkit.ToolTarget(toolTargetField, b.Name, b.Arguments),
			})
		}
	}
	if m.StopReason == "error" {
		events = append(events, driverkit.Event{Kind: driverkit.KindError, Timestamp: ts, MessageID: id, Text: driverkit.ErrorText(m.ErrorMessage)})
	}
	if u := m.Usage; u != nil {
		// A failed attempt still reports (zero-token) usage, so it counts as an
		// API call and a turn: pi did make the request.
		events = append(events, driverkit.Event{
			Kind:      driverkit.KindUsage,
			Timestamp: ts + int64(m.DurationMs),
			MessageID: id,
			Model:     model,
			Effort:    m.ThinkingLevel,
			Tokens: driverkit.Tokens{
				Input:        u.Input,
				Output:       u.Output, // reasoning tokens are already inside output
				CacheRead:    u.CacheRead,
				CacheWrite5m: u.CacheWrite, // single un-split count; 5m is the default TTL
				CostUSD:      u.Cost.Total,
			},
		})
	}
	if sc.role == "" {
		return events
	}
	// A child's failure does not fail the run and its reasoning is noise, so only
	// its model, text, tool activity and usage are kept.
	kept := events[:0]
	for _, ev := range events {
		switch ev.Kind {
		case driverkit.KindModel, driverkit.KindText, driverkit.KindToolCall, driverkit.KindUsage:
			ev.Role, ev.InSubagent = sc.role, true
			kept = append(kept, ev)
		}
	}
	return kept
}

// observedModel composes the model id in force as `<provider>/<model>`, the
// form --model names it. The thinking level rides separately as Event.Effort,
// so a mid-Pass level change never splits per-model accounting.
func observedModel(m message) string {
	if m.Model == "" {
		return ""
	}
	id := m.Model
	if m.Provider != "" {
		id = m.Provider + "/" + id
	}
	return id
}

// toolTargetField names, per pi tool, the input field that best identifies
// what the call acted on.
var toolTargetField = map[string]string{
	"bash":  "command",
	"read":  "path",
	"edit":  "path",
	"write": "path",
	"ls":    "path",
	"grep":  "pattern",
	"find":  "pattern",
}

// resultText joins a tool result's text blocks, falling back to the result's
// compact JSON when it holds none (a structured-only result).
func resultText(raw json.RawMessage) string {
	var r struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &r) == nil {
		return contentText(r.Content, driverkit.CompactJSON(raw))
	}
	return driverkit.CompactJSON(raw)
}

// contentText joins a content array's text blocks, or returns fallback when it
// holds none.
func contentText(content json.RawMessage, fallback string) string {
	var blocks []block
	if json.Unmarshal(content, &blocks) == nil {
		var b strings.Builder
		for _, c := range blocks {
			if c.Type == "text" {
				b.WriteString(c.Text)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return fallback
}
