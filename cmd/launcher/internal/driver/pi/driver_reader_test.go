package pi_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/pi"
)

func toolTurn(ts int, id, name, args string) string {
	return assistantEnd(ts, `[{"type":"toolCall","id":"`+id+`","name":"`+name+`","arguments":`+args+`}]`, "toolUse", "")
}

func toolEnd(id, name, out string, isErr bool) string {
	flag := "false"
	if isErr {
		flag = "true"
	}
	return `{"type":"tool_execution_end","toolCallId":"` + id + `","toolName":"` + name + `","result":{"content":[{"type":"text","text":"` + out + `"}]},"isError":` + flag + `}`
}

// A retried 429 followed by a normal answer: the attempt that failed is
// recovered, so the log carries no unrecovered error.
func retriedThenAnswered() []string {
	return []string{
		errorEnd(1000, rateLimitMsg), `{"type":"agent_end","willRetry":true}`, retryStart,
		textEnd(2000, "ok"), `{"type":"auto_retry_end","success":true,"attempt":1}`, agentEnd, settled,
	}
}

// Exhausted retries: pi still exits 0, with the last attempt's error the only
// trace of the failure.
func exhausted() []string {
	return []string{
		errorEnd(1000, rateLimitMsg), `{"type":"agent_end","willRetry":true}`, retryStart,
		errorEnd(2000, rateLimitMsg), agentEnd,
		`{"type":"auto_retry_end","success":false,"attempt":3,"finalError":"429: x"}`, settled,
	}
}

func TestUsage_OneTurnPerAssistantMessage(t *testing.T) {
	path := pi.WriteLog(t,
		toolTurn(1000, "c1", "bash", `{"command":"ls"}`),
		`{"type":"message_update","usage":`+stubUsage+`,"assistantMessageEvent":{"type":"text_delta","delta":"x"}}`,
		toolEnd("c1", "bash", "ok", false),
		textEnd(2000, "done"),
		`{"type":"turn_end","message":{"role":"assistant","usage":`+stubUsage+`},"toolResults":[]}`,
		`{"type":"agent_end","willRetry":false,"messages":[{"role":"assistant","usage":`+stubUsage+`}]}`,
	)
	rep, err := newDriver().ExtractUsage(path)
	if err != nil {
		t.Fatalf("ExtractUsage: %v", err)
	}
	u := rep.Totals
	if u.NumTurns != 2 || u.InputTokens != 160 || u.OutputTokens != 60 || u.CacheReadInputTokens != 80 {
		t.Errorf("summed = %+v, want 2 turns, 160 in, 60 out, 80 cache read", u)
	}
	if u.DurationMs != 2035-1000 {
		t.Errorf("DurationMs = %d, want %d", u.DurationMs, 2035-1000)
	}
	if got := len(rep.SummedByModel); got != 1 || rep.SummedByModel[0].Model != "stub/stub-model:high" {
		t.Errorf("SummedByModel = %+v, want one stub/stub-model:high row", rep.SummedByModel)
	}
}

func TestResultText_FinalAssistantMessageOnly(t *testing.T) {
	path := pi.WriteLog(t,
		textEnd(1000, outcomeText+" (midway)"),
		toolTurn(2000, "c1", "bash", `{"command":"ls"}`),
		toolEnd("c1", "bash", "ok", false),
		textEnd(3000, "All done."),
		agentEnd, settled,
	)
	got, err := newDriver().ResultText(path)
	if err != nil {
		t.Fatalf("ResultText: %v", err)
	}
	if want := "All done.\n"; got != want {
		t.Errorf("ResultText = %q, want %q", got, want)
	}
}

func TestResultText_FailedFinalTurnIsEmpty(t *testing.T) {
	path := pi.WriteLog(t, textEnd(1000, "earlier prose"), errorEnd(2000, badKeyMsg), agentEnd, settled)
	got, err := newDriver().ResultText(path)
	if err != nil {
		t.Fatalf("ResultText: %v", err)
	}
	if got != "" {
		t.Errorf("ResultText = %q, want empty", got)
	}
}

