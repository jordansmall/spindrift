package driverkit

import (
	"bytes"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/usage"
)

// fakeDecoder maps a raw line to the events it decodes to; an unlisted line
// decodes to none. Lines are the table keys, so a test reads as a log.
func fakeDecoder(table map[string][]Event) func() Decoder {
	return func() Decoder {
		return func(line string) []Event { return table[line] }
	}
}

// evLines builds a log from events: each event gets a unique line "L<i>".
func evLines(t *testing.T, evs ...Event) (string, func() Decoder) {
	t.Helper()
	table := map[string][]Event{}
	var lines []string
	for i, ev := range evs {
		line := "L" + string(rune('a'+i))
		table[line] = []Event{ev}
		lines = append(lines, line)
	}
	return writeClassifyLog(t, lines...), fakeDecoder(table)
}

var (
	rlExtras   = []Pattern{{Substr: "rate_limit", Reason: RateLimit}, {Substr: "overloaded", Reason: Overloaded}}
	termExtras = []Pattern{{Substr: "bad flag", Reason: UnsupportedFlag}}
	missingLog = func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.log") }
)

func TestClassifyProgressResetsLatch(t *testing.T) {
	for _, k := range []Kind{KindStart, KindText, KindReasoning, KindToolCall, KindToolResult} {
		path, nd := evLines(t, Event{Kind: KindError, Text: "rate_limit"}, Event{Kind: k})
		got, err := Classify(path, nd, rlExtras, termExtras)
		if err != nil {
			t.Fatal(err)
		}
		want := Classification{Class: Terminal, Reason: TaskFailed}
		if got != want {
			t.Errorf("progress %s: got %+v, want %+v", k, got, want)
		}
	}
}

func TestClassifyUsageIsNotProgress(t *testing.T) {
	path, nd := evLines(t, Event{Kind: KindError, Text: "rate_limit"}, Event{Kind: KindUsage}, Event{Kind: KindModel})
	got, _ := Classify(path, nd, rlExtras, termExtras)
	if got.Class != Transient || got.Reason != RateLimit {
		t.Errorf("got %+v, want Transient/RateLimit", got)
	}
}

func TestClassifyLastUnrecoveredErrorWins(t *testing.T) {
	path, nd := evLines(t,
		Event{Kind: KindError, Text: "rate_limit"},
		Event{Kind: KindText},
		Event{Kind: KindError, Text: "overloaded"},
		Event{Kind: KindError, Text: "unrelated"})
	got, _ := Classify(path, nd, rlExtras, termExtras)
	if got.Reason != Overloaded {
		t.Errorf("got %+v, want Overloaded", got)
	}
}

func TestClassifyUnparsedOnlyTerminalAndNonOverwriting(t *testing.T) {
	path, nd := evLines(t, Event{Kind: KindUnparsed, Text: "rate_limit hit"})
	if got, _ := Classify(path, nd, rlExtras, termExtras); got.Class != Terminal || got.Reason != TaskFailed {
		t.Errorf("transient marker in unparsed text: got %+v, want fallback", got)
	}
	path, nd = evLines(t, Event{Kind: KindUnparsed, Text: "bad flag"})
	if got, _ := Classify(path, nd, rlExtras, termExtras); got.Reason != UnsupportedFlag {
		t.Errorf("terminal marker in unparsed text: got %+v, want UnsupportedFlag", got)
	}
	path, nd = evLines(t, Event{Kind: KindError, Text: "rate_limit"}, Event{Kind: KindUnparsed, Text: "bad flag"})
	if got, _ := Classify(path, nd, rlExtras, termExtras); got.Reason != RateLimit {
		t.Errorf("unparsed overwrote the latched error: got %+v", got)
	}
	path, nd = evLines(t, Event{Kind: KindUnparsed, Text: "bad flag"}, Event{Kind: KindError, Text: "rate_limit"})
	if got, _ := Classify(path, nd, rlExtras, termExtras); got.Reason != RateLimit {
		t.Errorf("error did not overwrite the unparsed latch: got %+v", got)
	}
}

