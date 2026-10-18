package pi_test

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/pi"
	"spindrift.dev/launcher/internal/usage"
)

// childAssistant is one assistant message of a subagent's results[].messages;
// childUsage is its tally, deliberately unlike stubUsage.
func childAssistant(ts int, content string) string {
	return fmt.Sprintf(`{"role":"assistant","content":%s,"provider":"stub","model":"child-model","usage":%s,"stopReason":"stop","timestamp":%d,"durationMs":5,"thinkingLevel":"high"}`, content, childUsage, ts)
}

const childUsage = `{"input":7,"output":3,"cacheRead":5,"cacheWrite":0,"cost":{"total":0.5}}`

// subagentEnd is a subagent tool_execution_end.
func subagentEnd(mode string, results ...string) string {
	return `{"type":"tool_execution_end","toolCallId":"s1","toolName":"subagent","result":{"content":[{"type":"text","text":"report"}],"details":{"mode":"` + mode + `","results":[` + strings.Join(results, ",") + `]}},"isError":false}`
}

// subagentResult carries, beside the child's messages, the extension's own
// summed usage: numbers far from the messages', so counting it would show.
func subagentResult(agent string, messages ...string) string {
	return `{"agent":"` + agent + `","exitCode":0,"messages":[` + strings.Join(messages, ",") + `],"usage":{"input":9000,"output":8000,"cacheRead":7000,"cacheWrite":6000,"cost":99,"turns":50}}`
}

func subagentLog(t *testing.T) string {
	t.Helper()
	return pi.WriteLog(t,
		toolTurn(1000, "s1", "subagent", `{"agent":"scout","task":"look"}`),
		subagentEnd("parallel",
			// Both children share timestamp 1500: only the agent and index tell
			// their messages apart.
			subagentResult("scout",
				`{"role":"user","content":[{"type":"text","text":"Task: look"}]}`,
				childAssistant(1500, `[{"type":"toolCall","id":"k1","name":"read","arguments":{"path":"a.go"}}]`),
				`{"role":"toolResult","toolCallId":"k1","toolName":"read","content":[{"type":"text","text":"file body"}],"isError":false}`,
				childAssistant(1600, `[{"type":"text","text":"child says hi"}]`)),
			subagentResult("scout", childAssistant(1500, `[{"type":"text","text":"second"}]`)),
		),
		`{"type":"tool_execution_update","toolCallId":"s1","toolName":"subagent","partialResult":{"details":{"results":[`+subagentResult("scout", childAssistant(1500, `[]`))+`]}}}`,
		textEnd(2000, "parent done"),
	)
}

// The per-agent usage sums the child assistant messages only: the extension's
// own totals and its repeated tool_execution_update are not counted on top, and
// the main loop's row excludes the children.
func TestUsage_SubagentChildMessagesOnly(t *testing.T) {
	rep, err := newDriver().ExtractUsage(subagentLog(t))
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	got := map[string]usage.AgentUsage{}
	for _, a := range rep.SummedByAgent {
		got[a.Agent] = a
	}
	if len(got) != 2 {
		t.Fatalf("agents = %+v, want main and scout", rep.SummedByAgent)
	}
	if m := got["main"]; m.APICalls != 2 || m.UncachedInputTokens != 160 || m.OutputTokens != 60 {
		t.Errorf("main = %+v, want 2 calls, 160 in, 60 out", m)
	}
	if s := got["scout"]; s.APICalls != 3 || s.UncachedInputTokens != 21 || s.OutputTokens != 9 || s.CacheReadInputTokens != 15 {
		t.Errorf("scout = %+v, want 3 calls, 21 in, 9 out, 15 cache read", s)
	}
	if u := rep.Totals; u.InputTokens != 181 || u.OutputTokens != 69 || u.TotalCostUSD < 1.5 || u.TotalCostUSD > 1.6 {
		t.Errorf("totals = %+v, want 181 in, 69 out, ~1.5 cost (no extension totals)", u)
	}
}

func TestSubagent_ShownUnderItsRoleNotAsMainNarration(t *testing.T) {
	path := subagentLog(t)
	d := newDriver()
	tr, err := d.RenderTranscript(path, driverkit.RenderOptions{})
	if err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	for _, want := range []string{"[scout] read(a.go)", "[scout]   -> file body", "[scout] child says hi", "-> [scout,scout] report"} {
		if !strings.Contains(tr, want) {
			t.Errorf("transcript lacks %q:\n%s", want, tr)
		}
	}
	var out, raw strings.Builder
	var roles []string
	w := d.NewHeartbeatWriter(&raw, "42", &out, driverkit.RenderOptions{
		OnModel: func(_, role string) { roles = append(roles, role) },
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roles, []string{"", "scout", ""}) {
		t.Errorf("OnModel roles = %q, want main, scout, main", roles)
	}
	if strings.Contains(out.String(), "child says hi") {
		t.Errorf("heartbeat narrates a child's text:\n%s", out.String())
	}
	// A run cut off right after the subagent returns must not report a child's
	// text as the last message.
	cut := pi.WriteLog(t, toolTurn(1000, "s1", "subagent", `{}`), subagentEnd("single", subagentResult("scout", childAssistant(1500, `[{"type":"text","text":"child says hi"}]`))))
	if last, err := driverkit.LastMessageText(cut, pi.NewDecoder); err != nil || last != "" {
		t.Errorf("LastMessageText = %q, %v, want no text", last, err)
	}
}