func TestResultEvent_RoundTrips(t *testing.T) {
	d := newDriver()
	for _, text := range []string{outcomeText, "multi\nline \"quoted\"", ""} {
		b, err := d.ResultEvent(text)
		if err != nil {
			t.Fatalf("ResultEvent: %v", err)
		}
		if !strings.HasSuffix(string(b), "\n") || strings.Count(string(b), "\n") != 1 {
			t.Fatalf("ResultEvent(%q) = %q, want exactly one newline-terminated line", text, b)
		}
		path := pi.WriteLog(t, textEnd(1000, "earlier prose"))
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.Write(b)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		got, err := d.ResultText(path)
		if err != nil {
			t.Fatalf("ResultText: %v", err)
		}
		want := ""
		if text != "" {
			want = text + "\n"
		}
		if got != want {
			t.Errorf("round trip of %q: ResultText = %q, want %q", text, got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		lines  []string
		class  driverkit.Class
		reason driverkit.Reason
	}{
		{"exhausted 429", exhausted(), driverkit.Transient, driverkit.RateLimit},
		{"retried 429 then answer", retriedThenAnswered(), driverkit.Terminal, driverkit.TaskFailed},
		{"401 is terminal", []string{errorEnd(1000, badKeyMsg), agentEnd}, driverkit.Terminal, driverkit.TaskFailed},
		{"400 is terminal", []string{errorEnd(1000, `400: {"message":"bad things","type":"invalid_request_error"}`), agentEnd}, driverkit.Terminal, driverkit.TaskFailed},
		{"529 overloaded", []string{errorEnd(1000, `529: {"type":"error","error":{"type":"overloaded_error"}}`)}, driverkit.Transient, driverkit.Overloaded},
		{"503", []string{errorEnd(1000, `503: Service Unavailable`)}, driverkit.Transient, driverkit.Overloaded},
		{"502", []string{errorEnd(1000, `502: Bad Gateway`)}, driverkit.Transient, driverkit.Network},
		{"fetch failed", []string{errorEnd(1000, `Connection error: fetch failed`)}, driverkit.Transient, driverkit.Network},
		{"ECONNRESET", []string{errorEnd(1000, `read ECONNRESET`)}, driverkit.Transient, driverkit.Network},
		{"prose quoting a marker", []string{textEnd(1000, "I saw a rate_limit_error and a 429: earlier")}, driverkit.Terminal, driverkit.TaskFailed},
		{"token count is not a status", []string{errorEnd(1000, `context window 14290 tokens exceeded`)}, driverkit.Terminal, driverkit.TaskFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newDriver().ClassifyTransient(pi.WriteLog(t, tc.lines...))
			if err != nil {
				t.Fatalf("ClassifyTransient: %v", err)
			}
			if got.Class != tc.class || got.Reason != tc.reason {
				t.Errorf("got %s/%s, want %s/%s", got.Class, got.Reason, tc.class, tc.reason)
			}
		})
	}
}

func TestResolveExit(t *testing.T) {
	cases := []struct {
		name     string
		lines    []string
		exitCode int
		want     int
	}{
		{"clean answer", []string{textEnd(1000, outcomeText), agentEnd, settled}, 0, 0},
		{"failed turn exits 0 in pi", []string{errorEnd(1000, badKeyMsg), agentEnd, settled}, 0, 1},
		{"retries exhausted", exhausted(), 0, 1},
		{"retried then answered", retriedThenAnswered(), 0, 0},
		{"recovered then a later unrecovered error", append(retriedThenAnswered(), errorEnd(3000, badKeyMsg)), 0, 1},
		{"tool call is progress", []string{errorEnd(1000, badKeyMsg), toolTurn(2000, "c", "bash", `{}`)}, 0, 0},
		{"non-zero rc kept", []string{errorEnd(1000, badKeyMsg)}, 3, 3},
		{"non-zero rc kept without error", []string{textEnd(1000, "x")}, 17, 17},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pi.ResolveExit(pi.WriteLog(t, tc.lines...), tc.exitCode)
			if err != nil {
				t.Fatalf("ResolveExit: %v", err)
			}
			if got != tc.want {
				t.Errorf("code: got %d, want %d", got, tc.want)
			}
		})
	}
}