func TestClassifyResetAtOnlyOnRateLimit(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	later := at.Add(time.Hour)

	path, nd := evLines(t, Event{Kind: KindError, Text: "rate_limit", ResetAt: &at})
	if got, _ := Classify(path, nd, rlExtras, termExtras); got.ResetAt == nil || !got.ResetAt.Equal(at) {
		t.Errorf("RateLimit ResetAt = %v, want %v", got.ResetAt, at)
	}

	path, nd = evLines(t, Event{Kind: KindError, Text: "overloaded", ResetAt: &at})
	if got, _ := Classify(path, nd, rlExtras, termExtras); got.ResetAt != nil {
		t.Errorf("Overloaded kept ResetAt %v", got.ResetAt)
	}

	// An unmatched later error leaves the latch, so the earlier error's reset stays.
	path, nd = evLines(t, Event{Kind: KindError, Text: "rate_limit", ResetAt: &at}, Event{Kind: KindError, Text: "noise", ResetAt: &later})
	if got, _ := Classify(path, nd, rlExtras, termExtras); got.ResetAt == nil || !got.ResetAt.Equal(at) {
		t.Errorf("unmatched error moved ResetAt to %v, want %v", got.ResetAt, at)
	}

	// A matching later error without its own reset clears the earlier one.
	path, nd = evLines(t, Event{Kind: KindError, Text: "rate_limit", ResetAt: &at}, Event{Kind: KindError, Text: "rate_limit"})
	if got, _ := Classify(path, nd, rlExtras, termExtras); got.ResetAt != nil {
		t.Errorf("stale ResetAt %v survived a newer latch", got.ResetAt)
	}

	// Progress drops the latch and its reset together.
	path, nd = evLines(t, Event{Kind: KindError, Text: "rate_limit", ResetAt: &at}, Event{Kind: KindText}, Event{Kind: KindError, Text: "rate_limit"})
	if got, _ := Classify(path, nd, rlExtras, termExtras); got.ResetAt != nil {
		t.Errorf("reset survived progress: %v", got.ResetAt)
	}
}

