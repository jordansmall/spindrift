package driverkit

import (
	"encoding/json"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/usage"
)

func passStartEv() Event {
	return Event{Kind: KindHostOp, HostOp: &hostop.SpindriftOp{Op: hostop.OpPassStart}}
}

func writeCall(id, tool, target, content string) Event {
	in, _ := json.Marshal(map[string]string{"content": content})
	return Event{Kind: KindToolCall, ToolID: id, ToolName: tool, ToolTarget: target, ToolInput: in}
}

func readPasses(t *testing.T, opts PassOptions, track string, visit func(PassLine), evs ...[]Event) []PassTally {
	t.Helper()
	table := map[string][]Event{}
	var lines []string
	for i, e := range evs {
		line := "L" + string(rune('a'+i))
		table[line] = e
		lines = append(lines, line)
	}
	opts.NewDecoder = fakeDecoder(table)
	got, err := ReadPasses(writeClassifyLog(t, lines...), opts, track, visit)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestReadPassesSegmentsAndLineFlags(t *testing.T) {
	var seen []PassLine
	got := readPasses(t, PassOptions{Totals: SessionTotals}, "", func(l PassLine) { seen = append(seen, l) },
		[]Event{{Kind: KindUnparsed, Text: "stderr"}},
		[]Event{passStartEv()},
		[]Event{{Kind: KindError, Text: "boom"}},
		[]Event{{Kind: KindError, Text: "boom", Timestamp: 7}, {Kind: KindText, Timestamp: 9}},
		nil,
		[]Event{passStartEv()},
		[]Event{{Kind: KindText}},
	)
	if len(got) != 3 {
		t.Fatalf("segments = %d, want 3", len(got))
	}
	want := []struct {
		event bool
		ts    int64
		op    bool
	}{{false, 0, false}, {true, 0, true}, {false, 0, false}, {true, 7, false}, {true, 0, true}, {true, 0, false}}
	// The line decoding to no events ("Le") is non-blank, so it still
	// visits, as an eventless line.
	if len(seen) != 7 {
		t.Fatalf("visited %d lines, want 7", len(seen))
	}
	idx := 0
	for _, l := range seen {
		if l.Text == "Le" {
			if l.Event {
				t.Errorf("eventless line = %+v", l)
			}
			continue
		}
		w := want[idx]
		idx++
		if l.Event != w.event || l.Timestamp != w.ts || (l.HostOp != nil) != w.op {
			t.Errorf("line %q = %+v, want %+v", l.Text, l, w)
		}
	}
}

func TestReadPassesSkipsBlankLines(t *testing.T) {
	path := writeClassifyLog(t, "", "  ", "x")
	var n int
	got, err := ReadPasses(path, PassOptions{NewDecoder: fakeDecoder(nil)}, "", func(l PassLine) {
		n++
		if l.Text != "x" {
			t.Errorf("visited %q", l.Text)
		}
	})
	if err != nil || n != 1 || len(got) != 1 {
		t.Fatalf("got=%v err=%v visited=%d", got, err, n)
	}
}

func TestReadPassesMissingLogIsOneEmptyTally(t *testing.T) {
	got, err := ReadPasses("/nonexistent/pass.log", PassOptions{NewDecoder: fakeDecoder(nil)}, "", nil)
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], PassTally{}) {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestReadPassesSessionTotalsSumPerResult(t *testing.T) {
	res := func(text string, ms int64) []Event {
		return []Event{{
			Kind: KindResult, Text: text, DurationMs: ms, DurationAPIMs: ms / 2, NumTurns: 2,
			Tokens: Tokens{Input: 1, Output: 2, CacheRead: 3, CacheWrite5m: 4, CacheWrite1h: 5, CostUSD: 0.5},
			Models: []string{"b", "a"},
		}}
	}
	got := readPasses(t, PassOptions{Totals: SessionTotals}, "", nil,
		[]Event{{Kind: KindUsage, MessageID: "m1", Model: "ignored"}},
		[]Event{{Kind: KindUsage, MessageID: "m1"}},
		[]Event{{Kind: KindUsage, MessageID: "m2"}, {Kind: KindUsage}, {Kind: KindUsage}},
		res("one", 100),
		res("two", 40),
		[]Event{{Kind: KindResult, Text: "skipped", NoTotals: true, NumTurns: 9}},
		[]Event{passStartEv()},
		res("next", 10),
	)
	if len(got) != 2 {
		t.Fatalf("segments = %d", len(got))
	}
	first := got[0]
	if first.APICalls != 4 {
		t.Errorf("APICalls = %d, want 4 (m1 deduped, empty ids never)", first.APICalls)
	}
	wantUsage := usage.Usage{
		InputTokens: 2, OutputTokens: 4, CacheReadInputTokens: 6, CacheCreationInputTokens: 18,
		TotalCostUSD: 1, DurationMs: 140, DurationApiMs: 70, NumTurns: 4,
	}
	if first.Usage != wantUsage {
		t.Errorf("Usage = %+v, want %+v", first.Usage, wantUsage)
	}
	if !reflect.DeepEqual(first.Results, []string{"one", "two"}) {
		t.Errorf("Results = %q", first.Results)
	}
	if !reflect.DeepEqual(first.Models, []string{"a", "b"}) {
		t.Errorf("Models = %q (usage events must not feed them)", first.Models)
	}
	if got[1].Usage.NumTurns != 2 || got[1].APICalls != 0 {
		t.Errorf("second segment = %+v", got[1])
	}
}

func TestReadPassesPerCallTotals(t *testing.T) {
	call := func(id, model string, ts int64) []Event {
		return []Event{{Kind: KindUsage, MessageID: id, Model: model, Timestamp: ts,
			Tokens: Tokens{Input: 1, Output: 1, CacheWrite5m: 1, CacheWrite1h: 1, CostUSD: 0.25}}}
	}
	text := func(id, s string) []Event { return []Event{{Kind: KindText, MessageID: id, Text: s}} }
	got := readPasses(t, PassOptions{Totals: PerCallTotals}, "", nil,
		[]Event{{Kind: KindStart, Timestamp: 1000}},
		text("m1", "first"),
		call("m1", "x", 1500),
		[]Event{{Kind: KindStart, Timestamp: 2000}},
		text("m2", "final"),
		call("m2", "", 2500),
		call("m2", "y", 3000),
		[]Event{passStartEv()},
		text("m3", "new pass"),
		[]Event{passStartEv()},
		text("m4", "no usage"),
		[]Event{passStartEv()},
		[]Event{{Kind: KindResult, Text: "replaced"}},
	)
	if len(got) != 4 {
		t.Fatalf("segments = %d", len(got))
	}
	a := got[0]
	if a.APICalls != 2 || a.Usage.NumTurns != 3 {
		t.Errorf("APICalls=%d NumTurns=%d, want 2 and 3 (calls dedup, turns do not)", a.APICalls, a.Usage.NumTurns)
	}
	if a.Usage.InputTokens != 3 || a.Usage.CacheCreationInputTokens != 6 || a.Usage.TotalCostUSD != 0.75 {
		t.Errorf("Usage = %+v", a.Usage)
	}
	if a.Usage.DurationMs != 2000 || a.Usage.DurationApiMs != 0 {
		t.Errorf("durations = %d/%d, want 2000/0", a.Usage.DurationMs, a.Usage.DurationApiMs)
	}
	if !reflect.DeepEqual(a.Models, []string{"x", "y"}) {
		t.Errorf("Models = %q", a.Models)
	}
	if !reflect.DeepEqual(a.Results, []string{"final\n"}) {
		t.Errorf("Results = %q, want only the last message", a.Results)
	}
	if len(got[1].Results) != 0 {
		t.Errorf("segment with text but no usage/result has Results %q", got[1].Results)
	}
	if len(got[2].Results) != 0 {
		t.Errorf("segment without usage has Results %q", got[2].Results)
	}
	if !reflect.DeepEqual(got[3].Results, []string{"replaced"}) {
		t.Errorf("result event Results = %q", got[3].Results)
	}
}

func TestReadPassesPerCallNoStartLeavesDurationUnset(t *testing.T) {
	got := readPasses(t, PassOptions{Totals: PerCallTotals}, "", nil,
		[]Event{{Kind: KindUsage, Timestamp: 5000}},
	)
	if got[0].Usage.DurationMs != 0 {
		t.Errorf("DurationMs = %d, want 0 without a KindStart", got[0].Usage.DurationMs)
	}
}

func TestReadPassesPerCallStartWithoutUsageLeavesDurationUnset(t *testing.T) {
	got := readPasses(t, PassOptions{Totals: PerCallTotals}, "", nil,
		[]Event{passStartEv()},
		[]Event{{Kind: KindStart, Timestamp: 1760000000000}},
	)
	if got[1].Usage.DurationMs != 0 {
		t.Errorf("DurationMs = %d, want 0 when the segment saw a start but no usage", got[1].Usage.DurationMs)
	}
}

func TestReadPassesPerCallResultResetsAtSegmentBoundary(t *testing.T) {
	got := readPasses(t, PassOptions{Totals: PerCallTotals}, "", nil,
		[]Event{{Kind: KindText, MessageID: "m1", Text: "early"}, {Kind: KindUsage, MessageID: "m1"}},
		[]Event{passStartEv()},
		[]Event{{Kind: KindUsage, MessageID: "m2"}},
	)
	if !reflect.DeepEqual(got[0].Results, []string{"early\n"}) || !reflect.DeepEqual(got[1].Results, []string{""}) {
		t.Errorf("Results = %q / %q, want early and an empty result", got[0].Results, got[1].Results)
	}
}

func TestReadPassesWriteTracking(t *testing.T) {
	const path = "/tmp/dispositions.json"
	opts := PassOptions{Totals: SessionTotals, WriteTool: "Write"}
	ok := func(id string) []Event { return []Event{{Kind: KindToolResult, ToolID: id}} }
	bad := func(id string) []Event { return []Event{{Kind: KindToolResult, ToolID: id, IsError: true}} }

	tests := []struct {
		name      string
		track     string
		lines     [][]Event
		seg       int
		want      string
		wantWrote bool
	}{
		{"success", path, [][]Event{{writeCall("t1", "Write", path, "body")}, ok("t1")}, 0, "body", true},
		{"error result", path, [][]Event{{writeCall("t1", "Write", path, "body")}, bad("t1")}, 0, "", false},
		{"unanswered", path, [][]Event{{writeCall("t1", "Write", path, "body")}}, 0, "", false},
		{"last success wins", path, [][]Event{
			{writeCall("t1", "Write", path, "one")}, ok("t1"),
			{writeCall("t2", "Write", path, "two")}, ok("t2"),
		}, 0, "two", true},
		{"error after success keeps success", path, [][]Event{
			{writeCall("t1", "Write", path, "one")}, ok("t1"),
			{writeCall("t2", "Write", path, "two")}, bad("t2"),
		}, 0, "one", true},
		{"wrong tool", path, [][]Event{{writeCall("t1", "Edit", path, "body")}, ok("t1")}, 0, "", false},
		{"wrong path", path, [][]Event{{writeCall("t1", "Write", "/other", "body")}, ok("t1")}, 0, "", false},
		{"undecodable input", path, [][]Event{
			{{Kind: KindToolCall, ToolID: "t1", ToolName: "Write", ToolTarget: path, ToolInput: json.RawMessage(`{"content":`)}}, ok("t1"),
		}, 0, "", false},
		{"no tracked path", "", [][]Event{{writeCall("t1", "Write", "", "body")}, ok("t1")}, 0, "", false},
		{"pending dropped at pass boundary", path, [][]Event{
			{writeCall("t1", "Write", path, "body")}, {passStartEv()}, ok("t1"),
		}, 1, "", false},
		{"pending belongs to its own segment", path, [][]Event{
			{writeCall("t1", "Write", path, "body")}, ok("t1"), {passStartEv()},
		}, 0, "body", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readPasses(t, opts, tt.track, nil, tt.lines...)
			if g := got[tt.seg]; g.Written != tt.want || g.Wrote != tt.wantWrote {
				t.Errorf("Written=%q Wrote=%v, want %q %v", g.Written, g.Wrote, tt.want, tt.wantWrote)
			}
		})
	}

	got := readPasses(t, PassOptions{Totals: SessionTotals}, path, nil, []Event{writeCall("t1", "Write", path, "b")}, ok("t1"))
	if got[0].Wrote {
		t.Error("an empty WriteTool tracked a write")
	}
}