// A missing log means no error was observed, so the exit code passes through.
func TestResolveExit_MissingFile_PassesExitCodeThrough(t *testing.T) {
	for _, rc := range []int{0, 7} {
		got, err := pi.ResolveExit(filepath.Join(t.TempDir(), "does-not-exist.log"), rc)
		if err != nil || got != rc {
			t.Errorf("rc %d: got %d, %v; want it passed through", rc, got, err)
		}
	}
}

func TestSessionFlags_RenderNothingYet(t *testing.T) {
	for _, mode := range []string{"initial", "resume", ""} {
		if got := newDriver().SessionFlags(mode, "owner/repo", "42", t.TempDir()); got != "" {
			t.Errorf("SessionFlags(%q) = %q, want empty", mode, got)
		}
	}
}

func TestTranscript(t *testing.T) {
	path := pi.WriteLog(t,
		assistantEnd(1000, `[{"type":"thinking","thinking":"private"},{"type":"text","text":"Looking."},{"type":"toolCall","id":"c1","name":"bash","arguments":{"command":"ls"}}]`, "toolUse", ""),
		toolEnd("c1", "bash", "a.txt", false),
		textEnd(2000, "Done."),
	)
	got, err := newDriver().RenderTranscript(path, driverkit.RenderOptions{})
	if err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	for _, want := range []string{"Looking.", "bash(ls)", "-> a.txt", "Done."} {
		if !strings.Contains(got, want) {
			t.Errorf("transcript lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "private") {
		t.Errorf("transcript leaks reasoning:\n%s", got)
	}
}

// The model and thinking level in force reach the heartbeat's OnModel hook as
// one id, so the live header and the Dispatch Record both show the effort.
func TestHeartbeat_ReportsModelWithThinkingLevel(t *testing.T) {
	var out, raw strings.Builder
	var seen []string
	w := newDriver().NewHeartbeatWriter(&raw, "42", &out, driverkit.RenderOptions{
		OnModel: func(model, _ string) { seen = append(seen, model) },
	})
	for _, l := range []string{textEnd(1000, "a"), textEnd(2000, "b")} {
		if _, err := w.Write([]byte(l + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(seen, []string{"stub/stub-model:high"}) {
		t.Errorf("OnModel saw %v, want one stub/stub-model:high", seen)
	}
}

func TestReadPasses_TracksWriteModelsAndTotals(t *testing.T) {
	path := pi.WriteLog(t,
		toolTurn(1000, "w1", "write", `{"path":"OUT.md","content":"report body"}`),
		toolEnd("w1", "write", "wrote", false),
		textEnd(2000, "Done."),
	)
	passes, err := newDriver().ReadPasses(path, "OUT.md", nil)
	if err != nil || len(passes) != 1 {
		t.Fatalf("ReadPasses: %v, %d passes", err, len(passes))
	}
	p := passes[0]
	if !p.Wrote || p.Written != "report body" {
		t.Errorf("Wrote/Written = %v/%q, want true/\"report body\"", p.Wrote, p.Written)
	}
	if p.Usage.NumTurns != 2 || p.APICalls != 2 {
		t.Errorf("turns/APICalls = %d/%d, want 2/2", p.Usage.NumTurns, p.APICalls)
	}
	if !reflect.DeepEqual(p.Models, []string{"stub/stub-model:high"}) {
		t.Errorf("Models = %v, want [stub/stub-model:high]", p.Models)
	}
	if !reflect.DeepEqual(p.Results, []string{"Done.\n"}) {
		t.Errorf("Results = %q, want [\"Done.\\n\"]", p.Results)
	}
}
