package claude

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
)

var decodeNow = time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)

func kinds(evs []driverkit.Event) []driverkit.Kind {
	var out []driverkit.Kind
	for _, ev := range evs {
		out = append(out, ev.Kind)
	}
	return out
}

func wantKinds(t *testing.T, evs []driverkit.Event, want ...driverkit.Kind) {
	t.Helper()
	got := kinds(evs)
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", got, want)
		}
	}
}

func TestDecodeBlankLineYieldsNothing(t *testing.T) {
	if evs := newDecoderAt(decodeNow)("  \t"); len(evs) != 0 {
		t.Errorf("got %v", evs)
	}
}

func TestDecodeNonJSONIsErrorCandidateWithReset(t *testing.T) {
	evs := newDecoderAt(decodeNow)("  You've hit your session limit · resets 6:30pm (UTC)  ")
	wantKinds(t, evs, driverkit.KindError)
	if !strings.HasPrefix(evs[0].Text, "You've hit") || strings.HasSuffix(evs[0].Text, " ") {
		t.Errorf("text not the trimmed line: %q", evs[0].Text)
	}
	want := time.Date(2026, 7, 4, 18, 30, 0, 0, time.UTC)
	if evs[0].ResetAt == nil || !evs[0].ResetAt.Equal(want) {
		t.Errorf("ResetAt = %v, want %v", evs[0].ResetAt, want)
	}
}

func TestDecodeHostOpIsNeverAnError(t *testing.T) {
	d := newDecoderAt(decodeNow)
	line := hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpDispatchStart, Start: &hostop.DispatchStart{Knobs: map[string]string{"X": "Overloaded"}}})
	evs := d(strings.TrimSpace(line))
	wantKinds(t, evs, driverkit.KindHostOp)
	if evs[0].HostOp == nil || evs[0].HostOp.Op != hostop.OpDispatchStart {
		t.Errorf("HostOp = %+v", evs[0].HostOp)
	}
}

func TestDecodeAssistantContent(t *testing.T) {
	d := newDecoderAt(decodeNow)
	evs := d(`{"type":"assistant","message":{"id":"m1","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":" hi "},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"ls"}}]}}`)
	wantKinds(t, evs, driverkit.KindStart, driverkit.KindReasoning, driverkit.KindText, driverkit.KindToolCall, driverkit.KindUsage)
	if evs[1].Text != "hm" || evs[2].Text != " hi " || evs[2].MessageID != "m1" {
		t.Errorf("content = %+v", evs)
	}
	if evs[3].ToolID != "t1" || evs[3].ToolName != "Bash" || string(evs[3].ToolInput) != `{"command":"ls"}` {
		t.Errorf("tool call = %+v", evs[3])
	}

	evs = d(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":"x"}]}}`)
	wantKinds(t, evs, driverkit.KindStart, driverkit.KindToolResult)
	if evs[1].ToolID != "t1" || evs[1].ToolName != "Bash" || !evs[1].IsError {
		t.Errorf("tool result = %+v", evs[1])
	}
}

func TestDecodeUserStringContentStillSignalsProgress(t *testing.T) {
	evs := newDecoderAt(decodeNow)(`{"type":"user","message":{"role":"user","content":"rate_limit_error quoted in the prompt"}}`)
	wantKinds(t, evs, driverkit.KindStart)
}

func TestDecodeSyntheticAssistantIsErrorNotProgress(t *testing.T) {
	line := `{"type":"assistant","error":"rate_limit","message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: 429"}]}}`
	evs := newDecoderAt(decodeNow)(line)
	wantKinds(t, evs, driverkit.KindError, driverkit.KindText, driverkit.KindUsage)
	if evs[0].Text != line || !evs[1].Synthetic {
		t.Errorf("events = %+v", evs)
	}
}

func TestDecodeMistypedAssistantEnvelopeYieldsNoContentOrUsage(t *testing.T) {
	evs := newDecoderAt(decodeNow)(`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":"5","output_tokens":7}}}`)
	wantKinds(t, evs, driverkit.KindStart)
}

func TestDecodeResultEchoLatchSpansInterveningLines(t *testing.T) {
	d := newDecoderAt(decodeNow)
	d(`{"type":"assistant","message":{"content":[{"type":"text","text":"429 Too Many Requests"}]}}`)
	d(`{"type":"system","subtype":"heartbeat"}`)
	echo := `{"type":"result","result":"429 Too Many Requests"}`
	wantKinds(t, d(echo), driverkit.KindResult)
	// One non-error result clears the latch, so the same echo is now a signal.
	wantKinds(t, d(echo), driverkit.KindResult, driverkit.KindError)
}

func TestDecodeResultVariants(t *testing.T) {
	d := newDecoderAt(decodeNow)
	d(`{"type":"assistant","message":{"content":[{"type":"text","text":"429 Too Many Requests"}]}}`)
	// An error result is never an echo, and does not clear the latch.
	evs := d(`{"type":"result","is_error":true,"result":"429 Too Many Requests"}`)
	wantKinds(t, evs, driverkit.KindResult, driverkit.KindError)
	// A different reason is not an echo of the pending one.
	evs = d(`{"type":"result","result":"529 Overloaded"}`)
	wantKinds(t, evs, driverkit.KindResult, driverkit.KindError)

	evs = newDecoderAt(decodeNow)(`{"type":"result","result":{"not":"a string"}}`)
	wantKinds(t, evs, driverkit.KindResult, driverkit.KindError)
	if evs[0].Text != "" {
		t.Errorf("non-string result text = %q", evs[0].Text)
	}
}

func TestDecodeOtherLinesAreErrorCandidates(t *testing.T) {
	d := newDecoderAt(decodeNow)
	for _, line := range []string{
		`{"type":"system","subtype":"init"}`,
		`{"type":"error","error":{"type":"rate_limit_error"},"resetsAt":1783192800}`,
		`{"resetsAt":1783192800}`,
		`null`,
	} {
		evs := d(line)
		wantKinds(t, evs, driverkit.KindError)
		if evs[0].Text != line {
			t.Errorf("text = %q, want %q", evs[0].Text, line)
		}
	}
	if evs := d(`{"resetsAt":1783192800}`); evs[0].ResetAt == nil || evs[0].ResetAt.Unix() != 1783192800 {
		t.Errorf("ResetAt = %v", evs[0].ResetAt)
	}
}

func TestDecodeRoleAttribution(t *testing.T) {
	d := newDecoderAt(decodeNow)
	d(hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpPassStart, Role: "review"}))
	evs := d(`{"type":"assistant","message":{"id":"m","content":[{"type":"tool_use","id":"task1","name":"Task","input":{"subagent_type":"scout"}}]}}`)
	if evs[1].Role != ReviewerRole {
		t.Errorf("top-level role = %q, want %q", evs[1].Role, ReviewerRole)
	}
	evs = d(`{"type":"assistant","parent_tool_use_id":"task1","message":{"id":"m2","content":[{"type":"text","text":"x"}]}}`)
	if evs[1].Role != "scout" {
		t.Errorf("subagent role = %q, want scout", evs[1].Role)
	}
}

