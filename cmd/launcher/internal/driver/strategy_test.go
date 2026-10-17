package driver

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/usage"
)

// fakeDecoder reads tab-separated lines: kind, then that kind's fields.
//
//	text	id	body
//	model	id
//	usage	msgid	model	input	output
//	result	body	output
//	tool	id	name	target
//	toolres	id	output
//	err	text
//
// Any other line is KindUnparsed.
func fakeDecoder() driverkit.Decoder {
	return func(line string) []driverkit.Event {
		f := strings.Split(line, "\t")
		num := func(i int) int {
			n, _ := strconv.Atoi(f[i])
			return n
		}
		switch {
		case f[0] == "text" && len(f) == 3:
			return []driverkit.Event{{Kind: driverkit.KindText, MessageID: f[1], Text: f[2]}}
		case f[0] == "model" && len(f) == 2:
			return []driverkit.Event{{Kind: driverkit.KindModel, Model: f[1]}}
		case f[0] == "usage" && len(f) == 5:
			return []driverkit.Event{{Kind: driverkit.KindUsage, MessageID: f[1], Model: f[2],
				Tokens: driverkit.Tokens{Input: num(3), Output: num(4)}}}
		case f[0] == "result" && len(f) == 3:
			return []driverkit.Event{{Kind: driverkit.KindResult, Text: f[1], NumTurns: 1,
				Tokens: driverkit.Tokens{Output: num(2)}}}
		case f[0] == "tool" && len(f) == 4:
			return []driverkit.Event{{Kind: driverkit.KindToolCall, ToolID: f[1], ToolName: f[2], ToolTarget: f[3],
				ToolInput: json.RawMessage(`{"content":"x"}`)}}
		case f[0] == "toolres" && len(f) == 3:
			return []driverkit.Event{{Kind: driverkit.KindToolResult, ToolID: f[1], ToolOutput: f[2]}}
		case f[0] == "err" && len(f) == 2:
			return []driverkit.Event{{Kind: driverkit.KindError, Text: f[1]}}
		}
		return []driverkit.Event{{Kind: driverkit.KindUnparsed, Text: line}}
	}
}

func fakeStrategy() driverkit.Strategy {
	return driverkit.Strategy{
		Name:        "fake",
		NewDecoder:  fakeDecoder,
		ResultEvent: func(text string) ([]byte, error) { return []byte("result\t" + text + "\t0\n"), nil },
		SessionFlags: func(mode, repoSlug, issue, home string) string {
			return strings.Join([]string{mode, repoSlug, issue, home}, "|")
		},
	}
}

func writeFakeLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stream.log")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStrategyDriverName(t *testing.T) {
	if got := (strategyDriver{fakeStrategy()}).Name(); got != "fake" {
		t.Errorf("Name() = %q, want fake", got)
	}
}

