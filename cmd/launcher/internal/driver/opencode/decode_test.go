package opencode_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/opencode"
	"spindrift.dev/launcher/internal/hostop"
)

func TestDecode(t *testing.T) {
	reset := time.UnixMilli(1790000004000).Add(3600 * time.Second).UTC()
	tests := []struct {
		name string
		line string
		want []driverkit.Event
	}{
		{"blank", "  \t", nil},
		{"unknown type", `{"type":"step_banana","part":{"messageID":"m"}}`, nil},
		{"json null", `null`, nil},
		{"non-JSON stderr", "  panic: boom \t", []driverkit.Event{{Kind: driverkit.KindUnparsed, Text: "panic: boom"}}},
		{"json scalar", `42`, []driverkit.Event{{Kind: driverkit.KindUnparsed, Text: "42"}}},
		{
			"step_start",
			`{"type":"step_start","timestamp":1000,"part":{"messageID":"msg_1"}}`,
			[]driverkit.Event{{Kind: driverkit.KindStart, Timestamp: 1000, MessageID: "msg_1"}},
		},
		{
			"text keeps raw part.text",
			`{"type":"text","timestamp":5,"part":{"messageID":"msg_1","text":"\n hello \n"}}`,
			[]driverkit.Event{{Kind: driverkit.KindText, Timestamp: 5, MessageID: "msg_1", Text: "\n hello \n"}},
		},
		{
			"text with no messageID",
			`{"type":"text","part":{"text":"hi"}}`,
			[]driverkit.Event{{Kind: driverkit.KindText, Text: "hi"}},
		},
		{
			"text with no part",
			`{"type":"text"}`,
			[]driverkit.Event{{Kind: driverkit.KindText}},
		},
		{
			"reasoning",
			`{"type":"reasoning","timestamp":7,"part":{"messageID":"msg_1","text":"thinking"}}`,
			[]driverkit.Event{{Kind: driverkit.KindReasoning, Timestamp: 7, MessageID: "msg_1", Text: "thinking"}},
		},
		{
			"tool_use pending is a call only",
			`{"type":"tool_use","timestamp":9,"part":{"callID":"c1","tool":"bash","messageID":"msg_1","state":{"status":"pending","input":{"command":"ls"}}}}`,
			[]driverkit.Event{{
				Kind: driverkit.KindToolCall, Timestamp: 9, MessageID: "msg_1",
				ToolID: "c1", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"ls"}`),
			}},
		},
		{
			"tool_use completed is a call and a result",
			`{"type":"tool_use","timestamp":9,"part":{"callID":"c1","tool":"bash","messageID":"msg_1","state":{"status":"completed","input":{"command":"ls"},"output":"x"}}}`,
			[]driverkit.Event{
				{
					Kind: driverkit.KindToolCall, Timestamp: 9, MessageID: "msg_1",
					ToolID: "c1", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"ls"}`),
				},
				{
					Kind: driverkit.KindToolResult, Timestamp: 9, MessageID: "msg_1",
					ToolID: "c1", ToolName: "bash", ToolInput: json.RawMessage(`{"command":"ls"}`),
				},
			},
		},
		{
			"tool_use error result is flagged",
			`{"type":"tool_use","part":{"callID":"c2","tool":"read","state":{"status":"error","input":{}}}}`,
			[]driverkit.Event{
				{Kind: driverkit.KindToolCall, ToolID: "c2", ToolName: "read", ToolInput: json.RawMessage(`{}`)},
				{Kind: driverkit.KindToolResult, ToolID: "c2", ToolName: "read", ToolInput: json.RawMessage(`{}`), IsError: true},
			},
		},
		{
			"step_finish",
			`{"type":"step_finish","timestamp":1500,"part":{"messageID":"msg_1","modelID":"gpt-5","tokens":{"input":100,"output":50,"reasoning":10,"cache":{"write":20,"read":200}},"cost":0.01}}`,
			[]driverkit.Event{
				{
					Kind: driverkit.KindUsage, Timestamp: 1500, MessageID: "msg_1", Model: "gpt-5",
					Tokens: driverkit.Tokens{Input: 100, Output: 60, CacheRead: 200, CacheWrite5m: 20, CostUSD: 0.01},
				},
				{Kind: driverkit.KindModel, Timestamp: 1500, MessageID: "msg_1", Model: "gpt-5"},
			},
		},
		{
			"step_finish without modelID has no model event",
			`{"type":"step_finish","timestamp":1500,"part":{"messageID":"msg_1","tokens":{"input":1,"output":2}}}`,
			[]driverkit.Event{{
				Kind: driverkit.KindUsage, Timestamp: 1500, MessageID: "msg_1",
				Tokens: driverkit.Tokens{Input: 1, Output: 2},
			}},
		},
		{
			"step_finish with a malformed unrelated field is unparsed whole",
			`{"type":"step_finish","part":{"messageID":"m","modelID":"gpt-5","text":5,"tokens":{"input":3}}}`,
			[]driverkit.Event{{Kind: driverkit.KindUnparsed, Text: `{"type":"step_finish","part":{"messageID":"m","modelID":"gpt-5","text":5,"tokens":{"input":3}}}`}},
		},
		{
			"text with a non-string text is unparsed whole",
			`{"type":"text","part":{"messageID":"m2","text":7}}`,
			[]driverkit.Event{{Kind: driverkit.KindUnparsed, Text: `{"type":"text","part":{"messageID":"m2","text":7}}`}},
		},
		{
			"step_finish with a string cost is unparsed whole",
			`{"type":"step_finish","part":{"messageID":"m","cost":"x","tokens":{"input":3}}}`,
			[]driverkit.Event{{Kind: driverkit.KindUnparsed, Text: `{"type":"step_finish","part":{"messageID":"m","cost":"x","tokens":{"input":3}}}`}},
		},
		{
			"error with a non-int timestamp is unparsed whole",
			`{"type":"error","timestamp":"soon","error":"boom"}`,
			[]driverkit.Event{{Kind: driverkit.KindUnparsed, Text: `{"type":"error","timestamp":"soon","error":"boom"}`}},
		},
		{
			"tool_use with a malformed state is still a bare call",
			`{"type":"tool_use","part":{"callID":"c3","tool":"bash","messageID":"msg_1","state":7}}`,
			[]driverkit.Event{{Kind: driverkit.KindToolCall, MessageID: "msg_1", ToolID: "c3", ToolName: "bash"}},
		},
		{
			"string error",
			`{"type":"error","timestamp":3,"error":"connection reset by peer"}`,
			[]driverkit.Event{{Kind: driverkit.KindError, Timestamp: 3, Text: "connection reset by peer"}},
		},
		{
			"object error is compact text with the reset from retry-after",
			`{"type":"error","timestamp":1790000004000,"error":{"name":"APIError","data":{"statusCode":429,"responseHeaders":{"retry-after":"3600"}}}}`,
			[]driverkit.Event{{
				Kind: driverkit.KindError, Timestamp: 1790000004000,
				Text:    `{"name":"APIError","data":{"statusCode":429,"responseHeaders":{"retry-after":"3600"}}}`,
				ResetAt: &reset,
			}},
		},
		{
			"error with no payload",
			`{"type":"error"}`,
			[]driverkit.Event{{Kind: driverkit.KindError}},
		},
		{
			"spindrift_op",
			`{"type":"spindrift_op","spindrift_op":{"op":"pass_start","pass":2,"role":"review"}}`,
			[]driverkit.Event{{Kind: driverkit.KindHostOp, HostOp: &hostop.SpindriftOp{Op: "pass_start", Pass: 2, Role: "review"}}},
		},
		{"spindrift_op without payload", `{"type":"spindrift_op"}`, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := opencode.Decode(tc.line)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Decode(%s)\n got %+v\nwant %+v", tc.line, got, tc.want)
			}
		})
	}
}

