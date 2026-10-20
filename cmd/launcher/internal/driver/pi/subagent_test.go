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

// subagentUpdate is a subagent tool_execution_update: partialResult carries the
// children's messages so far, cumulatively.
func subagentUpdate(results ...string) string {
	return `{"type":"tool_execution_update","toolCallId":"s1","toolName":"subagent","args":{},"partialResult":{"content":[{"type":"text","text":"(running...)"}],"details":{"mode":"parallel","results":[` + strings.Join(results, ",") + `]}}}`
}

// Both children share timestamp 1500: only the agent and index tell their
// messages apart.
var (
	scoutUser   = `{"role":"user","content":[{"type":"text","text":"Task: look"}]}`
	scoutCall   = childAssistant(1500, `[{"type":"toolCall","id":"k1","name":"read","arguments":{"path":"a.go"}}]`)
	scoutResult = `{"role":"toolResult","toolCallId":"k1","toolName":"read","content":[{"type":"text","text":"file body"}],"isError":false}`
	scoutSays   = childAssistant(1600, `[{"type":"text","text":"child says hi"}]`)
	secondSays  = childAssistant(1500, `[{"type":"text","text":"second"}]`)
)

// subagentUpdates are the cumulative updates ahead of the end: each repeats the
// earlier messages verbatim, as the extension's does.
func subagentUpdates() []string {
	return []string{
		subagentUpdate(subagentResult("scout", scoutUser, scoutCall)),
		subagentUpdate(subagentResult("scout", scoutUser, scoutCall, scoutResult, scoutSays)),
		subagentUpdate(subagentResult("scout", scoutUser, scoutCall, scoutResult, scoutSays), subagentResult("scout", secondSays)),
	}
}

func subagentLog(t *testing.T) string {
	t.Helper()
	lines := []string{toolTurn(1000, "s1", "subagent", `{"agent":"scout","task":"look"}`)}
	lines = append(lines, subagentUpdates()...)
	lines = append(lines,
		subagentEnd("parallel",
			subagentResult("scout", scoutUser, scoutCall, scoutResult, scoutSays),
			subagentResult("scout", secondSays),
		),
		textEnd(2000, "parent done"),
	)
	return pi.WriteLog(t, lines...)
}

// byAgent indexes a report's per-agent rows by agent name.
func byAgent(rep usage.Report) map[string]usage.AgentUsage {
	got := map[string]usage.AgentUsage{}
	for _, a := range rep.SummedByAgent {
		got[a.Agent] = a
	}
	return got
}

// A run killed mid-subagent has no tool_execution_end: the child calls its
// updates already carried still count.
func TestUsage_SubagentCutAfterUpdate(t *testing.T) {
	lines := append([]string{toolTurn(1000, "s1", "subagent", `{}`)}, subagentUpdates()[:2]...)
	rep, err := newDriver().ExtractUsage(pi.WriteLog(t, lines...))
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	got := byAgent(rep)
	if s := got["scout"]; s.APICalls != 2 || s.UncachedInputTokens != 14 || s.OutputTokens != 6 {
		t.Errorf("scout = %+v, want the update's 2 calls, 14 in, 6 out", s)
	}
	if u := rep.Totals; u.InputTokens != 94 || u.TotalCostUSD < 1.0 {
		t.Errorf("totals = %+v, want 94 in and the children's ~1.0 cost", u)
	}
}

// The per-agent usage sums the child assistant messages only: the extension's
// own totals are not counted on top, each child call is counted once however
// many cumulative tool_execution_updates repeated it, and the main loop's row
// excludes the children.
func TestUsage_SubagentChildMessagesOnly(t *testing.T) {
	rep, err := newDriver().ExtractUsage(subagentLog(t))
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	got := byAgent(rep)
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

// A provider may reuse a toolCallId across subagent calls: the first call's
// emitted-message count must not hide the second call's children.
func TestUsage_SubagentReusedToolCallID(t *testing.T) {
	first := []string{scoutUser, scoutCall, scoutResult, scoutSays}
	second := []string{
		scoutUser,
		childAssistant(2500, `[{"type":"text","text":"again"}]`),
		childAssistant(2600, `[{"type":"text","text":"more"}]`),
	}
	lines := []string{
		toolTurn(1000, "s1", "subagent", `{}`),
		subagentUpdate(subagentResult("scout", first[:2]...)),
		subagentEnd("single", subagentResult("scout", first...)),
		toolTurn(2400, "s1", "subagent", `{}`),
		subagentUpdate(subagentResult("scout", second[:2]...)),
		subagentEnd("single", subagentResult("scout", second...)),
	}
	rep, err := newDriver().ExtractUsage(pi.WriteLog(t, lines...))
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	if s := byAgent(rep)["scout"]; s.APICalls != 4 || s.UncachedInputTokens != 28 || s.OutputTokens != 12 {
		t.Errorf("scout = %+v, want both calls' 4 child calls, 28 in, 12 out", s)
	}
}
