package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"spindrift.dev/sdk/driver"
	"spindrift.dev/sdk/event"
)

// decode runs line through a fresh decoder.
func decode(line string) []event.Event { return newDecoder(time.Time{})(line) }

func TestDecode(t *testing.T) {
	reset := time.UnixMilli(1790000004000).Add(3600 * time.Second).UTC()
	tests := []struct {
		name string
		line string
		want []event.Event
	}{
		{"blank", "  \t", nil},
		{"unknown type", `{"type":"step_banana","part":{"messageID":"m"}}`, nil},
		{"json null", `null`, nil},
		{"non-JSON stderr", "  panic: boom \t", []event.Event{{Kind: event.KindUnparsed, Text: "panic: boom"}}},
		{"json scalar", `42`, []event.Event{{Kind: event.KindUnparsed, Text: "42"}}},
		{
			"step_start",
			`{"type":"step_start","timestamp":1000,"part":{"messageID":"msg_1"}}`,
			[]event.Event{{Kind: event.KindStart, Timestamp: 1000, MessageID: "msg_1"}},
		},
		{
			"text keeps raw part.text",
			`{"type":"text","timestamp":5,"part":{"messageID":"msg_1","text":"\n hello \n"}}`,
			[]event.Event{{Kind: event.KindText, Timestamp: 5, MessageID: "msg_1", Text: "\n hello \n"}},
		},
		{
			"text with no messageID",
			`{"type":"text","part":{"text":"hi"}}`,
			[]event.Event{{Kind: event.KindText, Text: "hi"}},
		},
		{
			"text with no part",
			`{"type":"text"}`,
			[]event.Event{{Kind: event.KindText}},
		},
		{
			"reasoning",
			`{"type":"reasoning","timestamp":7,"part":{"messageID":"msg_1","text":"thinking"}}`,
			[]event.Event{{Kind: event.KindReasoning, Timestamp: 7, MessageID: "msg_1", Text: "thinking"}},
		},
		{
			"tool_use pending is a call only",
			`{"type":"tool_use","timestamp":9,"part":{"callID":"c1","tool":"bash","messageID":"msg_1","state":{"status":"pending","input":{"command":"ls"}}}}`,
			[]event.Event{{
				Kind: event.KindToolCall, Timestamp: 9, MessageID: "msg_1",
				ToolID: "c1", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"ls"}`),
				ToolTarget: "ls",
			}},
		},
		{
			"tool_use completed is a call and a result",
			`{"type":"tool_use","timestamp":9,"part":{"callID":"c1","tool":"bash","messageID":"msg_1","state":{"status":"completed","input":{"command":"ls"},"output":"x"}}}`,
			[]event.Event{
				{
					Kind: event.KindToolCall, Timestamp: 9, MessageID: "msg_1",
					ToolID: "c1", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"ls"}`),
					ToolTarget: "ls",
				},
				{
					Kind: event.KindToolResult, Timestamp: 9, MessageID: "msg_1",
					ToolID: "c1", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"ls"}`),
					ToolTarget: "ls", ToolOutput: "x",
				},
			},
		},
		{
			"tool_use error carries state.error as the output",
			`{"type":"tool_use","part":{"callID":"c4","tool":"read","state":{"status":"error","input":{"filePath":"/tmp/x"},"error":"file not found"}}}`,
			[]event.Event{
				{Kind: event.KindToolCall, ToolID: "c4", ToolName: "read", ToolInput: json.RawMessage(`{"filePath":"/tmp/x"}`), ToolTarget: "/tmp/x"},
				{
					Kind: event.KindToolResult, ToolID: "c4", ToolName: "read", ToolInput: json.RawMessage(`{"filePath":"/tmp/x"}`),
					ToolTarget: "/tmp/x", ToolOutput: "file not found", IsError: true,
				},
			},
		},
		{
			"task tool result names the subagent",
			`{"type":"tool_use","part":{"callID":"c5","tool":"task","state":{"status":"completed","input":{"description":"d","prompt":"p","subagent_type":"reviewer"},"output":"VERDICT: BLOCK"}}}`,
			[]event.Event{
				{Kind: event.KindToolCall, ToolID: "c5", ToolName: "task", ToolInput: json.RawMessage(`{"description":"d","prompt":"p","subagent_type":"reviewer"}`), ToolTarget: "reviewer"},
				{
					Kind: event.KindToolResult, ToolID: "c5", ToolName: "task", ToolInput: json.RawMessage(`{"description":"d","prompt":"p","subagent_type":"reviewer"}`),
					ToolTarget: "reviewer", ToolOutput: "VERDICT: BLOCK", Subagent: "reviewer",
				},
			},
		},
		{
			"tool_use with a non-string output keeps the raw JSON as output",
			`{"type":"tool_use","part":{"callID":"c6","tool":"glob","state":{"status":"completed","input":{"pattern":"*.go"},"output":7}}}`,
			[]event.Event{
				{Kind: event.KindToolCall, ToolID: "c6", ToolName: "glob", ToolInput: json.RawMessage(`{"pattern":"*.go"}`), ToolTarget: "*.go"},
				{Kind: event.KindToolResult, ToolID: "c6", ToolName: "glob", ToolInput: json.RawMessage(`{"pattern":"*.go"}`), ToolTarget: "*.go", ToolOutput: "7"},
			},
		},
		{
			"task tool without a subagent_type tags the default role",
			`{"type":"tool_use","part":{"callID":"c7","tool":"task","state":{"status":"completed","input":{"prompt":"p"},"output":"done"}}}`,
			[]event.Event{
				{Kind: event.KindToolCall, ToolID: "c7", ToolName: "task", ToolInput: json.RawMessage(`{"prompt":"p"}`)},
				{Kind: event.KindToolResult, ToolID: "c7", ToolName: "task", ToolInput: json.RawMessage(`{"prompt":"p"}`), ToolOutput: "done", Subagent: driver.DefaultRole},
			},
		},
		{
			"tool_use error result is flagged",
			`{"type":"tool_use","part":{"callID":"c2","tool":"read","state":{"status":"error","input":{}}}}`,
			[]event.Event{
				{Kind: event.KindToolCall, ToolID: "c2", ToolName: "read", ToolInput: json.RawMessage(`{}`)},
				{Kind: event.KindToolResult, ToolID: "c2", ToolName: "read", ToolInput: json.RawMessage(`{}`), IsError: true},
			},
		},
		{
			"step_finish",
			`{"type":"step_finish","timestamp":1500,"part":{"messageID":"msg_1","modelID":"gpt-5","tokens":{"input":100,"output":50,"reasoning":10,"cache":{"write":20,"read":200}},"cost":0.01}}`,
			[]event.Event{
				{
					Kind: event.KindUsage, Timestamp: 1500, MessageID: "msg_1", Model: "gpt-5",
					Tokens: event.Tokens{Input: 100, Output: 60, CacheRead: 200, CacheWrite5m: 20, CostUSD: 0.01},
				},
				{Kind: event.KindModel, Timestamp: 1500, MessageID: "msg_1", Model: "gpt-5"},
			},
		},
		{
			"step_finish without modelID has no model event",
			`{"type":"step_finish","timestamp":1500,"part":{"messageID":"msg_1","tokens":{"input":1,"output":2}}}`,
			[]event.Event{{
				Kind: event.KindUsage, Timestamp: 1500, MessageID: "msg_1",
				Tokens: event.Tokens{Input: 1, Output: 2},
			}},
		},
		{
			"step_finish with a malformed unrelated field is unparsed whole",
			`{"type":"step_finish","part":{"messageID":"m","modelID":"gpt-5","text":5,"tokens":{"input":3}}}`,
			[]event.Event{{Kind: event.KindUnparsed, Text: `{"type":"step_finish","part":{"messageID":"m","modelID":"gpt-5","text":5,"tokens":{"input":3}}}`}},
		},
		{
			"text with a non-string text is unparsed whole",
			`{"type":"text","part":{"messageID":"m2","text":7}}`,
			[]event.Event{{Kind: event.KindUnparsed, Text: `{"type":"text","part":{"messageID":"m2","text":7}}`}},
		},
		{
			"step_finish with a string cost is unparsed whole",
			`{"type":"step_finish","part":{"messageID":"m","cost":"x","tokens":{"input":3}}}`,
			[]event.Event{{Kind: event.KindUnparsed, Text: `{"type":"step_finish","part":{"messageID":"m","cost":"x","tokens":{"input":3}}}`}},
		},
		{
			"error with a non-int timestamp is unparsed whole",
			`{"type":"error","timestamp":"soon","error":"boom"}`,
			[]event.Event{{Kind: event.KindUnparsed, Text: `{"type":"error","timestamp":"soon","error":"boom"}`}},
		},
		{
			"text with a non-int timestamp is unparsed whole",
			`{"type":"text","timestamp":"soon","part":{"text":"hi"}}`,
			[]event.Event{{Kind: event.KindUnparsed, Text: `{"type":"text","timestamp":"soon","part":{"text":"hi"}}`}},
		},
		{
			"tool_use with a malformed state is still a bare call",
			`{"type":"tool_use","part":{"callID":"c3","tool":"bash","messageID":"msg_1","state":7}}`,
			[]event.Event{{Kind: event.KindToolCall, MessageID: "msg_1", ToolID: "c3", ToolName: "bash"}},
		},
		{
			"string error",
			`{"type":"error","timestamp":3,"error":"connection reset by peer"}`,
			[]event.Event{{Kind: event.KindError, Timestamp: 3, Text: "connection reset by peer"}},
		},
		{
			"object error is compact text with the reset from retry-after",
			`{"type":"error","timestamp":1790000004000,"error":{"name":"APIError","data":{"statusCode":429,"responseHeaders":{"retry-after":"3600"}}}}`,
			[]event.Event{{
				Kind: event.KindError, Timestamp: 1790000004000,
				Text:    `{"name":"APIError","data":{"statusCode":429,"responseHeaders":{"retry-after":"3600"}}}`,
				ResetAt: &reset,
			}},
		},
		{
			"error with no payload",
			`{"type":"error"}`,
			[]event.Event{{Kind: event.KindError}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decode(tc.line)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Decode(%s)\n got %+v\nwant %+v", tc.line, got, tc.want)
			}
		})
	}
}

func TestDecode_ResetAtNeedsAnchorOrHeader(t *testing.T) {
	got := decode(`{"type":"error","error":{"data":{"responseHeaders":{"retry-after":"60"}}}}`)
	if len(got) != 1 || got[0].ResetAt != nil {
		t.Errorf("relative retry-after with no timestamp: got %+v, want one error with nil ResetAt", got)
	}
}

// A host line is the host's to decode: the guest yields nothing for it, whether
// or not its payload is well-formed, and only follows its state.
func TestDecode_HostLinesYieldNothing(t *testing.T) {
	for _, line := range []string{
		`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":2,"role":"review"}}`,
		`{"type":"spindrift_op","spindrift_op":{"op":"verdict","verdict":"APPROVE"}}`,
		`{"type":"spindrift_op"}`,
	} {
		if got := decode(line); got != nil {
			t.Errorf("decode(%s) = %+v, want none", line, got)
		}
	}
}

