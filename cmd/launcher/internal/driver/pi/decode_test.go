package pi_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/pi"
)

func TestDecode(t *testing.T) {
	const model = "stub/stub-model"
	tests := []struct {
		name string
		line string
		want []driverkit.Event
	}{
		{"blank", "  \t", nil},
		{"json null", `null`, nil},
		{"non-JSON stderr", "  Error: unknown model \t", []driverkit.Event{{Kind: driverkit.KindUnparsed, Text: "Error: unknown model"}}},
		{"session", `{"type":"session","version":3,"id":"x","cwd":"/w"}`, nil},
		{"message_update repeats the message", `{"type":"message_update","usage":` + stubUsage + `,"assistantMessageEvent":{"type":"text_delta","delta":"hi"}}`, nil},
		{"turn_end repeats the message", `{"type":"turn_end","message":{"role":"assistant","content":[],"usage":` + stubUsage + `},"toolResults":[]}`, nil},
		{"agent_end repeats the messages", `{"type":"agent_end","willRetry":true,"messages":[{"role":"assistant","usage":` + stubUsage + `}]}`, nil},
		{"agent_settled", settled, nil},
		{"auto_retry_start", retryStart, nil},
		{"system message", `{"type":"message_end","message":{"role":"system","content":"","timestamp":5}}`, nil},
		{"user message", `{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"go"}],"timestamp":5}}`, nil},
		{"toolResult message", `{"type":"message_end","message":{"role":"toolResult","toolCallId":"c","content":[{"type":"text","text":"x"}],"timestamp":5}}`, nil},
		{
			"assistant text",
			textEnd(1000, "\n hello \n"),
			[]driverkit.Event{
				{Kind: driverkit.KindStart, Timestamp: 1000, MessageID: "1000"},
				{Kind: driverkit.KindModel, Timestamp: 1000, MessageID: "1000", Model: model, Effort: "high"},
				{Kind: driverkit.KindText, Timestamp: 1000, MessageID: "1000", Text: "\n hello \n"},
				{
					Kind: driverkit.KindUsage, Timestamp: 1035, MessageID: "1000", Model: model, Effort: "high",
					Tokens: driverkit.Tokens{Input: 80, Output: 30, CacheRead: 40, CostUSD: 0.000144},
				},
			},
		},
		{
			"reasoning precedes text",
			assistantEnd(2000, `[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"done"}]`, "stop", ""),
			[]driverkit.Event{
				{Kind: driverkit.KindStart, Timestamp: 2000, MessageID: "2000"},
				{Kind: driverkit.KindModel, Timestamp: 2000, MessageID: "2000", Model: model, Effort: "high"},
				{Kind: driverkit.KindReasoning, Timestamp: 2000, MessageID: "2000", Text: "hmm"},
				{Kind: driverkit.KindText, Timestamp: 2000, MessageID: "2000", Text: "done"},
				{
					Kind: driverkit.KindUsage, Timestamp: 2035, MessageID: "2000", Model: model, Effort: "high",
					Tokens: driverkit.Tokens{Input: 80, Output: 30, CacheRead: 40, CostUSD: 0.000144},
				},
			},
		},
		{
			"bash tool call targets its command",
			assistantEnd(3000, `[{"type":"toolCall","id":"call_1","name":"bash","arguments":{"command":"echo hi"}}]`, "toolUse", ""),
			[]driverkit.Event{
				{Kind: driverkit.KindStart, Timestamp: 3000, MessageID: "3000"},
				{Kind: driverkit.KindModel, Timestamp: 3000, MessageID: "3000", Model: model, Effort: "high"},
				{
					Kind: driverkit.KindToolCall, Timestamp: 3000, MessageID: "3000", ToolID: "call_1", ToolName: "bash",
					ToolInput: json.RawMessage(`{"command":"echo hi"}`), ToolTarget: "echo hi",
				},
				{
					Kind: driverkit.KindUsage, Timestamp: 3035, MessageID: "3000", Model: model, Effort: "high",
					Tokens: driverkit.Tokens{Input: 80, Output: 30, CacheRead: 40, CostUSD: 0.000144},
				},
			},
		},
		{
			"read tool call targets its path",
			assistantEnd(3000, `[{"type":"toolCall","id":"c2","name":"read","arguments":{"path":"a.txt"}}]`, "toolUse", ""),
			[]driverkit.Event{
				{Kind: driverkit.KindStart, Timestamp: 3000, MessageID: "3000"},
				{Kind: driverkit.KindModel, Timestamp: 3000, MessageID: "3000", Model: model, Effort: "high"},
				{
					Kind: driverkit.KindToolCall, Timestamp: 3000, MessageID: "3000", ToolID: "c2", ToolName: "read",
					ToolInput: json.RawMessage(`{"path":"a.txt"}`), ToolTarget: "a.txt",
				},
				{
					Kind: driverkit.KindUsage, Timestamp: 3035, MessageID: "3000", Model: model, Effort: "high",
					Tokens: driverkit.Tokens{Input: 80, Output: 30, CacheRead: 40, CostUSD: 0.000144},
				},
			},
		},
		{
			"failed turn is an error with no progress",
			errorEnd(4000, rateLimitMsg),
			[]driverkit.Event{
				{Kind: driverkit.KindModel, Timestamp: 4000, MessageID: "4000", Model: model, Effort: "high"},
				{Kind: driverkit.KindError, Timestamp: 4000, MessageID: "4000", Text: rateLimitMsg},
				{
					Kind: driverkit.KindUsage, Timestamp: 4035, MessageID: "4000", Model: model, Effort: "high",
					Tokens: driverkit.Tokens{Input: 80, Output: 30, CacheRead: 40, CostUSD: 0.000144},
				},
			},
		},
		{
			"no provider and no thinking level leaves the bare model",
			`{"type":"message_end","message":{"role":"assistant","content":[],"model":"m","usage":{"input":1,"output":2},"stopReason":"stop","timestamp":7}}`,
			[]driverkit.Event{
				{Kind: driverkit.KindStart, Timestamp: 7, MessageID: "7"},
				{Kind: driverkit.KindModel, Timestamp: 7, MessageID: "7", Model: "m"},
				{Kind: driverkit.KindUsage, Timestamp: 7, MessageID: "7", Model: "m", Tokens: driverkit.Tokens{Input: 1, Output: 2}},
			},
		},
		{
			"tool_execution_end carries the result text",
			`{"type":"tool_execution_end","toolCallId":"call_1","toolName":"bash","result":{"content":[{"type":"text","text":"hi\n"},{"type":"text","text":"there"}],"structuredContent":{"exit_code":0}},"isError":false,"durationMs":10}`,
			[]driverkit.Event{{Kind: driverkit.KindToolResult, ToolID: "call_1", ToolName: "bash", ToolOutput: "hi\nthere"}},
		},
		{
			"failing tool_execution_end",
			`{"type":"tool_execution_end","toolCallId":"c","toolName":"bash","result":{"content":[{"type":"text","text":"boom"}]},"isError":true}`,
			[]driverkit.Event{{Kind: driverkit.KindToolResult, ToolID: "c", ToolName: "bash", ToolOutput: "boom", IsError: true}},
		},
		{
			"tool_execution_end with a non-text result keeps the raw JSON",
			`{"type":"tool_execution_end","toolCallId":"c","toolName":"x","result":{"structuredContent":{"a":1}},"isError":false}`,
			[]driverkit.Event{{Kind: driverkit.KindToolResult, ToolID: "c", ToolName: "x", ToolOutput: `{"structuredContent":{"a":1}}`}},
		},
		{
			"strict field: malformed usage drops the whole line",
			`{"type":"message_end","message":{"role":"assistant","usage":{"input":"many"},"timestamp":7}}`,
			[]driverkit.Event{{Kind: driverkit.KindUnparsed, Text: `{"type":"message_end","message":{"role":"assistant","usage":{"input":"many"},"timestamp":7}}`}},
		},
		{
			"lenient field: malformed tool arguments cost only the target",
			assistantEnd(9, `[{"type":"toolCall","id":"c","name":"bash","arguments":"oops"}]`, "toolUse", ""),
			[]driverkit.Event{
				{Kind: driverkit.KindStart, Timestamp: 9, MessageID: "9"},
				{Kind: driverkit.KindModel, Timestamp: 9, MessageID: "9", Model: model, Effort: "high"},
				{Kind: driverkit.KindToolCall, Timestamp: 9, MessageID: "9", ToolID: "c", ToolName: "bash", ToolInput: json.RawMessage(`"oops"`)},
				{
					Kind: driverkit.KindUsage, Timestamp: 44, MessageID: "9", Model: model, Effort: "high",
					Tokens: driverkit.Tokens{Input: 80, Output: 30, CacheRead: 40, CostUSD: 0.000144},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pi.Decode(tc.line)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Decode(%.80s):\n got %+v\nwant %+v", tc.line, got, tc.want)
			}
		})
	}
}
