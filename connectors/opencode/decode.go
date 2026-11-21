package main

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/sdk/driver"
	"spindrift.dev/sdk/event"
)

// WriteTool is the opencode CLI's file-write tool name.
const WriteTool = "write"

// newDecoder returns an opencode decoder with fresh state for one scan. Real
// opencode never puts modelID on step_finish, so the decoder attributes each
// untagged step_finish to the model and effort of the last pass_model op until
// the next pass_start, and stamps the pass_start role on the events it tags.
// The host decodes spindrift_op lines itself and discards this decoder's
// output for them, but still hands them over so this state can follow passes;
// Event.HostOp is unpublished, so the op is read from the scanned line.
func newDecoder(time.Time) driver.Decoder {
	var model, effort, role string
	// lastEmitted is the (model, effort, role) the last KindModel carried, so
	// only a change re-emits one; pass_start and pass_model clear it so a new
	// pass re-emits.
	type modelTag struct{ model, effort, role string }
	var lastEmitted modelTag
	return func(line string) []event.Event {
		line = strings.TrimSpace(line)
		if line == "" {
			return nil
		}
		root, ok := driver.Scan(line)
		if !ok {
			return unparsed(line)
		}
		if passRole, ok := driver.HostLineOf(root); ok {
			switch op, m, e, ok := readPassOp(root); {
			case !ok:
			case op == "pass_start":
				model, effort, lastEmitted = "", "", modelTag{}
				// An unmapped role leaves the current one in force.
				if passRole != "" {
					role = passRole
				}
			case op == "pass_model":
				model, effort, lastEmitted = m, e, modelTag{}
			}
			return nil
		}
		events := decodeLine(line, root)
		for i, e := range events {
			switch {
			case e.Kind == event.KindModel || (e.Kind == event.KindUsage && e.Model != ""):
				events[i].Role = role
			case e.Kind == event.KindUsage && e.Model == "" && model != "":
				events[i].Model, events[i].Effort, events[i].Role = model, effort, role
				if cur := (modelTag{model, effort, role}); cur != lastEmitted {
					lastEmitted = cur
					events = append(events, event.Event{Kind: event.KindModel, Timestamp: e.Timestamp, MessageID: e.MessageID, Model: model, Effort: effort, Role: role})
				}
			}
		}
		return events
	}
}

func unparsed(line string) []event.Event {
	return []event.Event{{Kind: event.KindUnparsed, Text: line}}
}

// readPassOp reads op, model and effort from a host line's payload as
// driver.HostLineOf reads op and role, so a payload it accepted merges the same
// way. ok is false when model or effort is mistyped, which the host's own
// decode rejects as a whole.
func readPassOp(root driver.Value) (op, model, effort string, ok bool) {
	for k, m := range root.Members() {
		if !driver.KeyMatches(k, "spindrift_op") {
			continue
		}
		if m.Kind() == driver.Null {
			op, model, effort = "", "", ""
		}
		for pk, pv := range m.Members() {
			switch {
			case driver.KeyMatches(pk, "op"):
				pv.DecodeString(&op)
			case driver.KeyMatches(pk, "model"):
				if !pv.DecodeString(&model) {
					return "", "", "", false
				}
			case driver.KeyMatches(pk, "effort"):
				if !pv.DecodeString(&effort) {
					return "", "", "", false
				}
			}
		}
	}
	return op, model, effort, true
}

// decodeLine turns one scanned, non-host line of an opencode `--format json`
// Pass log into canonical events. A line that fails to decode as a whole is
// KindUnparsed, as is stderr sharing the log; a literal `null` or an unknown
// event type yields none.
//
// Every field the readers consume is strict except the tool fields and the
// error payload, which are lenient (see the event vocabulary's Decoder
// contract).
func decodeLine(line string, root driver.Value) []event.Event {
	evs, ok := decodeRoot(root)
	if !ok {
		return unparsed(line)
	}
	return evs
}

// decodeRoot decodes root into canonical events; ok is false when the line
// fails to decode as a whole, for a caller that has no line to hand back as
// KindUnparsed.
func decodeRoot(root driver.Value) (evs []event.Event, ok bool) {
	if !objectOrNull(root) {
		return nil, false
	}
	l, ok := readLine(root)
	if !ok {
		return nil, false
	}
	return decodeFields(l), true
}