func TestDecode_ResetAtHeaders(t *testing.T) {
	const ts = 1790000000000
	at := func(d time.Duration) *time.Time { r := time.UnixMilli(ts).Add(d).UTC(); return &r }
	date := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name, headers string
		want          *time.Time
	}{
		{"retry-after-ms wins", `{"Retry-After-MS":" 1500 ","retry-after":"60"}`, at(1500 * time.Millisecond)},
		{"retry-after seconds", `{"retry-after":"60"}`, at(time.Minute)},
		{"retry-after http date", `{"retry-after":"` + date.Format(http.TimeFormat) + `"}`, &date},
		{"negative is no reset", `{"retry-after":"-1"}`, nil},
		{"non-string header drops the payload", `{"retry-after":60}`, nil},
		{"case-colliding headers: last wins", `{"Retry-After":"9","retry-after":"8"}`, at(8 * time.Second)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			line := `{"type":"error","timestamp":1790000000000,"error":{"data":{"responseHeaders":` + tc.headers + `}}}`
			got := decode(line)
			if len(got) != 1 || !reflect.DeepEqual(got[0].ResetAt, tc.want) {
				t.Errorf("decode(%s) = %+v, want ResetAt %v", line, got, tc.want)
			}
		})
	}
}

const (
	passStart1 = `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":1,"role":"implement"}}`
	passStart2 = `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":2,"role":"review"}}`
	modelA     = "github-copilot/claude-sonnet-5.5"
	modelB     = "github-copilot/claude-opus-5.5"
	passModelA = `{"type":"spindrift_op","spindrift_op":{"op":"pass_model","model":"` + modelA + `","effort":"high"}}`
	passModelB = `{"type":"spindrift_op","spindrift_op":{"op":"pass_model","model":"` + modelB + `"}}`
)

