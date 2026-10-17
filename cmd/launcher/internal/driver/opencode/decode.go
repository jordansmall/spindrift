package opencode

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
)

// NewDecoder returns the opencode decoder. Decode is stateless, so every call
// shares it.
func NewDecoder() driverkit.Decoder { return Decode }

// isErrorEvent reports whether line is a type:"error" event, for ResolveExit.
// It is deliberately narrower than Decode: it decodes only type and part.text,
// so a malformed field elsewhere on an error line (a fractional timestamp, a
// bad token count) still counts as an error, where Decode would drop the whole
// line to KindUnparsed. part.text stays only for parity with the legacy decode:
// an error line whose part.text is not a string is dropped, not counted.
// ResolveExit runs Decode only on the remaining lines, to find progress.
func isErrorEvent(line string) bool {
	var ev struct {
		Type string `json:"type"`
		Part struct {
			Text string `json:"text"`
		} `json:"part"`
	}
	return json.Unmarshal([]byte(strings.TrimSpace(line)), &ev) == nil && ev.Type == "error"
}

// Decode turns one line of an opencode `--format json` Pass log into canonical
// events. Text that does not parse as a JSON object is stderr sharing the log
// and yields one KindUnparsed; a blank line, a literal `null` or an unknown
// event type yields none.
//
// The line decodes as one struct, the union of the fields the readers consume,
// so each reader keeps its drop-the-line semantics: a line with any of those
// fields malformed is KindUnparsed whole. Tool fields no reader inspects stay
// lenient.
func Decode(line string) []driverkit.Event {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var l logLine
	if json.Unmarshal([]byte(line), &l) != nil {
		return []driverkit.Event{{Kind: driverkit.KindUnparsed, Text: line}}
	}
	ts, part := l.Timestamp, l.Part

	switch l.Type {
	case "step_start":
		return []driverkit.Event{{Kind: driverkit.KindStart, Timestamp: ts, MessageID: part.MessageID}}
	case "text":
		return []driverkit.Event{{Kind: driverkit.KindText, Timestamp: ts, MessageID: part.MessageID, Text: part.Text}}
	case "reasoning":
		return []driverkit.Event{{Kind: driverkit.KindReasoning, Timestamp: ts, MessageID: part.MessageID, Text: part.Text}}
	case "tool_use":
		return decodeToolUse(part, ts)
	case "step_finish":
		return decodeStepFinish(part, ts)
	case "error":
		return []driverkit.Event{{
			Kind:      driverkit.KindError,
			Timestamp: ts,
			Text:      errorText(l.Error),
			ResetAt:   resetFrom(l.Error, ts),
		}}
	case "spindrift_op":
		var op hostop.Event
		if json.Unmarshal([]byte(line), &op) != nil || op.SpindriftOp == nil {
			return nil
		}
		return []driverkit.Event{{Kind: driverkit.KindHostOp, Timestamp: ts, HostOp: op.SpindriftOp}}
	}
	return nil
}

// logLine is one Pass-log line.
type logLine struct {
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp"`
	Error     json.RawMessage `json:"error"`
	Part      logPart         `json:"part"`
}

// logPart's tool fields are raw so a malformed one costs only itself rather
// than the line.
type logPart struct {
	MessageID string  `json:"messageID"`
	ModelID   string  `json:"modelID"`
	Text      string  `json:"text"`
	Cost      float64 `json:"cost"`
	Tokens    struct {
		Input     int `json:"input"`
		Output    int `json:"output"`
		Reasoning int `json:"reasoning"`
		Cache     struct {
			Write int `json:"write"`
			Read  int `json:"read"`
		} `json:"cache"`
	} `json:"tokens"`

	CallID json.RawMessage `json:"callID"`
	Tool   json.RawMessage `json:"tool"`
	State  json.RawMessage `json:"state"`
}

// subagentSpawnTool is the opencode tool whose call runs a subagent.
const subagentSpawnTool = "task"

