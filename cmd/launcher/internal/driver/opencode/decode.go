package opencode

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/dispatchkind"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
)

// WriteTool is the opencode CLI's file-write tool name.
const WriteTool = "write"

// NewDecoder returns an opencode decoder with fresh state for one scan. Real
// opencode never puts modelID on step_finish, so the decoder attributes each
// untagged step_finish to the model and effort driver-exec's pass_model op
// announced, until the next pass_start, and stamps the pass_start's role
// (dispatchkind.PassRole) on the KindModel and KindUsage events it tags.
// Decode alone is stateless and never sees either.
func NewDecoder() driverkit.Decoder {
	var model, effort, role string
	// lastEmitted is the (model, effort, role) the last KindModel carried, so
	// only a change re-emits one; pass_start and pass_model clear it so a new
	// pass re-emits.
	type modelTag struct{ model, effort, role string }
	var lastEmitted modelTag
	return func(line string) []driverkit.Event {
		events := Decode(line)
		for i, e := range events {
			switch {
			case e.Kind == driverkit.KindHostOp && e.HostOp.Op == hostop.OpPassStart:
				model, effort, lastEmitted = "", "", modelTag{}
				// An unmapped role leaves the current one in force.
				if r := dispatchkind.PassRole(e.HostOp.Role); r != "" {
					role = r
				}
			case e.Kind == driverkit.KindHostOp && e.HostOp.Op == hostop.OpPassModel:
				model, effort, lastEmitted = e.HostOp.Model, e.HostOp.Effort, modelTag{}
			case e.Kind == driverkit.KindModel || (e.Kind == driverkit.KindUsage && e.Model != ""):
				events[i].Role = role
			case e.Kind == driverkit.KindUsage && e.Model == "" && model != "":
				events[i].Model, events[i].Effort, events[i].Role = model, effort, role
				if cur := (modelTag{model, effort, role}); cur != lastEmitted {
					lastEmitted = cur
					events = append(events, driverkit.Event{Kind: driverkit.KindModel, Timestamp: e.Timestamp, MessageID: e.MessageID, Model: model, Effort: effort, Role: role})
				}
			}
		}
		return events
	}
}

// isErrorEvent reports whether line is a type:"error" event, for ResolveExit.
// It decodes only type and part.text, so a malformed field elsewhere (a
// fractional timestamp, a bad token count) still counts, where Decode would
// drop the line to KindUnparsed. part.text stays only for parity with the
// legacy decode: an error line whose part.text is not a string does not
// count. ResolveExit runs Decode only on the other lines.
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
			Text:      driverkit.ErrorText(l.Error),
			ResetAt:   resetFrom(l.Error, ts),
		}}
	case "spindrift_op":
		var op hostop.Event
		if json.Unmarshal([]byte(line), &op) != nil {
			return []driverkit.Event{{Kind: driverkit.KindUnparsed, Text: line}}
		}
		if op.SpindriftOp == nil {
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
		ToolTarget: driverkit.ToolTarget(toolTargetField, tool, state.Input),
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

// responseHeaders holds lower-cased header names. Decoding a plain map would
// lose document order, so Retry-After and retry-after would race; walking the
// tokens lets the last one win. encoding/json calls UnmarshalJSON once per
// repeated (case-folded) key, in document order, so the win holds across them.
type responseHeaders map[string]string

func (h *responseHeaders) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return errors.New("responseHeaders: not an object")
	}
	if *h == nil {
		*h = responseHeaders{}
	}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return err
		}
		var v string
		if err := dec.Decode(&v); err != nil {
			return err
		}
		(*h)[strings.ToLower(k.(string))] = strings.TrimSpace(v)
	}
	return nil
}

// resetFrom derives the rate-limit reset time from the response headers of a
// NamedError payload, in the order opencode's own session/retry.ts reads them:
// retry-after-ms, then retry-after as seconds, then retry-after as an HTTP date.
// A relative delay is anchored to the event timestamp rather than a wall clock,
// so the result is deterministic; a relative delay with no timestamp yields nil.
func resetFrom(raw json.RawMessage, timestampMs int64) *time.Time {
	var payload struct {
		Data struct {
			ResponseHeaders responseHeaders `json:"responseHeaders"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return nil
	}
	headers := payload.Data.ResponseHeaders
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