func stepFinish(id string) string {
	return `{"type":"step_finish","part":{"messageID":"` + id + `","tokens":{"input":10,"output":5}}}`
}

// usageAndModel feeds lines through one decoder and returns the KindUsage and
// KindModel events, in order.
func usageAndModel(lines ...string) (usage, models []event.Event) {
	dec := newDecoder(time.Time{})
	for _, l := range lines {
		for _, e := range dec(l) {
			switch e.Kind {
			case event.KindUsage:
				usage = append(usage, e)
			case event.KindModel:
				models = append(models, e)
			}
		}
	}
	return usage, models
}

// Real opencode never puts modelID on step_finish; the pass_model op that
// driver-exec prints after pass_start is the only source of the pass's model.
func TestNewDecoder_PassModelAttributesStepFinish(t *testing.T) {
	usage, models := usageAndModel(passStart1, passModelA, stepFinish("m1"), stepFinish("m2"))
	if len(usage) != 2 || usage[0].Model != modelA || usage[1].Model != usage[0].Model {
		t.Fatalf("usage = %+v", usage)
	}
	for _, u := range usage {
		if u.Effort != "high" || u.Role != driver.ImplementorRole {
			t.Errorf("usage effort/role = %q/%q, want high/implementor", u.Effort, u.Role)
		}
	}
	// Both step_finish lines share one (model, effort, role), so one KindModel.
	if len(models) != 1 || models[0].Model != usage[0].Model || models[0].Effort != "high" || models[0].Role != driver.ImplementorRole {
		t.Fatalf("KindModel events = %+v", models)
	}
}