func TestClassifyMissingLog(t *testing.T) {
	got, err := Classify(missingLog(t), fakeDecoder(nil), rlExtras, termExtras)
	if err != nil || got != (Classification{Class: Terminal, Reason: TaskFailed}) {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestClassifyUsesFreshDecoderPerScan(t *testing.T) {
	made := 0
	nd := func() Decoder {
		made++
		return func(string) []Event { return nil }
	}
	path := writeClassifyLog(t, "x")
	_, _ = Classify(path, nd, nil, nil)
	_, _ = Classify(path, nd, nil, nil)
	if made != 2 {
		t.Errorf("newDecoder called %d times, want 2", made)
	}
}

func TestLastMessageText(t *testing.T) {
	txt := func(id, s string) Event { return Event{Kind: KindText, MessageID: id, Text: s} }
	tests := []struct {
		name string
		evs  []Event
		want string
	}{
		{"last message only", []Event{txt("m1", "a"), txt("m2", "b"), txt("m2", "c")}, "b\nc\n"},
		{"textless final message", []Event{txt("m1", "a"), {Kind: KindStart, MessageID: "m2"}}, ""},
		{"idless text starts new message", []Event{txt("m1", "a"), txt("", "final")}, "final\n"},
		{"idless text resets prior idless text", []Event{txt("", "x"), txt("", "y")}, "y\n"},
		{"idless non-text keeps message", []Event{txt("m1", "a"), {Kind: KindToolCall}}, "a\n"},
		{"empty text dropped", []Event{txt("m1", ""), txt("m1", "a")}, "a\n"},
		{"reasoning is not result text", []Event{txt("m1", "a"), {Kind: KindReasoning, MessageID: "m1", Text: "think"}}, "a\n"},
		{"result replaces buffer", []Event{txt("m1", "a"), {Kind: KindResult, Text: "done"}}, "done"},
		{"text after result appends", []Event{{Kind: KindResult, Text: "done"}, txt("", "z")}, "z\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path, nd := evLines(t, tc.evs...)
			got, err := LastMessageText(path, nd)
			if err != nil || got != tc.want {
				t.Errorf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	if got, err := LastMessageText(missingLog(t), fakeDecoder(nil)); got != "" || err != nil {
		t.Errorf("missing log: %q, %v", got, err)
	}
}

func TestRenderTranscript(t *testing.T) {
	path, nd := evLines(t,
		Event{Kind: KindText, Text: "\n  top line\nsecond  "},
		Event{Kind: KindText, Text: "   "},
		Event{Kind: KindReasoning, Text: "hidden"},
		Event{Kind: KindText, Role: "sub", Text: "from sub"},
		Event{Kind: KindToolCall})
	got, err := RenderTranscript(path, nd, "")
	want := "[implementor] top line\nsecond\n[sub] from sub"
	if err != nil || got != want {
		t.Errorf("got %q, %v; want %q", got, err, want)
	}
	got, _ = RenderTranscript(path, nd, ReviewerRole)
	if !strings.HasPrefix(got, "[reviewer] top line") || !strings.HasSuffix(got, "[sub] from sub") {
		t.Errorf("topLevelRole not applied: %q", got)
	}
	if got, err := RenderTranscript(missingLog(t), fakeDecoder(nil), ""); got != "" || err != nil {
		t.Errorf("missing log: %q, %v", got, err)
	}
	empty, nd := evLines(t, Event{Kind: KindToolCall})
	if got, _ := RenderTranscript(empty, nd, ""); got != "" {
		t.Errorf("no text: %q", got)
	}
}

func usageEv(id, model string, ts int64, tok Tokens) Event {
	return Event{Kind: KindUsage, MessageID: id, Model: model, Timestamp: ts, Tokens: tok}
}

func TestExtractUsageSumsEveryEventAndSpans(t *testing.T) {
	path, nd := evLines(t,
		Event{Kind: KindStart, Timestamp: 1000},
		Event{Kind: KindStart, Timestamp: 1500},
		usageEv("m1", "a", 2000, Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite5m: 4, CostUSD: 0.5}),
		usageEv("m1", "a", 3000, Tokens{Input: 10, Output: 20, CacheRead: 30, CacheWrite5m: 40, CacheWrite1h: 5, CostUSD: 0.25}),
	)
	r, err := ExtractUsage(path, nd, byModelOf(nd))
	if err != nil {
		t.Fatal(err)
	}
	u := r.Totals
	if !r.Found || u.InputTokens != 11 || u.OutputTokens != 22 || u.CacheReadInputTokens != 33 ||
		u.CacheCreationInputTokens != 49 || u.TotalCostUSD != 0.75 || u.NumTurns != 2 || u.DurationMs != 2000 {
		t.Errorf("totals = %+v found=%v", u, r.Found)
	}
	if !r.HasEventSpan || r.EarliestEventMs != 1000 || r.LatestEventMs != 3000 {
		t.Errorf("span = %v %d %d", r.HasEventSpan, r.EarliestEventMs, r.LatestEventMs)
	}
	// Per-model dedups the repeated message ID though the totals did not.
	want := []usage.ModelUsage{{Model: "a", UncachedInputTokens: 1, OutputTokens: 2, CacheReadInputTokens: 3, CacheWrite5mTokens: 4}}
	if !reflect.DeepEqual(r.SummedByModel, want) {
		t.Errorf("SummedByModel = %+v, want %+v", r.SummedByModel, want)
	}
	if r.SummedByAgent != nil {
		t.Errorf("SummedByAgent = %v, want nil", r.SummedByAgent)
	}
}

func TestExtractUsageNoStartLeavesSpanUnset(t *testing.T) {
	path, nd := evLines(t, usageEv("", "", 5000, Tokens{Input: 1}))
	r, _ := ExtractUsage(path, nd, byModelOf(nd))
	if !r.Found || r.Totals.DurationMs != 0 || r.HasEventSpan || r.EarliestEventMs != 0 || r.LatestEventMs != 0 {
		t.Errorf("report = %+v", r)
	}
}

func TestExtractUsageNotFound(t *testing.T) {
	path, nd := evLines(t, Event{Kind: KindStart, Timestamp: 1}, Event{Kind: KindText})
	if r, err := ExtractUsage(path, nd, byModelOf(nd)); err != nil || r.Found {
		t.Errorf("no usage events: %+v, %v", r, err)
	}
	if r, err := ExtractUsage(missingLog(t), fakeDecoder(nil), byModelOf(fakeDecoder(nil))); err != nil || r.Found {
		t.Errorf("missing log: %+v, %v", r, err)
	}
}

func TestExtractUsageByModelErrorDegrades(t *testing.T) {
	path, nd := evLines(t, usageEv("m", "a", 1, Tokens{Input: 7}))
	r, err := ExtractUsage(path, nd, func(string) ([]usage.ModelUsage, error) { return nil, io.ErrUnexpectedEOF })
	if err != nil || !r.Found || r.Totals.InputTokens != 7 || r.SummedByModel != nil {
		t.Errorf("report = %+v, err = %v", r, err)
	}
}

func TestUsageByModel(t *testing.T) {
	path, nd := evLines(t,
		usageEv("m1", "zeta", 1, Tokens{Input: 1, Output: 1}),
		usageEv("m1", "zeta", 2, Tokens{Input: 100}),
		usageEv("", "", 3, Tokens{Input: 2, CacheWrite1h: 6}),
		usageEv("", "", 4, Tokens{Input: 3}),
		usageEv("m2", "alpha", 5, Tokens{Output: 9}),
		Event{Kind: KindModel, Model: "ignored"},
	)
	got, err := UsageByModel(path, nd)
	want := []usage.ModelUsage{
		{Model: "alpha", OutputTokens: 9},
		{Model: usage.UnknownModel, UncachedInputTokens: 5, CacheWrite1hTokens: 6},
		{Model: "zeta", UncachedInputTokens: 1, OutputTokens: 1},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, %v; want %+v", got, err, want)
	}
	if got, err := UsageByModel(missingLog(t), fakeDecoder(nil)); got != nil || err != nil {
		t.Errorf("missing log: %v, %v", got, err)
	}
}

type modelCall struct{ model, role string }

func TestHeartbeatWriterChunkSplitStable(t *testing.T) {
	table := map[string][]Event{
		"t1": {{Kind: KindText, Text: "first. second"}},
		"m1": {{Kind: KindModel, Model: "a"}},
		"m2": {{Kind: KindModel, Model: "a"}},
		"m3": {{Kind: KindModel, Model: "b", Role: "sub"}},
		"t2": {{Kind: KindText, Text: "  "}},
		"x":  {{Kind: KindToolCall}},
	}
	stream := "t1\nm1\nx\nm2\nt2\nm3\nunknown\n"
	wantOut := "#7 \xc2\xb7 first.\n"
	wantCalls := []modelCall{{"a", "top"}, {"b", "sub"}}

	for _, chunk := range []int{1, 2, 3, len(stream)} {
		var raw, out bytes.Buffer
		var calls []modelCall
		w := NewHeartbeatWriter(&raw, "7", &out, fakeDecoder(table), "top").OnModel(func(m, r string) {
			calls = append(calls, modelCall{m, r})
		})
		for i := 0; i < len(stream); i += chunk {
			end := min(i+chunk, len(stream))
			if n, err := w.Write([]byte(stream[i:end])); err != nil || n != end-i {
				t.Fatalf("chunk %d: Write = %d, %v", chunk, n, err)
			}
		}
		if raw.String() != stream {
			t.Errorf("chunk %d: raw not forwarded verbatim: %q", chunk, raw.String())
		}
		if out.String() != wantOut {
			t.Errorf("chunk %d: out = %q, want %q", chunk, out.String(), wantOut)
		}
		if !reflect.DeepEqual(calls, wantCalls) {
			t.Errorf("chunk %d: calls = %v, want %v", chunk, calls, wantCalls)
		}
	}
}

func TestHeartbeatWriterNoListenerAndEmptyTopLevelRole(t *testing.T) {
	table := map[string][]Event{"m": {{Kind: KindModel, Model: "a"}}}
	var raw, out bytes.Buffer
	w := NewHeartbeatWriter(&raw, "1", &out, fakeDecoder(table), "")
	if _, err := w.Write([]byte("m\n")); err != nil {
		t.Fatal(err)
	}
	var got []modelCall
	w.OnModel(func(m, r string) { got = append(got, modelCall{m, r}) })
	_, _ = w.Write([]byte("m\n"))
	if want := []modelCall{{"a", ""}}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestHeartbeatWriterRawErrorSkipsDecode(t *testing.T) {
	var out bytes.Buffer
	table := map[string][]Event{"t": {{Kind: KindText, Text: "hi"}}}
	w := NewHeartbeatWriter(failWriter{}, "1", &out, fakeDecoder(table), "")
	if _, err := w.Write([]byte("t\n")); err == nil || out.Len() != 0 {
		t.Errorf("err = %v, out = %q", err, out.String())
	}
}

func TestHeartbeatWriterCallbackRunsOutsideLock(t *testing.T) {
	table := map[string][]Event{"m": {{Kind: KindModel, Model: "a"}}}
	var raw, out bytes.Buffer
	var w *HeartbeatWriter
	var wg sync.WaitGroup
	w = NewHeartbeatWriter(&raw, "1", &out, fakeDecoder(table), "").OnModel(func(string, string) {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = w.Write([]byte("m\n")) }()
		wg.Wait()
	})
	if _, err := w.Write([]byte("m\n")); err != nil {
		t.Fatal(err)
	}
}

func byModelOf(nd func() Decoder) func(string) ([]usage.ModelUsage, error) {
	return func(path string) ([]usage.ModelUsage, error) { return UsageByModel(path, nd) }
}
