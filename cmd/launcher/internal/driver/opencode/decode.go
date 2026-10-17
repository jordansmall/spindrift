package opencode

import (
	"encoding/json"
	"strings"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
)

// NewDecoder returns the opencode decoder. Decode is stateless, so every call
// shares it.
func NewDecoder() driverkit.Decoder { return Decode }

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

func decodeToolUse(part logPart, ts int64) []driverkit.Event {
	var state struct {
		Status string          `json:"status"`
		Input  json.RawMessage `json:"input"`
	}
	_ = json.Unmarshal(part.State, &state)
	var callID, tool string
	_ = json.Unmarshal(part.CallID, &callID)
	_ = json.Unmarshal(part.Tool, &tool)
	call := driverkit.Event{
		Kind:      driverkit.KindToolCall,
		Timestamp: ts,
		MessageID: part.MessageID,
		ToolID:    callID,
		ToolName:  tool,
		ToolInput: state.Input,
	}
	switch state.Status {
	case "completed", "error":
		result := call
		result.Kind = driverkit.KindToolResult
		result.IsError = state.Status == "error"
		return []driverkit.Event{call, result}
	}
	return []driverkit.Event{call}
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
