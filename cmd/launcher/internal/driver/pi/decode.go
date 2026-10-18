package pi

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/driver/driverkit"
)

// WriteTool is the pi CLI's file-write tool name.
const WriteTool = "write"

// NewDecoder returns the pi decoder. Decode is stateless, so every call shares
// it.
func NewDecoder() driverkit.Decoder { return Decode }

// Decode turns one line of a pi `--print --mode json` Pass log into canonical
// events. Text that does not parse as a JSON object is stderr sharing the log
// and yields one KindUnparsed; a blank line, a literal `null` or an event type
// outside the grammar yields none.
//
// Only an assistant message_end and a tool_execution_end carry events. pi
// repeats the same assistant message on message_update, turn_end and agent_end,
// and its usage with it, so reading any of those too would count a call more
// than once; the retry events repeat the failed message's errorMessage.
//
// A subagent tool_execution_end also unpacks details.results[].messages into
// InSubagent events with the agent as Role (see decodeSubagent).
//
// Every field the readers consume is strict except the tool fields and the
// error payload, which are lenient (see the driverkit.Decoder contract).
func Decode(line string) []driverkit.Event {
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
	case "tool_execution_end":
		ev := driverkit.Event{
			Kind:       driverkit.KindToolResult,
			ToolID:     l.ToolCallID,
			ToolName:   l.ToolName,
			ToolOutput: resultText(l.Result),
			IsError:    l.IsError,
		}
		if l.ToolName == subagentTool {
			return decodeSubagent(ev, l.Result)
		}
		return []driverkit.Event{ev}
	}
	return nil
}

// subagentTool is the tool name of pi's official subagent extension.
const subagentTool = "subagent"

// subagentResult is the part of a subagent tool_execution_end result Decode
// reads. The per-child usage the extension sums beside the messages, and its
// tool_execution_update events, repeat what the child messages already carry,
// so reading them too would count the cost twice.
type subagentResult struct {
	Details struct {
		Results []struct {
			Agent    string    `json:"agent"`
			Messages []message `json:"messages"`
		} `json:"results"`
	} `json:"details"`
}

// decodeSubagent returns each child's events, then the subagent tool's own
// result tagged with the agents it ran: the model, text, tool calls and usage
// of a child's assistant messages and its tool results, as InSubagent events
// with the agent as Role, ahead of the result as in claude's log order. The
// readers keep InSubagent text out of the heartbeat narration and the
// last-message result. A result with no parsable details yields the bare tool
// result.
func decodeSubagent(res driverkit.Event, raw json.RawMessage) []driverkit.Event {
	var d subagentResult
	if json.Unmarshal(raw, &d) != nil {
		return []driverkit.Event{res}
	}
	var agents []string
	var children []driverkit.Event
	for i, r := range d.Details.Results {
		if r.Agent == "" {
			continue
		}
		agents = append(agents, r.Agent)
		for _, m := range r.Messages {
			switch m.Role {
			case "assistant":
				// The index keeps a parallel run's children, whose timestamps can
				// collide, and a chain's repeated agent apart.
				children = append(children, decodeAssistant(m, scope{role: r.Agent, idPrefix: r.Agent + ":" + strconv.Itoa(i) + ":"})...)
			case "toolResult":
				children = append(children, driverkit.Event{
					Kind:       driverkit.KindToolResult,
					Role:       r.Agent,
					InSubagent: true,
					ToolID:     m.ToolCallID,
					ToolName:   m.ToolName,
					ToolOutput: contentText(m.Content, compact(m.Content)),
					IsError:    m.IsError,
				})
			}
		}
	}
	res.Subagent = strings.Join(agents, ",")
	return append(children, res)
}

// logLine is one Pass-log line. The message's content stays raw because only an
// assistant's is an array (a system message's is a string).
type logLine struct {
	Type       string  `json:"type"`
	Message    message `json:"message"`
	ToolCallID string  `json:"toolCallId"`
	ToolName   string  `json:"toolName"`
	// Result is raw so a malformed one costs only itself rather than the line.
	Result  json.RawMessage `json:"result"`
	IsError bool            `json:"isError"`
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
		events = append(events, driverkit.Event{Kind: driverkit.KindModel, Timestamp: ts, MessageID: id, Model: model})
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
				ToolTarget: toolTarget(b.Name, b.Arguments),
			})
		}
	}
	if m.StopReason == "error" {
		events = append(events, driverkit.Event{Kind: driverkit.KindError, Timestamp: ts, MessageID: id, Text: errorText(m.ErrorMessage)})
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

// observedModel composes the model id in force the way pi itself writes one,
// `<provider>/<model>:<thinkingLevel>` (the form --model accepts). The level
// rides in the id because the heartbeat and the Dispatch Record's model list
// carry only an id, so this is how they show the effort actually applied.
func observedModel(m message) string {
	if m.Model == "" {
		return ""
	}
	id := m.Model
	if m.Provider != "" {
		id = m.Provider + "/" + id
	}
	if m.ThinkingLevel != "" {
		id += ":" + m.ThinkingLevel
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

// toolTarget returns the string value of the tool's identifying input field,
// "" for an unknown tool, a missing field or a non-string one.
func toolTarget(tool string, input json.RawMessage) string {
	field, ok := toolTargetField[tool]
	if !ok {
		return ""
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(input, &fields) != nil {
		return ""
	}
	var target string
	_ = json.Unmarshal(fields[field], &target)
	return target
}

// resultText joins a tool result's text blocks, falling back to the result's
// compact JSON when it holds none (a structured-only result), as the opencode
// Driver's transcript does.
func resultText(raw json.RawMessage) string {
	var r struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &r) == nil {
		return contentText(r.Content, compact(raw))
	}
	return compact(raw)
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

// errorText returns the string payload unquoted, or any other payload in
// compact JSON. A missing or invalid payload yields "", which matches no
// marker.
func errorText(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	return compact(raw)
}

func compact(raw json.RawMessage) string {
	var buf bytes.Buffer
	if len(raw) == 0 || string(raw) == "null" || json.Compact(&buf, raw) != nil {
		return ""
	}
	return buf.String()
}