func TestNewDecoderYieldsIndependentState(t *testing.T) {
	a, b := NewDecoder(), NewDecoder()
	a(`{"type":"assistant","message":{"content":[{"type":"text","text":"429 Too Many Requests"}]}}`)
	wantKinds(t, b(`{"type":"result","result":"429 Too Many Requests"}`), driverkit.KindResult, driverkit.KindError)
}

func TestDecodeAssistantUsage(t *testing.T) {
	d := newDecoderAt(decodeNow)
	d(`{"type":"assistant","message":{"id":"s","content":[{"type":"tool_use","id":"task1","name":"Task","input":{"subagent_type":"scout"}}]}}`)

	split := d(`{"type":"assistant","message":{"id":"m1","model":"claude-opus-4","content":"x","usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":99,"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":5}}}}`)
	wantKinds(t, split, driverkit.KindStart, driverkit.KindUsage)
	want := driverkit.Event{Kind: driverkit.KindUsage, MessageID: "m1", Model: "claude-opus-4", Tokens: driverkit.Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite5m: 4, CacheWrite1h: 5}}
	if !reflect.DeepEqual(split[1], want) {
		t.Errorf("usage = %+v, want %+v", split[1], want)
	}

	flat := d(`{"type":"assistant","parent_tool_use_id":"task1","message":{"id":"m2","usage":{"cache_creation_input_tokens":7}}}`)
	got := flat[len(flat)-1]
	if got.Tokens.CacheWrite5m != 7 || got.Tokens.CacheWrite1h != 0 || !got.InSubagent || got.Role != "scout" {
		t.Errorf("flat cache_creation usage = %+v", got)
	}

	// Re-emitted lines still decode in full: only the reader dedups.
	again := d(`{"type":"assistant","message":{"id":"m1","content":[]}}`)
	wantKinds(t, again, driverkit.KindStart, driverkit.KindUsage)
}

func TestDecodeResultCarriesSessionTotals(t *testing.T) {
	evs := newDecoderAt(decodeNow)(`{"type":"result","result":"done","num_turns":3,"total_cost_usd":0.25,"duration_ms":900,"duration_api_ms":400,"usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}`)
	want := driverkit.Event{
		Kind: driverkit.KindResult, Text: "done", DurationMs: 900, DurationAPIMs: 400, NumTurns: 3,
		Tokens: driverkit.Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite5m: 4, CostUSD: 0.25},
	}
	if !reflect.DeepEqual(evs[0], want) {
		t.Errorf("result = %+v, want %+v", evs[0], want)
	}
}

func TestDecodeMistypedResultFieldContributesNoTotals(t *testing.T) {
	// One mistyped total fails the totals decode as a whole: the result keeps
	// its text, carries no totals, and the usage reader skips it.
	evs := newDecoderAt(decodeNow)(`{"type":"result","result":"done","num_turns":"3","total_cost_usd":0.25,"usage":{"input_tokens":1}}`)
	wantKinds(t, evs, driverkit.KindResult, driverkit.KindError)
	want := driverkit.Event{Kind: driverkit.KindResult, Text: "done", NoTotals: true}
	if !reflect.DeepEqual(evs[0], want) {
		t.Errorf("result = %+v, want %+v", evs[0], want)
	}
}