// A pass that announces no model must not inherit the previous pass's.
func TestNewDecoder_PassStartClearsModel(t *testing.T) {
	usage, models := usageAndModel(passStart1, passModelA, stepFinish("m1"), passStart2, stepFinish("m2"))
	if len(usage) != 2 || usage[0].Model == "" || usage[1].Model != "" {
		t.Fatalf("usage = %+v, want second model empty", usage)
	}
	if len(models) != 1 {
		t.Fatalf("KindModel events = %+v, want only the first pass's", models)
	}
}

// A step_finish that names its own modelID beats the model a pass_model op
// announced, and carries no effort.
func TestNewDecoder_ModelIDWinsOverOp(t *testing.T) {
	line := `{"type":"step_finish","part":{"messageID":"m1","modelID":"gpt-5","tokens":{"input":1}}}`
	usage, models := usageAndModel(passStart1, passModelA, line)
	if len(usage) != 1 || usage[0].Model != "gpt-5" {
		t.Fatalf("usage = %+v", usage)
	}
	if len(models) != 1 || models[0].Model != "gpt-5" || models[0].Effort != "" {
		t.Fatalf("KindModel events = %+v, want gpt-5 with no effort", models)
	}
}

// pass_start's role maps through the pass-role table; an unmapped role keeps
// the prior one, and a repeated pass re-announces its model.
func TestNewDecoder_PassRoleOnModel(t *testing.T) {
	unmapped := `{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":3,"role":"bogus"}}`
	_, models := usageAndModel(
		passStart1, passModelA, stepFinish("m1"),
		passStart2, passModelB, stepFinish("m2"),
		unmapped, passModelB, stepFinish("m3"),
		passStart2, passModelB, stepFinish("m4"),
	)
	var roles []string
	for _, m := range models {
		roles = append(roles, m.Role)
	}
	if want := []string{"implementor", "reviewer", "reviewer", "reviewer"}; !slices.Equal(roles, want) {
		t.Fatalf("KindModel roles = %v, want %v", roles, want)
	}
}

// Decoders share no state: a second one starts with no model or role.
func TestNewDecoder_StateIsPerDecoder(t *testing.T) {
	usageAndModel(passStart1, passModelA)
	usage, models := usageAndModel(stepFinish("m1"))
	if len(usage) != 1 || usage[0].Model != "" || usage[0].Role != "" || len(models) != 0 {
		t.Fatalf("usage = %+v, models = %+v, want untagged", usage, models)
	}
}
