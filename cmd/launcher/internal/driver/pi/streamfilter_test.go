package pi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/pi"
)

// filterChunked feeds in to a fresh filter in random-size chunks, then flushes.
func filterChunked(t *testing.T, in string, rng *rand.Rand) string {
	t.Helper()
	var out bytes.Buffer
	f := pi.NewStreamFilter(&out)
	for rest := []byte(in); len(rest) > 0; {
		n := min(1+rng.Intn(200), len(rest))
		if got, err := f.Write(rest[:n]); err != nil || got != n {
			t.Fatalf("Write = %d, %v; want %d, nil", got, err, n)
		}
		rest = rest[n:]
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func corpusLogs(t *testing.T) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "corpus", "*", "pass.jsonl"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no corpus fixtures: %v", err)
	}
	logs := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		logs[filepath.Base(filepath.Dir(p))] = string(b)
	}
	return logs
}

func decodeAll(log string) []driverkit.Event {
	dec := pi.NewDecoder()
	var evs []driverkit.Event
	for _, l := range strings.Split(log, "\n") {
		evs = append(evs, dec(l)...)
	}
	return evs
}

// sameEvents compares two decoded streams: the main loop's events in order, the
// subagent children as a set. The end emits every child by index, where the
// dropped updates emitted them as they arrived, so interleaved children of
// parallel calls come out in another order.
func sameEvents(a, b []driverkit.Event) bool {
	split := func(evs []driverkit.Event) (main, kids []string) {
		for _, ev := range evs {
			if ev.InSubagent {
				kids = append(kids, fmt.Sprintf("%+v", ev))
			} else {
				main = append(main, fmt.Sprintf("%+v", ev))
			}
		}
		slices.Sort(kids)
		return main, kids
	}
	am, ak := split(a)
	bm, bk := split(b)
	return reflect.DeepEqual(am, bm) && reflect.DeepEqual(ak, bk)
}

// testLine is the part of a pi event line the tests inspect.
type testLine struct {
	Type       string `json:"type"`
	ToolName   string `json:"toolName"`
	ToolCallID string `json:"toolCallId"`
	Message    struct {
		Role string `json:"role"`
	} `json:"message"`
	AME struct {
		Type         string `json:"type"`
		ContentIndex int    `json:"contentIndex"`
		Delta        string `json:"delta"`
	} `json:"assistantMessageEvent"`
}

func parseLine(t *testing.T, line string) testLine {
	t.Helper()
	var l testLine
	if err := json.Unmarshal([]byte(line), &l); err != nil {
		t.Fatalf("not JSON: %q: %v", line, err)
	}
	return l
}

// isSubagentUpdate reports whether line is a subagent tool_execution_update.
func isSubagentUpdate(t *testing.T, line string) bool {
	t.Helper()
	l := parseLine(t, line)
	return l.Type == "tool_execution_update" && l.ToolName == "subagent"
}

// isOtherToolUpdate reports whether line is a tool_execution_update of a tool
// other than the subagent.
func isOtherToolUpdate(t *testing.T, line string) bool {
	t.Helper()
	l := parseLine(t, line)
	return l.Type == "tool_execution_update" && l.ToolName != "subagent"
}

func isDeltaLine(t *testing.T, line string) bool {
	t.Helper()
	l := parseLine(t, line)
	return pi.IsDelta(l.Type, l.AME.Type)
}

func TestStreamFilterPreservesDecodedEvents(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for name, log := range corpusLogs(t) {
		for range 20 {
			got := filterChunked(t, log, rng)
			if !sameEvents(decodeAll(got), decodeAll(log)) {
				t.Fatalf("%s: filtered log decodes differently", name)
			}
		}
	}
}

func TestStreamFilterDropsDeltasOfFinishedMessages(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for name, log := range corpusLogs(t) {
		got := filterChunked(t, log, rng)
		for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
			if isDeltaLine(t, line) {
				t.Errorf("%s: delta survived a finished message: %.120s", name, line)
			}
		}
		// Every other line the filter keeps is the fixture's own, in order.
		// Subagent updates are left out of both sides: whether one survives
		// depends on how its call ends, which the update-hold tests cover.
		keep := func(out string) []string {
			var kept []string
			for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
				if !isDeltaLine(t, line) && !isSubagentUpdate(t, line) {
					kept = append(kept, line)
				}
			}
			return kept
		}
		if !reflect.DeepEqual(keep(got), keep(log)) {
			t.Errorf("%s: non-delta lines changed", name)
		}
	}
}

