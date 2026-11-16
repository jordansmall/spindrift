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

// This file is a frozen encoding/json copy of the pre-#7107 decoder: the
// reference TestDecodeMatchesReference and FuzzDecodeMatchesReference compare
// decode.go against, so it must not change alongside decode.go. It shares the
// live helpers driver.HostLine, matchMarker and extractResetsAt, so on host
// lines it compares the scanner with itself; sdk/driver/hostline_test.go pins
// that path against encoding/json.

func newRefDecoder(now time.Time) driver.Decoder {
	d := &refDecoder{
		now:       now,
		taskRole:  map[string]string{},
		toolNames: map[string]string{},
	}
	return d.decode
}

type refDecoder struct {
	now time.Time

	taskRole  map[string]string
	toolNames map[string]string
	topRole   string

	echoReason  driver.Reason
	echoPending bool
}

func (d *refDecoder) decode(line string) []event.Event {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	top := refParseTopLevel(line)
	evs := d.refDecodeLine(line, top)
	if ts, ok := refTopLevelTimestamp(line, top); ok {
		for i := range evs {
			evs[i].Timestamp = ts
		}
	}
	return evs
}

type refTopLevel struct {
	Type      string          `json:"type"`
	Error     json.RawMessage `json:"error"`
	Message   json.RawMessage `json:"message"`
	Timestamp json.RawMessage `json:"timestamp"`
}

func refParseTopLevel(line string) *refTopLevel {
	var top refTopLevel
	if json.Unmarshal([]byte(line), &top) != nil {
		return nil
	}
	return &top
}
func refDecodeField(raw json.RawMessage, v any) bool {
	return len(raw) == 0 || json.Unmarshal(raw, v) == nil
}
func refTopLevelTimestamp(line string, top *refTopLevel) (int64, bool) {
	if top == nil || !strings.Contains(line, `"timestamp"`) {
		return 0, false
	}
	var stamp string
	if top.Type == "" || !refDecodeField(top.Timestamp, &stamp) || stamp == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}
