package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/sdk/driver"
	"spindrift.dev/sdk/event"
)

// This file is a frozen encoding/json copy of the native opencode Decode, the
// reference TestDecodeMatchesReference and FuzzDecodeMatchesReference compare
// decode.go against, so it must not change alongside decode.go: it follows
// native, not decode.go. It omits the spindrift_op case: the host decodes those
// lines itself, and the guest decoder only reads them for state.

// refIsErrorEvent reports whether line is a type:"error" event, for ResolveExit.
// It is deliberately narrower than Decode: it decodes only type and part.text,
// so a malformed field elsewhere on an error line (a fractional timestamp, a
// bad token count) still counts as an error, where Decode would drop the whole
// line to KindUnparsed. part.text stays only for parity with the legacy decode:
// an error line whose part.text is not a string is dropped, not counted.
// Native ResolveExit runs Decode only on the remaining lines, to find progress.
func refIsErrorEvent(line string) bool {
	var ev struct {
		Type string `json:"type"`
		Part struct {
			Text string `json:"text"`
		} `json:"part"`
	}
	return json.Unmarshal([]byte(strings.TrimSpace(line)), &ev) == nil && ev.Type == "error"
}

func refDecode(line string) []event.Event {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var l refLogLine
	if json.Unmarshal([]byte(line), &l) != nil {
		return []event.Event{{Kind: event.KindUnparsed, Text: line}}
	}
	ts, part := l.Timestamp, l.Part

	switch l.Type {
	case "step_start":
		return []event.Event{{Kind: event.KindStart, Timestamp: ts, MessageID: part.MessageID}}
	case "text":
		return []event.Event{{Kind: event.KindText, Timestamp: ts, MessageID: part.MessageID, Text: part.Text}}
	case "reasoning":
		return []event.Event{{Kind: event.KindReasoning, Timestamp: ts, MessageID: part.MessageID, Text: part.Text}}
	case "tool_use":
		return refDecodeToolUse(part, ts)
	case "step_finish":
		return refDecodeStepFinish(part, ts)
	case "error":
		return []event.Event{{
			Kind:      event.KindError,
			Timestamp: ts,
			Text:      driver.ErrorText(l.Error),
			ResetAt:   refResetFrom(l.Error, ts),
		}}
	}
	return nil
}

// refLogLine is one Pass-log line.
type refLogLine struct {
	Type      string          `json:"type"`
	Timestamp int64           `json:"timestamp"`
	Error     json.RawMessage `json:"error"`
	Part      refLogPart      `json:"part"`
}

// refLogPart's tool fields are raw so a malformed one costs only itself rather
// than the line.
type refLogPart struct {
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

func refDecodeToolUse(part refLogPart, ts int64) []event.Event {
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
	call := event.Event{
		Kind:       event.KindToolCall,
		Timestamp:  ts,
		MessageID:  part.MessageID,
		ToolID:     callID,
		ToolName:   tool,
		ToolInput:  state.Input,
		ToolTarget: driver.ToolTarget(toolTargetField, tool, state.Input),
	}
	switch state.Status {
	case "completed", "error":
		result := call
		result.Kind = event.KindToolResult
		result.IsError = state.Status == "error"
		raw := state.Output
		if result.IsError {
			raw = state.Error
		}
		result.ToolOutput = refOutputText(raw)
		if tool == subagentSpawnTool {
			result.Subagent = call.ToolTarget
			if result.Subagent == "" {
				result.Subagent = driver.DefaultRole
			}
		}
		return []event.Event{call, result}
	}
	return []event.Event{call}
}

// refOutputText decodes a tool's output or error, falling back to the raw JSON
// when it is not a string, as the claude Driver's transcript does.
func refOutputText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// refDecodeStepFinish maps one step_finish to its usage tally and, when the line
// names a model, a KindModel. Each step_finish is one call's own tally, not a
// running total. Reasoning tokens bill as output; opencode's cache.write has
// no TTL split, so it all lands in the 5m bucket, the Anthropic default TTL.
func refDecodeStepFinish(part refLogPart, ts int64) []event.Event {
	tok, id, model := part.Tokens, part.MessageID, part.ModelID
	events := []event.Event{{
		Kind:      event.KindUsage,
		Timestamp: ts,
		MessageID: id,
		Model:     model,
		Tokens: event.Tokens{
			Input:        tok.Input,
			Output:       tok.Output + tok.Reasoning,
			CacheRead:    tok.Cache.Read,
			CacheWrite5m: tok.Cache.Write,
			CostUSD:      part.Cost,
		},
	}}
	if model != "" {
		events = append(events, event.Event{Kind: event.KindModel, Timestamp: ts, MessageID: id, Model: model})
	}
	return events
}

// refResponseHeaders holds lower-cased header names. Decoding a plain map would
// lose document order, so Retry-After and retry-after would race; walking the
// tokens lets the last one win. encoding/json calls UnmarshalJSON once per
// repeated (case-folded) key, in document order, so the win holds across them.
type refResponseHeaders map[string]string

func (h *refResponseHeaders) UnmarshalJSON(b []byte) error {
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
		*h = refResponseHeaders{}
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

// refResetFrom derives the rate-limit reset time from the response headers of a
// NamedError payload, in the order opencode's own session/retry.ts reads them:
// retry-after-ms, then retry-after as seconds, then retry-after as an HTTP date.
// A relative delay is anchored to the event timestamp rather than a wall clock,
// so the result is deterministic; a relative delay with no timestamp yields nil.
func refResetFrom(raw json.RawMessage, timestampMs int64) *time.Time {
	var payload struct {
		Data struct {
			ResponseHeaders refResponseHeaders `json:"responseHeaders"`
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