func TestStreamFilterKeepsNonSubagentToolExecutionUpdatesVerbatim(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	seen := 0
	for name, log := range corpusLogs(t) {
		var want []string
		for _, line := range strings.Split(strings.TrimSuffix(log, "\n"), "\n") {
			if isOtherToolUpdate(t, line) {
				want = append(want, line)
			}
		}
		var got []string
		for _, line := range strings.Split(strings.TrimSuffix(filterChunked(t, log, rng), "\n"), "\n") {
			if isOtherToolUpdate(t, line) {
				got = append(got, line)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: non-subagent tool_execution_update lines changed", name)
		}
		seen += len(want)
	}
	if seen == 0 {
		t.Fatal("corpus holds no non-subagent tool_execution_update line")
	}
}

type openBlock struct{ typ, text string }

// heldAtEnd reads the deltas of the message still open at the end of lines,
// grouped per content block in order of first appearance.
func heldAtEnd(t *testing.T, lines []string) (order []int, blocks map[int]*openBlock) {
	t.Helper()
	blocks = map[int]*openBlock{}
	for _, line := range lines {
		l := parseLine(t, line)
		if l.Message.Role == "assistant" && (l.Type == "message_end" || l.Type == "message_start") {
			order, blocks = nil, map[int]*openBlock{}
		}
		if !pi.IsDelta(l.Type, l.AME.Type) {
			continue
		}
		b, ok := blocks[l.AME.ContentIndex]
		if !ok {
			b = &openBlock{typ: l.AME.Type}
			blocks[l.AME.ContentIndex] = b
			order = append(order, l.AME.ContentIndex)
		}
		b.text += l.AME.Delta
	}
	return order, blocks
}

func TestStreamFilterFlushCoalescesAbortedMessage(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	log := corpusLogs(t)["reviewer-verdict"]
	lines := strings.Split(strings.TrimSuffix(log, "\n"), "\n")

	// Cut right after the last delta, so its message never reaches message_end.
	cut := -1
	for i, line := range lines {
		if isDeltaLine(t, line) {
			cut = i
		}
	}
	truncated := lines[:cut+1]
	order, blocks := heldAtEnd(t, truncated)
	if len(order) == 0 {
		t.Fatal("truncation left no open deltas")
	}

	got := filterChunked(t, strings.Join(truncated, "\n")+"\n", rng)
	outLines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if !strings.HasSuffix(got, "\n") {
		t.Error("output must end in a newline")
	}
	var deltas []int
	for _, line := range outLines {
		l := parseLine(t, line)
		if !pi.IsDelta(l.Type, l.AME.Type) {
			continue
		}
		b := blocks[l.AME.ContentIndex]
		if b == nil || l.AME.Type != b.typ || l.AME.Delta != b.text {
			t.Errorf("coalesced delta %q (%s) at %d does not match input %+v", l.AME.Delta, l.AME.Type, l.AME.ContentIndex, b)
		}
		deltas = append(deltas, l.AME.ContentIndex)
	}
	if !reflect.DeepEqual(deltas, order) {
		t.Errorf("coalesced block order = %v, want %v", deltas, order)
	}
	for _, ev := range decodeAll(got) {
		if ev.Kind == driverkit.KindUnparsed {
			t.Errorf("filtered output holds an unparsed line: %q", ev.Text)
		}
	}
}

func TestStreamFilterCoalescesInterleavedBlocksKeepingEnvelope(t *testing.T) {
	in := strings.Join([]string{
		`{"type":"message_start","message":{"role":"assistant"}}`,
		`{"type":"message_update","usage":{"input":1},"assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"a"}}`,
		`{"type":"message_update","usage":{"input":2},"assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"X"}}`,
		`{"type":"message_update","usage":{"input":3},"assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"b"}}`,
		`{"type":"message_update","usage":{"input":4},"assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"Y"}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	f := pi.NewStreamFilter(&out)
	if _, err := f.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`{"type":"message_start","message":{"role":"assistant"}}`,
		`{"assistantMessageEvent":{"contentIndex":0,"delta":"ab","type":"thinking_delta"},"type":"message_update","usage":{"input":3}}`,
		`{"assistantMessageEvent":{"contentIndex":1,"delta":"XY","type":"text_delta"},"type":"message_update","usage":{"input":4}}`,
	}, "\n") + "\n"
	if out.String() != want {
		t.Errorf("output =\n%s\nwant\n%s", out.String(), want)
	}
}

// The coalesced text follows the *_end marker that already passed through; the
// decoder ignores message_update, so the order is harmless.
func TestStreamFilterFlushAfterBlockEndMarkerKeepsTextAndDecodes(t *testing.T) {
	in := strings.Join([]string{
		`{"type":"message_start","message":{"role":"assistant"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hel"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"lo"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_end","contentIndex":0}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_start","contentIndex":1}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	f := pi.NewStreamFilter(&out)
	if _, err := f.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`{"type":"message_start","message":{"role":"assistant"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_end","contentIndex":0}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_start","contentIndex":1}}`,
		`{"assistantMessageEvent":{"contentIndex":0,"delta":"Hello","type":"text_delta"},"type":"message_update"}`,
	}, "\n") + "\n"
	if out.String() != want {
		t.Errorf("output =\n%s\nwant\n%s", out.String(), want)
	}
	for _, ev := range decodeAll(out.String()) {
		if ev.Kind == driverkit.KindUnparsed {
			t.Errorf("filtered output holds an unparsed line: %q", ev.Text)
		}
	}
}

func TestStreamFilterFlushesHeldDeltasBeforeNextAssistantMessage(t *testing.T) {
	in := strings.Join([]string{
		`{"type":"message_start","message":{"role":"assistant"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"lost"}}`,
		`{"type":"message_start","message":{"role":"assistant"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"gone"}}`,
		`{"type":"message_end","message":{"role":"assistant"}}`,
	}, "\n") + "\n"
	var out bytes.Buffer
	f := pi.NewStreamFilter(&out)
	if _, err := f.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`{"type":"message_start","message":{"role":"assistant"}}`,
		`{"assistantMessageEvent":{"contentIndex":0,"delta":"lost","type":"text_delta"},"type":"message_update"}`,
		`{"type":"message_start","message":{"role":"assistant"}}`,
		`{"type":"message_end","message":{"role":"assistant"}}`,
	}, "\n") + "\n"
	if out.String() != want {
		t.Errorf("output =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestStreamFilterPartialLineAndUnparsedPassThrough(t *testing.T) {
	var out bytes.Buffer
	f := pi.NewStreamFilter(&out)
	for _, chunk := range []string{"not json\n", `{"type":"agent_`, `end"}` + "\n", `{"type":"tru`} {
		if _, err := f.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if want := "not json\n{\"type\":\"agent_end\"}\n"; out.String() != want {
		t.Errorf("before Flush = %q, want %q", out.String(), want)
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	if want := "not json\n{\"type\":\"agent_end\"}\n{\"type\":\"tru"; out.String() != want {
		t.Errorf("after Flush = %q, want %q", out.String(), want)
	}
}

func TestStrategyDeclaresStreamFilter(t *testing.T) {
	var out bytes.Buffer
	f := driverkit.New(pi.Strategy).NewStreamFilter(&out)
	line := `{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"x"}}` + "\n"
	if _, err := f.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Errorf("delta written through before Flush: %q", out.String())
	}
}

// subagentUpdateFor is a subagent update of call id whose one child message
// says text, so the latest held line is told apart from the ones it replaced.
func subagentUpdateFor(id, text string) string {
	return subagentUpdateOf(id, subagentResult("scout", scoutUser, childAssistant(1500, `[{"type":"text","text":"`+text+`"}]`)))
}

func writeLines(t *testing.T, w io.Writer, lines ...string) {
	t.Helper()
	if _, err := w.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatal(err)
	}
}

func countSubagentUpdates(t *testing.T, out string) (n int) {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line != "" && isSubagentUpdate(t, line) {
			n++
		}
	}
	return n
}

func TestStreamFilterHoldsNoSubagentUpdateWhileRunningOrAfterEnd(t *testing.T) {
	var out bytes.Buffer
	f := pi.NewStreamFilter(&out)
	for _, u := range subagentUpdates() {
		writeLines(t, f, u)
		if n := countSubagentUpdates(t, out.String()); n != 0 {
			t.Fatalf("%d updates written through while the call runs", n)
		}
	}
	end := subagentEnd("parallel", subagentResult("scout", scoutUser, scoutCall, scoutResult, scoutSays))
	writeLines(t, f, end)
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != end+"\n" {
		t.Errorf("output = %q, want only the end line", got)
	}
}

func TestStreamFilterFlushKeepsLatestSubagentUpdateInFirstSeenOrder(t *testing.T) {
	var out bytes.Buffer
	f := pi.NewStreamFilter(&out)
	other := `{"type":"tool_execution_update","toolCallId":"b1","toolName":"bash","partialResult":{}}`
	latestA := subagentUpdateFor("a", "a-2")
	latestB := subagentUpdateFor("b", "b-3")
	writeLines(t, f,
		subagentUpdateFor("a", "a-1"),
		subagentUpdateFor("b", "b-1"),
		other,
		latestA,
		subagentUpdateFor("b", "b-2"),
		latestB,
	)
	if got := out.String(); got != other+"\n" {
		t.Fatalf("before Flush = %q, want only the non-subagent update", got)
	}
	for range 2 {
		if err := f.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	if want := other + "\n" + latestA + "\n" + latestB + "\n"; out.String() != want {
		t.Errorf("after Flush =\n%s\nwant\n%s", out.String(), want)
	}
}

// An end drops only its own call's held update, and a reused toolCallId starts
// a fresh hold.
func TestStreamFilterEndDropsOnlyItsOwnCallsUpdate(t *testing.T) {
	var out bytes.Buffer
	f := pi.NewStreamFilter(&out)
	end := subagentEnd("single", subagentResult("scout", scoutUser, scoutSays))
	keep := subagentUpdateFor("b", "b-1")
	again := subagentUpdateFor("s1", "again")
	writeLines(t, f, subagentUpdateFor("s1", "s1-1"), keep, end, again)
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	if want := end + "\n" + keep + "\n" + again + "\n"; out.String() != want {
		t.Errorf("output =\n%s\nwant\n%s", out.String(), want)
	}
}

// subagentErrorEnd is the end of a call whose execute threw: an error result
// with no details.results, so it cannot restate the children.
const subagentErrorEnd = `{"type":"tool_execution_end","toolCallId":"s1","toolName":"subagent","result":{"content":[{"type":"text","text":"Subagent was aborted"}],"details":{}},"isError":true}`

// The held update of such a call is written ahead of the end.
func TestStreamFilterWritesHeldUpdateBeforeErrorEnd(t *testing.T) {
	var out bytes.Buffer
	f := pi.NewStreamFilter(&out)
	latest := subagentUpdateFor("s1", "s1-2")
	writeLines(t, f, subagentUpdateFor("s1", "s1-1"), latest, subagentErrorEnd)
	if err := f.Flush(); err != nil {
		t.Fatal(err)
	}
	if want := latest + "\n" + subagentErrorEnd + "\n"; out.String() != want {
		t.Errorf("output =\n%s\nwant\n%s", out.String(), want)
	}
}

// The filtered log yields the usage of the unfiltered stream, for a finished
// subagent call and for one killed before its end.
func TestStreamFilterPreservesSubagentUsage(t *testing.T) {
	head := toolTurn(1000, "s1", "subagent", `{"agent":"scout","task":"look"}`)
	completed := append([]string{head}, subagentUpdates()...)
	completed = append(completed,
		subagentEnd("parallel", subagentResult("scout", scoutUser, scoutCall, scoutResult, scoutSays), subagentResult("scout", secondSays)),
		textEnd(2000, "parent done"),
	)
	// A subagent that throws ends in an error result with no details.results.
	failed := append(append([]string{head}, subagentUpdates()...), subagentErrorEnd)
	cases := []struct {
		name     string
		lines    []string
		wantHeld int
	}{
		{"completed", completed, 0},
		{"killed", append([]string{head}, subagentUpdates()...), 1},
		{"failed", failed, 1},
	}
	rng := rand.New(rand.NewSource(5))
	for _, c := range cases {
		want, err := newDriver().ExtractUsage(pi.WriteLog(t, c.lines...))
		if err != nil {
			t.Fatal(err)
		}
		filtered := filterChunked(t, strings.Join(c.lines, "\n")+"\n", rng)
		got, err := newDriver().ExtractUsage(pi.WriteLog(t, strings.Split(strings.TrimSuffix(filtered, "\n"), "\n")...))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: usage from the filtered log = %+v, want %+v", c.name, got, want)
		}
		if n := countSubagentUpdates(t, filtered); n != c.wantHeld {
			t.Errorf("%s: filtered log holds %d subagent updates, want %d", c.name, n, c.wantHeld)
		}
	}
}