func (d *refDecoder) refDecodeLine(line string, top *refTopLevel) []event.Event {
	if role, ok := driver.HostLine(line); ok {
		if role != "" {
			d.topRole = role
		}
		return nil
	}

	content, synthetic := refClassifyTurn(top)
	switch {
	case content:
		d.echoReason, d.echoPending = matchMarker(line)
		return append([]event.Event{{Kind: event.KindStart}}, d.refContentEvents(line, false)...)
	case synthetic:
		return append([]event.Event{d.refErrorEvent(line)}, d.refContentEvents(line, true)...)
	}

	var env struct {
		Type   string          `json:"type"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal([]byte(line), &env) != nil || env.Type != "result" {
		return []event.Event{d.refErrorEvent(line)}
	}
	var text string
	textOK := len(env.Result) == 0 || json.Unmarshal(env.Result, &text) == nil
	var flags struct {
		IsError bool `json:"is_error"`
	}
	notError := json.Unmarshal([]byte(line), &flags) == nil && !flags.IsError
	var tot struct {
		TotalCostUSD  float64       `json:"total_cost_usd"`
		DurationMs    int64         `json:"duration_ms"`
		DurationAPIMs int64         `json:"duration_api_ms"`
		Usage         refTokenUsage `json:"usage"`
	}
	result := event.Event{Kind: event.KindResult, Text: text}
	if json.Unmarshal([]byte(line), &tot) == nil {
		result.Tokens = event.Tokens{
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
	var turns struct {
		NumTurns int `json:"num_turns"`
	}
	if json.Unmarshal([]byte(line), &turns) == nil {
		result.NumTurns = turns.NumTurns
	} else {
		result.Tokens = event.Tokens{}
		result.DurationMs, result.DurationAPIMs = 0, 0
		result.NoTotals, result.NoTurns = true, true
	}
	var mu struct {
		ModelUsage map[string]json.RawMessage `json:"modelUsage"`
	}
	if json.Unmarshal([]byte(line), &mu) == nil {
		result.Models = slices.Sorted(maps.Keys(mu.ModelUsage))
	}
	var mo struct {
		ModelUsage map[string]struct {
			OutputTokens int `json:"outputTokens"`
		} `json:"modelUsage"`
	}
	if json.Unmarshal([]byte(line), &mo) == nil && len(mo.ModelUsage) > 0 {
		result.ModelOutput = make(map[string]int, len(mo.ModelUsage))
		for model, u := range mo.ModelUsage {
			result.ModelOutput[model] = u.OutputTokens
		}
	}
	var sess struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(line), &sess) == nil {
		result.SessionID = sess.SessionID
	}
	out := []event.Event{result}
	if d.echoPending && textOK && notError {
		d.echoPending = false
		if reason, matched := matchMarker(text); matched && reason == d.echoReason {
			return out
		}
	}
	return append(out, d.refErrorEvent(line))
}

func (d *refDecoder) refErrorEvent(line string) event.Event {
	return event.Event{Kind: event.KindError, Text: line, ResetAt: extractResetsAt(line, d.now)}
}

type refBlock struct {
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

func (d *refDecoder) refContentEvents(line string, synthetic bool) []event.Event {
	var env struct {
		Type            string `json:"type"`
		ParentToolUseID string `json:"parent_tool_use_id"`
		Message         *struct {
			ID      string          `json:"id"`
			Model   string          `json:"model"`
			Content json.RawMessage `json:"content"`
			Usage   refTokenUsage   `json:"usage"`
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
	blocks := make([]refBlock, 0, len(raws))
	for _, raw := range raws {
		var b refBlock
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		blocks = append(blocks, b)
	}
	if env.Type == "assistant" {
		d.refRecordSpawns(blocks)
	}

	subagent := env.ParentToolUseID != ""
	role := d.topRole
	if subagent {
		role = d.refSubagentRole(env.ParentToolUseID)
	}

	var out []event.Event
	if env.Type == "assistant" {
		out = append(out, event.Event{
			Kind: event.KindModel, MessageID: env.Message.ID, Model: env.Message.Model,
			Role: role, InSubagent: subagent,
			Synthetic: synthetic || env.Message.Model == syntheticModelSentinel,
		})
	}
	for _, b := range blocks {
		ev := event.Event{MessageID: env.Message.ID, Role: role, InSubagent: subagent, Synthetic: synthetic}
		switch b.Type {
		case "text":
			if env.Type != "assistant" {
				continue
			}
			ev.Kind, ev.Text = event.KindText, b.Text
		case "thinking":
			var thinking string
			if len(b.Thinking) > 0 && json.Unmarshal(b.Thinking, &thinking) != nil {
				continue
			}
			ev.Kind, ev.Text = event.KindReasoning, thinking
		case "tool_use":
			if env.Type != "assistant" {
				continue
			}
			d.toolNames[b.ID] = b.Name
			ev.Kind, ev.ToolID, ev.ToolName, ev.ToolInput = event.KindToolCall, b.ID, b.Name, b.Input
			ev.ToolTarget = driver.ToolTarget(toolTargetField, b.Name, b.Input)
		case "tool_result":
			if env.Type != "user" {
				continue
			}
			ev.Kind, ev.ToolID, ev.ToolName, ev.IsError = event.KindToolResult, b.ToolUseID, d.toolNames[b.ToolUseID], b.IsError
			ev.ToolOutput = refResultText(b.Content)
			ev.Subagent = d.taskRole[b.ToolUseID]
		default:
			continue
		}
		out = append(out, ev)
	}
	if env.Type == "assistant" {
		u := env.Message.Usage
		tok := event.Tokens{Input: u.InputTokens, Output: u.OutputTokens, CacheRead: u.CacheReadInputTokens}
		if cc := u.CacheCreation; cc != nil {
			tok.CacheWrite5m, tok.CacheWrite1h = cc.Ephemeral5mInputTokens, cc.Ephemeral1hInputTokens
		} else {
			tok.CacheWrite5m = u.CacheCreationInputTokens
		}
		out = append(out, event.Event{
			Kind: event.KindUsage, MessageID: env.Message.ID, Model: env.Message.Model,
			Role: role, InSubagent: subagent, Tokens: tok,
		})
	}
	return out
}

type refTaskInput struct {
	SubagentType string `json:"subagent_type"`
}
type refTokenUsage struct {
	InputTokens              int               `json:"input_tokens"`
	OutputTokens             int               `json:"output_tokens"`
	CacheReadInputTokens     int               `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int               `json:"cache_creation_input_tokens"`
	CacheCreation            *refCacheCreation `json:"cache_creation,omitempty"`
}
type refCacheCreation struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens"`
}

func refSubagentType(input json.RawMessage) string {
	var ti refTaskInput
	if len(input) > 0 {
		_ = json.Unmarshal(input, &ti)
	}
	return ti.SubagentType
}
func (d *refDecoder) refRecordSpawns(blocks []refBlock) {
	for _, b := range blocks {
		if b.Type != "tool_use" || !isSubagentSpawnTool(b.Name) || b.ID == "" {
			continue
		}
		role := refSubagentType(b.Input)
		if role == "" {
			role = driver.DefaultRole
		}
		d.taskRole[b.ID] = role
	}
}
func (d *refDecoder) refSubagentRole(parent string) string {
	if role, ok := d.taskRole[parent]; ok {
		return role
	}
	return driver.DefaultRole
}
func refResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return string(raw)
}
func refClassifyTurn(top *refTopLevel) (content, synthetic bool) {
	if top == nil {
		return false, false
	}
	var msg struct {
		Model string `json:"model"`
	}
	if !refDecodeField(top.Message, &msg) {
		return false, false
	}
	if top.Type == "assistant" && msg.Model == syntheticModelSentinel && refErrorPresent(top.Error) {
		return false, true
	}
	return top.Type == "assistant" || top.Type == "user", false
}
func refErrorPresent(raw json.RawMessage) bool {
	s := string(raw)
	return s != "" && s != "null" && s != `""`
}