func decodeFields(l logLine) []event.Event {
	ts, part := l.Timestamp, l.Part
	switch l.Type {
	case "step_start":
		return []event.Event{{Kind: event.KindStart, Timestamp: ts, MessageID: part.MessageID}}
	case "text":
		return []event.Event{{Kind: event.KindText, Timestamp: ts, MessageID: part.MessageID, Text: part.Text}}
	case "reasoning":
		return []event.Event{{Kind: event.KindReasoning, Timestamp: ts, MessageID: part.MessageID, Text: part.Text}}
	case "tool_use":
		return decodeToolUse(part, ts)
	case "step_finish":
		return decodeStepFinish(part, ts)
	case "error":
		raw := json.RawMessage(l.Error.Raw())
		return []event.Event{{
			Kind:      event.KindError,
			Timestamp: ts,
			Text:      driver.ErrorText(raw),
			ResetAt:   resetFrom(l.Error, ts),
		}}
	}
	return nil
}

func objectOrNull(v driver.Value) bool {
	k := v.Kind()
	return k == driver.Object || k == driver.Null
}

// logLine is one Pass-log line.
type logLine struct {
	Type      string
	Timestamp int64
	Error     driver.Value
	Part      logPart
}

// logPart's tool fields stay scanned values so a malformed one costs only
// itself rather than the line.
type logPart struct {
	MessageID, ModelID, Text string
	Cost                     float64
	Input, Output, Reasoning int
	CacheWrite, CacheRead    int
	CallID, Tool, State      driver.Value
}

// readLine reads root, an object or null, as json.Unmarshal would fill a
// logLine: keys match case-insensitively, a repeated key overwrites or merges,
// and a mistyped field fails the line (ok false).
func readLine(root driver.Value) (l logLine, ok bool) {
	for k, v := range root.Members() {
		ok := true
		switch {
		case driver.KeyMatches(k, "type"):
			ok = v.DecodeString(&l.Type)
		case driver.KeyMatches(k, "timestamp"):
			var ts int
			if ok = v.DecodeInt(&ts); ok && v.Kind() == driver.Number {
				l.Timestamp = int64(ts)
			}
		case driver.KeyMatches(k, "error"):
			l.Error = v
		case driver.KeyMatches(k, "part"):
			ok = readPart(v, &l.Part)
		}
		if !ok {
			return logLine{}, false
		}
	}
	return l, true
}

func readPart(v driver.Value, p *logPart) bool {
	if !objectOrNull(v) {
		return false
	}
	for k, m := range v.Members() {
		ok := true
		switch {
		case driver.KeyMatches(k, "messageID"):
			ok = m.DecodeString(&p.MessageID)
		case driver.KeyMatches(k, "modelID"):
			ok = m.DecodeString(&p.ModelID)
		case driver.KeyMatches(k, "text"):
			ok = m.DecodeString(&p.Text)
		case driver.KeyMatches(k, "cost"):
			ok = m.DecodeFloat(&p.Cost)
		case driver.KeyMatches(k, "tokens"):
			ok = readTokens(m, p)
		case driver.KeyMatches(k, "callID"):
			p.CallID = m
		case driver.KeyMatches(k, "tool"):
			p.Tool = m
		case driver.KeyMatches(k, "state"):
			p.State = m
		}
		if !ok {
			return false
		}
	}
	return true
}