func TestDecodeTimestampIsTopLevelOnly(t *testing.T) {
	d := newDecoderAt(decodeNow)
	const ms = int64(1786474800000) // 2026-08-11T19:00:00Z
	for name, line := range map[string]string{
		"assistant": `{"type":"assistant","timestamp":"2026-08-11T19:00:00.000Z"}`,
		"result":    `{"type":"result","timestamp":"2026-08-11T19:00:00Z"}`,
		"system":    `{"type":"system","timestamp":"2026-08-11T19:00:00.000Z"}`,
		"host op":   `{"type":"spindrift_op","timestamp":"2026-08-11T19:00:00.000Z","spindrift_op":{"op":"pass_start","role":"reviewer"}}`,
	} {
		for _, ev := range d(line) {
			if ev.Timestamp != ms {
				t.Errorf("%s: %s timestamp = %d, want %d", name, ev.Kind, ev.Timestamp, ms)
			}
		}
	}
	for name, line := range map[string]string{
		"no type":    `{"timestamp":"2026-08-11T19:00:00.000Z"}`,
		"nested":     `{"type":"user","message":{"content":[{"type":"tool_result","content":"{\"timestamp\":\"2026-08-11T19:00:00Z\"}"}]}}`,
		"unparsable": `{"type":"system","timestamp":"yesterday"}`,
	} {
		for _, ev := range d(line) {
			if ev.Timestamp != 0 {
				t.Errorf("%s: %s timestamp = %d, want 0", name, ev.Kind, ev.Timestamp)
			}
		}
	}
}

func TestDecodeToolFields(t *testing.T) {
	d := newDecoderAt(decodeNow)
	evs := d(`{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"t1","name":"Task","input":{"subagent_type":"reviewer"}},{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"ls"}}]}}`)
	if evs[1].ToolTarget != "reviewer" || evs[2].ToolTarget != "ls" {
		t.Errorf("tool targets = %q, %q", evs[1].ToolTarget, evs[2].ToolTarget)
	}
	evs = d(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"done"},{"type":"tool_result","tool_use_id":"t2","is_error":true,"content":[{"type":"text","text":"boom"}]}]}}`)
	if evs[1].ToolOutput != "done" || evs[1].Subagent != "reviewer" || evs[1].IsError {
		t.Errorf("spawn result = output %q subagent %q error %v", evs[1].ToolOutput, evs[1].Subagent, evs[1].IsError)
	}
	if evs[2].ToolOutput != "boom" || evs[2].Subagent != "" || !evs[2].IsError {
		t.Errorf("ordinary result = output %q subagent %q error %v", evs[2].ToolOutput, evs[2].Subagent, evs[2].IsError)
	}
}

func TestDecodeToolBlocksOnlyInTheirOwnTurnType(t *testing.T) {
	d := newDecoderAt(decodeNow)
	evs := d(`{"type":"user","message":{"content":[{"type":"tool_use","id":"t1","name":"Task","input":{"subagent_type":"reviewer"}}]}}`)
	wantKinds(t, evs, driverkit.KindStart)
	evs = d(`{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}}`)
	wantKinds(t, evs, driverkit.KindStart, driverkit.KindUsage)
	// The user-turn spawn recorded no role, so a later result is not a subagent report.
	evs = d(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}}`)
	wantKinds(t, evs, driverkit.KindStart, driverkit.KindToolResult)
	if evs[1].Subagent != "" {
		t.Errorf("subagent = %q, want none", evs[1].Subagent)
	}
}

func TestDecodeUserTextBlocksAreNotTranscriptText(t *testing.T) {
	evs := newDecoderAt(decodeNow)(`{"type":"user","parent_tool_use_id":"t1","message":{"content":[{"type":"text","text":"do the thing"}]}}`)
	wantKinds(t, evs, driverkit.KindStart)
}

func TestDecodeMistypedTotalsKeepTextAndEchoLatch(t *testing.T) {
	d := newDecoderAt(decodeNow)
	d(`{"type":"assistant","message":{"content":[{"type":"text","text":"API Error: 529 overloaded_error"}]}}`)
	evs := d(`{"type":"result","result":"API Error: 529 overloaded_error","num_turns":"3"}`)
	wantKinds(t, evs, driverkit.KindResult)
	if evs[0].Text != "API Error: 529 overloaded_error" || !evs[0].NoTotals {
		t.Errorf("result = %+v", evs[0])
	}
	// A mistyped is_error is no echo (the old envelope failed to decode), but
	// the text and totals survive.
	d(`{"type":"assistant","message":{"content":[{"type":"text","text":"API Error: 529 overloaded_error"}]}}`)
	evs = d(`{"type":"result","result":"API Error: 529 overloaded_error","is_error":"no","num_turns":3}`)
	wantKinds(t, evs, driverkit.KindResult, driverkit.KindError)
	if evs[0].NumTurns != 3 || evs[0].NoTotals {
		t.Errorf("result = %+v", evs[0])
	}
}