func TestDecode_ResetAtNeedsAnchorOrHeader(t *testing.T) {
	got := opencode.Decode(`{"type":"error","error":{"data":{"responseHeaders":{"retry-after":"60"}}}}`)
	if len(got) != 1 || got[0].ResetAt != nil {
		t.Errorf("relative retry-after with no timestamp: got %+v, want one error with nil ResetAt", got)
	}
}

func TestNewDecoder_IsDecode(t *testing.T) {
	line := `{"type":"step_start","timestamp":1,"part":{"messageID":"m"}}`
	if got, want := opencode.NewDecoder()(line), opencode.Decode(line); !reflect.DeepEqual(got, want) {
		t.Errorf("NewDecoder()(line) = %+v, want %+v", got, want)
	}
}

// TestDecode_CorpusLines decodes every real pass.jsonl line: none may panic or
// yield an event with an empty Kind.
func TestDecode_CorpusLines(t *testing.T) {
	paths, err := filepath.Glob("../testdata/corpus/opencode/*/pass.jsonl")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no corpus pass logs found: %v", err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			for _, ev := range opencode.Decode(line) {
				if ev.Kind == "" {
					t.Errorf("%s: empty Kind decoding %.120s", path, line)
				}
			}
		}
	}
}

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pass.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A line any reader's field fails to decode is dropped whole, so the earlier
// message stays the last one.
func TestResultText_DropsMalformedLineWhole(t *testing.T) {
	path := writeLog(t,
		`{"type":"text","part":{"messageID":"m1","text":"SPINDRIFT_OUTCOME status=complete"}}`,
		`{"type":"text","part":{"messageID":"m2","text":7}}`,
	)
	got, err := opencode.ResultText(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "SPINDRIFT_OUTCOME status=complete\n"; got != want {
		t.Errorf("ResultText = %q, want %q", got, want)
	}
}

func TestExtractUsage_DropsMalformedStepFinishWhole(t *testing.T) {
	path := writeLog(t, `{"type":"step_finish","part":{"messageID":"m","cost":"x","tokens":{"input":3}}}`)
	rep, err := opencode.ExtractUsage(path)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Found {
		t.Errorf("ExtractUsage reported Found for a log whose only step_finish is malformed: %+v", rep)
	}
}