// toolTargetField names, per opencode tool, the input field that best
// identifies what the call acted on.
var toolTargetField = map[string]string{
	"bash":            "command",
	"read":            "filePath",
	"edit":            "filePath",
	"write":           "filePath",
	"glob":            "pattern",
	"grep":            "pattern",
	"webfetch":        "url",
	"websearch":       "query",
	subagentSpawnTool: "subagent_type",
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

func decodeToolUse(part logPart, ts int64) []driverkit.Event {
	var state struct {
		Status string          `json:"status"`
		Input  json.RawMessage `json:"input"`
		Output json.RawMessage `json:"output"`
		Error  json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(part.State, &state)
	var callID, tool string
	_ = json.Unmarshal(part.CallID, &callID)
	_ = json.Unmarshal(part.Tool, &tool)
	call := driverkit.Event{
		Kind:       driverkit.KindToolCall,
		Timestamp:  ts,
		MessageID:  part.MessageID,
		ToolID:     callID,
		ToolName:   tool,
		ToolInput:  state.Input,
		ToolTarget: toolTarget(tool, state.Input),
	}
	switch state.Status {
	case "completed", "error":
		result := call
		result.Kind = driverkit.KindToolResult
		result.IsError = state.Status == "error"
		raw := state.Output
		if result.IsError {
			raw = state.Error
		}
		result.ToolOutput = outputText(raw)
		if tool == subagentSpawnTool {
			result.Subagent = call.ToolTarget
			if result.Subagent == "" {
				result.Subagent = driverkit.DefaultRole
			}
		}
		return []driverkit.Event{call, result}
	}
	return []driverkit.Event{call}
}

// outputText decodes a tool's output or error, falling back to the raw JSON
// when it is not a string, as the claude Driver's transcript does.
func outputText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// decodeStepFinish maps one step_finish to its usage tally and, when the line
// names a model, a KindModel. Each step_finish is one call's own tally, not a
// running total. Reasoning tokens bill as output; opencode's cache.write has
// no TTL split, so it all lands in the 5m bucket, the Anthropic default TTL.
func decodeStepFinish(part logPart, ts int64) []driverkit.Event {
	tok, id, model := part.Tokens, part.MessageID, part.ModelID
	events := []driverkit.Event{{
		Kind:      driverkit.KindUsage,
		Timestamp: ts,
		MessageID: id,
		Model:     model,
		Tokens: driverkit.Tokens{
			Input:        tok.Input,
			Output:       tok.Output + tok.Reasoning,
			CacheRead:    tok.Cache.Read,
			CacheWrite5m: tok.Cache.Write,
			CostUSD:      part.Cost,
		},
	}}
	if model != "" {
		events = append(events, driverkit.Event{Kind: driverkit.KindModel, Timestamp: ts, MessageID: id, Model: model})
	}
	return events
}

// resetFrom derives the rate-limit reset time from the response headers of a
// NamedError payload, in the order opencode's own session/retry.ts reads them:
// retry-after-ms, then retry-after as seconds, then retry-after as an HTTP date.
// A relative delay is anchored to the event timestamp rather than a wall clock,
// so the result is deterministic; a relative delay with no timestamp yields nil.
func resetFrom(raw json.RawMessage, timestampMs int64) *time.Time {
	var payload struct {
		Data struct {
			ResponseHeaders map[string]string `json:"responseHeaders"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	headers := map[string]string{}
	for k, v := range payload.Data.ResponseHeaders {
		headers[strings.ToLower(k)] = strings.TrimSpace(v)
	}
	relative := func(value string, unit time.Duration) *time.Time {
		// MaxInt32 keeps n*unit inside time.Duration's int64 range for either unit.
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || !(n >= 0 && n <= math.MaxInt32) || timestampMs <= 0 {
			return nil
		}
		t := time.UnixMilli(timestampMs).Add(time.Duration(n * float64(unit))).UTC()
		return &t
	}
	if t := relative(headers["retry-after-ms"], time.Millisecond); t != nil {
		return t
	}
	if t := relative(headers["retry-after"], time.Second); t != nil {
		return t
	}
	if t, err := http.ParseTime(headers["retry-after"]); err == nil {
		t = t.UTC()
		return &t
	}
	return nil
}

// errorText returns the string payload unquoted, or any other payload in
// compact JSON so a marker like `"statusCode":429` has no stray whitespace. A
// missing or invalid payload yields "", which matches no marker.
func errorText(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str
	}
	var buf bytes.Buffer
	if json.Compact(&buf, raw) != nil {
		return ""
	}
	return buf.String()
}