func readTokens(v driver.Value, p *logPart) bool {
	if !objectOrNull(v) {
		return false
	}
	for k, m := range v.Members() {
		ok := true
		switch {
		case driver.KeyMatches(k, "input"):
			ok = m.DecodeInt(&p.Input)
		case driver.KeyMatches(k, "output"):
			ok = m.DecodeInt(&p.Output)
		case driver.KeyMatches(k, "reasoning"):
			ok = m.DecodeInt(&p.Reasoning)
		case driver.KeyMatches(k, "cache"):
			if ok = objectOrNull(m); ok {
				for ck, cv := range m.Members() {
					switch {
					case driver.KeyMatches(ck, "write"):
						ok = ok && cv.DecodeInt(&p.CacheWrite)
					case driver.KeyMatches(ck, "read"):
						ok = ok && cv.DecodeInt(&p.CacheRead)
					}
				}
			}
		}
		if !ok {
			return false
		}
	}
	return true
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

// toolTarget is driver.ToolTarget over a scanned input, so a large tool input
// is not parsed a second time. The key is matched exactly, as a map key is.
func toolTarget(tool string, input driver.Value) string {
	name, ok := toolTargetField[tool]
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

func rawOf(v driver.Value) json.RawMessage {
	if v.Kind() == driver.Invalid {
		return nil
	}
	return json.RawMessage(v.Raw())
}

func decodeToolUse(part logPart, ts int64) []event.Event {
	// The tool fields are lenient: a mistyped member costs only itself.
	var status string
	var input, output, errValue driver.Value
	for k, v := range part.State.Members() {
		switch {
		case driver.KeyMatches(k, "status"):
			v.DecodeString(&status)
		case driver.KeyMatches(k, "input"):
			input = v
		case driver.KeyMatches(k, "output"):
			output = v
		case driver.KeyMatches(k, "error"):
			errValue = v
		}
	}
	var callID, tool string
	part.CallID.DecodeString(&callID)
	part.Tool.DecodeString(&tool)
	call := event.Event{
		Kind:       event.KindToolCall,
		Timestamp:  ts,
		MessageID:  part.MessageID,
		ToolID:     callID,
		ToolName:   tool,
		ToolInput:  rawOf(input),
		ToolTarget: toolTarget(tool, input),
	}
	switch status {
	case "completed", "error":
		result := call
		result.Kind = event.KindToolResult
		result.IsError = status == "error"
		raw := output
		if result.IsError {
			raw = errValue
		}
		result.ToolOutput = outputText(raw)
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

// outputText decodes a tool's output or error, falling back to the raw JSON
// when it is not a string, as the claude Driver's transcript does.
func outputText(v driver.Value) string {
	switch v.Kind() {
	case driver.Invalid, driver.Null:
		return ""
	case driver.String:
		var s string
		v.DecodeString(&s)
		return s
	}
	return v.Raw()
}

// decodeStepFinish maps one step_finish to its usage tally and, when the line
// names a model, a KindModel. Each step_finish is one call's own tally, not a
// running total. Reasoning tokens bill as output; opencode's cache.write has
// no TTL split, so it all lands in the 5m bucket, the Anthropic default TTL.
func decodeStepFinish(part logPart, ts int64) []event.Event {
	id, model := part.MessageID, part.ModelID
	events := []event.Event{{
		Kind:      event.KindUsage,
		Timestamp: ts,
		MessageID: id,
		Model:     model,
		Tokens: event.Tokens{
			Input:        part.Input,
			Output:       part.Output + part.Reasoning,
			CacheRead:    part.CacheRead,
			CacheWrite5m: part.CacheWrite,
			CostUSD:      part.Cost,
		},
	}}
	if model != "" {
		events = append(events, event.Event{Kind: event.KindModel, Timestamp: ts, MessageID: id, Model: model})
	}
	return events
}

// resetFrom derives the rate-limit reset time from the response headers of a
// NamedError payload, in opencode's session/retry.ts order: retry-after-ms,
// then retry-after as seconds, then as an HTTP date. A relative delay is
// anchored to the event timestamp, not a wall clock, so the result is
// deterministic; a relative delay with no timestamp, or unreadable headers,
// yields nil.
func resetFrom(payload driver.Value, timestampMs int64) *time.Time {
	headers := map[string]string{}
	if !readHeaders(payload, headers) {
		return nil
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

// readHeaders fills headers with the lower-cased, trimmed response headers of
// an error payload, as json.Unmarshal would fill a struct holding a
// map[string]string; it reports false when any level is mistyped.
func readHeaders(payload driver.Value, headers map[string]string) bool {
	if !objectOrNull(payload) {
		return false
	}
	for k, data := range payload.Members() {
		if !driver.KeyMatches(k, "data") {
			continue
		}
		if !objectOrNull(data) {
			return false
		}
		for dk, rh := range data.Members() {
			if !driver.KeyMatches(dk, "responseHeaders") {
				continue
			}
			if !objectOrNull(rh) {
				return false
			}
			for hk, hv := range rh.Members() {
				var s string
				if !hv.DecodeString(&s) {
					return false
				}
				headers[strings.ToLower(hk)] = strings.TrimSpace(s)
			}
		}
	}
	return true
}
