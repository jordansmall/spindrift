package claude

import (
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
	wantKinds(t, evs, driverkit.KindStart, driverkit.KindReasoning, driverkit.KindText, driverkit.KindToolCall)
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
	wantKinds(t, evs, driverkit.KindError, driverkit.KindText)
	if evs[0].Text != line || !evs[1].Synthetic {
		t.Errorf("events = %+v", evs)
	}
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