func TestStrategyDriverClassifyTransient(t *testing.T) {
	s := fakeStrategy()
	s.TransientPatterns = []driverkit.Pattern{{Substr: "slow down", Reason: driverkit.RateLimit}}
	s.TerminalPatterns = []driverkit.Pattern{{Substr: "bad flag", Reason: driverkit.UnsupportedFlag}}
	d := strategyDriver{s}

	cases := []struct {
		name string
		log  string
		want Classification
	}{
		{"transient pattern", "err\tplease slow down", Classification{Class: Transient, Reason: RateLimit}},
		{"terminal pattern", "err\tbad flag --x", Classification{Class: Terminal, Reason: UnsupportedFlag}},
		{"no marker", "err\tboom", Classification{Class: Terminal, Reason: TaskFailed}},
		{"recovered by progress", "err\tplease slow down\ntext\tm1\tback at it", Classification{Class: Terminal, Reason: TaskFailed}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := d.ClassifyTransient(writeFakeLog(t, tc.log))
			if err != nil {
				t.Fatalf("ClassifyTransient: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestStrategyDriverClassifyHonoursClassifyOptions(t *testing.T) {
	s := fakeStrategy()
	s.TransientPatterns = []driverkit.Pattern{{Substr: "slow down", Reason: driverkit.RateLimit}, {Substr: "overloaded", Reason: driverkit.Overloaded}}
	log := writeFakeLog(t, "err\tslow down", "err\toverloaded")

	got, err := strategyDriver{s}.ClassifyTransient(log)
	if err != nil || got.Reason != Overloaded {
		t.Fatalf("default: got %+v, %v; want the last marker (overloaded)", got, err)
	}
	s.Classify = driverkit.ClassifyOptions{FirstMatch: true}
	got, err = strategyDriver{s}.ClassifyTransient(log)
	if err != nil || got.Reason != RateLimit {
		t.Fatalf("FirstMatch: got %+v, %v; want the first marker (rateLimit)", got, err)
	}
}

func TestStrategyDriverHeartbeatWriter(t *testing.T) {
	var raw, out bytes.Buffer
	var models []string
	w := strategyDriver{fakeStrategy()}.NewHeartbeatWriter(&raw, "77", &out, driverkit.RenderOptions{
		OnModel: func(model, role string) { models = append(models, model+"/"+role) },
	})

	ndjson := "model\tm1\ntext\tid1\tdoing the thing\n"
	if _, err := w.Write([]byte(ndjson)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if raw.String() != ndjson {
		t.Errorf("raw not byte-exact:\ngot:  %q\nwant: %q", raw.String(), ndjson)
	}
	if !strings.Contains(out.String(), "#77") || !strings.Contains(out.String(), "doing the thing") {
		t.Errorf("heartbeat output missing issue prefix or narration: %q", out.String())
	}
	if len(models) != 1 {
		t.Errorf("OnModel calls = %v, want one", models)
	}
}

func TestStrategyDriverHeartbeatTopLevelRole(t *testing.T) {
	cases := []struct {
		name        string
		defaultRole string
		callerRole  string
		want        string
	}{
		{"default fills an empty caller role", "implementor", "", "m1/implementor"},
		{"caller role wins over the default", "implementor", "reviewer", "m1/reviewer"},
		{"no default: no role", "", "", "m1/"},
		{"no default: caller role ignored", "", "reviewer", "m1/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fakeStrategy()
			s.DefaultTopLevelRole = tc.defaultRole
			var got []string
			w := strategyDriver{s}.NewHeartbeatWriter(&bytes.Buffer{}, "1", &bytes.Buffer{}, driverkit.RenderOptions{
				TopLevelRole: tc.callerRole,
				OnModel:      func(model, role string) { got = append(got, model+"/"+role) },
			})
			if _, err := w.Write([]byte("model\tm1\n")); err != nil {
				t.Fatalf("Write: %v", err)
			}
			if len(got) != 1 || got[0] != tc.want {
				t.Errorf("OnModel = %v, want [%s]", got, tc.want)
			}
		})
	}
}

func TestStrategyDriverHeartbeatAppliesTracking(t *testing.T) {
	s := fakeStrategy()
	s.DefaultTopLevelRole = "implementor"
	s.Heartbeat = driverkit.HeartbeatTracking{Kind: func(string) string { return "edit" }}
	var out bytes.Buffer
	w := strategyDriver{s}.NewHeartbeatWriter(&bytes.Buffer{}, "1", &out, driverkit.RenderOptions{})
	if _, err := w.Write([]byte("model\tm1\ntool\tt1\tEdit\ta.go\nresult\tdone\t0\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if !strings.Contains(out.String(), "edit") {
		t.Errorf("heartbeat output carries no tracked count: %q", out.String())
	}
}

func TestStrategyDriverExtractUsage(t *testing.T) {
	log := writeFakeLog(t,
		"usage\tm1\tmodel-a\t10\t1",
		"usage\tm2\tmodel-b\t20\t2",
		"result\tdone\t50",
	)

	t.Run("SessionTotals take the result event", func(t *testing.T) {
		s := fakeStrategy()
		s.Totals = driverkit.SessionTotals
		report, err := strategyDriver{s}.ExtractUsage(log)
		if err != nil {
			t.Fatalf("ExtractUsage: %v", err)
		}
		if !report.Found || report.Totals.OutputTokens != 50 || !report.OutputIsMainLoopOnly {
			t.Errorf("report = %+v, want output 50 from the result event, main-loop-only", report)
		}
		if len(report.SummedByModel) != 2 {
			t.Errorf("SummedByModel = %+v, want 2 rows", report.SummedByModel)
		}
	})

	t.Run("PerCallTotals sum the usage events", func(t *testing.T) {
		report, err := strategyDriver{fakeStrategy()}.ExtractUsage(log)
		if err != nil {
			t.Fatalf("ExtractUsage: %v", err)
		}
		if report.Totals.OutputTokens != 3 || report.Totals.InputTokens != 30 || report.OutputIsMainLoopOnly {
			t.Errorf("report = %+v, want 30 in / 3 out summed per call", report)
		}
	})

	t.Run("Heartbeat.Family orders the per-model rows", func(t *testing.T) {
		s := fakeStrategy()
		s.Heartbeat.Family = func(model string) (string, int) {
			if model == "model-b" {
				return "b", 0
			}
			return "a", 1
		}
		report, err := strategyDriver{s}.ExtractUsage(log)
		if err != nil {
			t.Fatalf("ExtractUsage: %v", err)
		}
		if len(report.SummedByModel) != 2 || report.SummedByModel[0].Model != "model-b" {
			t.Errorf("SummedByModel = %+v, want model-b first by family rank", report.SummedByModel)
		}
	})

	t.Run("usageByModel failure degrades only the model section", func(t *testing.T) {
		orig := usageByModel
		t.Cleanup(func() { usageByModel = orig })
		usageByModel = func(string, func() driverkit.Decoder, driverkit.ModelFamily) ([]usage.ModelUsage, error) {
			return nil, errors.New("simulated I/O error")
		}
		report, err := strategyDriver{fakeStrategy()}.ExtractUsage(log)
		if err != nil {
			t.Fatalf("ExtractUsage: %v", err)
		}
		if !report.Found || report.SummedByModel != nil || report.SummedByAgent == nil {
			t.Errorf("report = %+v, want totals and agents kept, models nil", report)
		}
	})

	t.Run("usageByAgent failure degrades only the agent section", func(t *testing.T) {
		orig := usageByAgent
		t.Cleanup(func() { usageByAgent = orig })
		usageByAgent = func(string, func() driverkit.Decoder, driverkit.TotalsSource) ([]usage.AgentUsage, error) {
			return nil, errors.New("simulated I/O error")
		}
		report, err := strategyDriver{fakeStrategy()}.ExtractUsage(log)
		if err != nil {
			t.Fatalf("ExtractUsage: %v", err)
		}
		if !report.Found || report.SummedByAgent != nil || report.SummedByModel == nil {
			t.Errorf("report = %+v, want totals and models kept, agents nil", report)
		}
	})
}

func TestStrategyDriverRenderTranscript(t *testing.T) {
	log := writeFakeLog(t, "text\tm1\thello", "tool\tt1\tBash\tls", "toolres\tt1\tok")
	d := strategyDriver{fakeStrategy()}

	got, err := d.RenderTranscript(log, driverkit.RenderOptions{TopLevelRole: "reviewer"})
	if err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	if !strings.Contains(got, "[reviewer] hello") || !strings.Contains(got, "[reviewer] Bash(ls)") {
		t.Errorf("transcript = %q, want the caller's role on every line", got)
	}

	got, err = d.RenderTranscript(log, driverkit.RenderOptions{})
	if err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	if !strings.Contains(got, "["+driverkit.ImplementorRole+"] hello") {
		t.Errorf("transcript = %q, want the implementor default for an empty role", got)
	}
}

// DefaultTopLevelRole is the heartbeat's alone: a Strategy without one still
// renders the caller's role in the transcript.
func TestStrategyDriverRenderTranscriptIgnoresDefaultTopLevelRole(t *testing.T) {
	s := fakeStrategy()
	s.DefaultTopLevelRole = "heartbeat-only"
	got, err := strategyDriver{s}.RenderTranscript(writeFakeLog(t, "text\tm1\thello"), driverkit.RenderOptions{TopLevelRole: "reviewer"})
	if err != nil {
		t.Fatalf("RenderTranscript: %v", err)
	}
	if !strings.Contains(got, "[reviewer] hello") {
		t.Errorf("transcript = %q, want [reviewer] hello", got)
	}
}

func TestStrategyDriverResolveExit(t *testing.T) {
	log := writeFakeLog(t, "text\tm1\thi")

	t.Run("nil hook keeps the exit code", func(t *testing.T) {
		for _, code := range []int{0, 1, 137} {
			got, err := strategyDriver{fakeStrategy()}.ResolveExit(log, code)
			if err != nil || got != code {
				t.Errorf("ResolveExit(%d) = %d, %v; want unchanged", code, got, err)
			}
		}
	})

	t.Run("hook decides", func(t *testing.T) {
		s := fakeStrategy()
		var gotPath string
		s.ResolveExit = func(logPath string, exitCode int) (int, error) {
			gotPath = logPath
			return exitCode + 1, nil
		}
		got, err := strategyDriver{s}.ResolveExit(log, 0)
		if err != nil || got != 1 || gotPath != log {
			t.Errorf("ResolveExit = %d, %v (path %q); want 1 via the hook on %q", got, err, gotPath, log)
		}
	})

	t.Run("hook error is returned", func(t *testing.T) {
		s := fakeStrategy()
		want := errors.New("boom")
		s.ResolveExit = func(string, int) (int, error) { return 0, want }
		if _, err := (strategyDriver{s}).ResolveExit(log, 0); !errors.Is(err, want) {
			t.Errorf("err = %v, want %v", err, want)
		}
	})
}

func TestStrategyDriverResultText(t *testing.T) {
	// The result event and the last assistant message differ, so each reader
	// gives its own answer.
	log := writeFakeLog(t,
		"text\tm1\tearly prose",
		"result\tthe result\t0",
		"text\tm2\tlate prose",
	)

	cases := []struct {
		name   string
		reader driverkit.ResultReader
		want   string
	}{
		{"zero value reads result events", driverkit.ResultEvents, "the result\n"},
		{"LastMessage reads the final assistant message", driverkit.LastMessage, "late prose\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := fakeStrategy()
			s.Result = tc.reader
			got, err := strategyDriver{s}.ResultText(log)
			if err != nil || got != tc.want {
				t.Errorf("ResultText = %q, %v; want %q", got, err, tc.want)
			}
		})
	}

	t.Run("missing log is empty", func(t *testing.T) {
		got, err := strategyDriver{fakeStrategy()}.ResultText(filepath.Join(t.TempDir(), "absent.log"))
		if err != nil || got != "" {
			t.Errorf("ResultText = %q, %v; want empty, nil", got, err)
		}
	})
}

func TestStrategyDriverResultEventRoundTrip(t *testing.T) {
	d := strategyDriver{fakeStrategy()}
	ev, err := d.ResultEvent("all done")
	if err != nil {
		t.Fatalf("ResultEvent: %v", err)
	}
	path := filepath.Join(t.TempDir(), "stream.log")
	if err := os.WriteFile(path, ev, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := d.ResultText(path)
	if err != nil || got != "all done\n" {
		t.Errorf("ResultText = %q, %v; want %q", got, err, "all done\n")
	}
}

func TestStrategyDriverReadPasses(t *testing.T) {
	s := fakeStrategy()
	s.Totals = driverkit.SessionTotals
	s.WriteTool = "Write"
	log := writeFakeLog(t,
		"usage\tm1\tmodel-a\t10\t1",
		"tool\tw1\tWrite\tout.txt",
		"toolres\tw1\tok",
		"result\tdone\t7",
	)

	var seen int
	tallies, err := strategyDriver{s}.ReadPasses(log, "out.txt", func(driverkit.PassLine) { seen++ })
	if err != nil {
		t.Fatalf("ReadPasses: %v", err)
	}
	if seen != 4 {
		t.Errorf("visit saw %d lines, want 4", seen)
	}
	if len(tallies) != 1 {
		t.Fatalf("tallies = %+v, want one", tallies)
	}
	tl := tallies[0]
	if tl.APICalls != 1 || tl.Usage.OutputTokens != 7 || len(tl.Results) != 1 || tl.Results[0] != "done" {
		t.Errorf("tally = %+v, want 1 call, SessionTotals output 7, result done", tl)
	}
	if !tl.Wrote {
		t.Errorf("tally = %+v, want the WriteTool write to out.txt tracked", tl)
	}

	s.WriteTool = ""
	tallies, err = strategyDriver{s}.ReadPasses(log, "out.txt", nil)
	if err != nil || tallies[0].Wrote {
		t.Errorf("empty WriteTool: tallies %+v, err %v; want no write tracked", tallies, err)
	}
}

func TestStrategyDriverSessionFlags(t *testing.T) {
	got := strategyDriver{fakeStrategy()}.SessionFlags("initial", "o/r", "12", "/home/agent")
	if want := "initial|o/r|12|/home/agent"; got != want {
		t.Errorf("SessionFlags = %q, want %q", got, want)
	}
}
