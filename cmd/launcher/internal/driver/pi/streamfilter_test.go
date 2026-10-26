package pi_test

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
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

// testLine is the part of a pi event line the tests inspect.
type testLine struct {
	Type    string `json:"type"`
	Message struct {
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
			if !reflect.DeepEqual(decodeAll(got), decodeAll(log)) {
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
		// Every line the filter keeps is the fixture's own, in order.
		var want []string
		for _, line := range strings.Split(strings.TrimSuffix(log, "\n"), "\n") {
			if isDeltaLine(t, line) {
				continue
			}
			want = append(want, line)
		}
		if got != strings.Join(want, "\n")+"\n" {
			t.Errorf("%s: non-delta lines changed", name)
		}
	}
}

func TestStreamFilterKeepsToolExecutionUpdatesVerbatim(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	seen := 0
	for name, log := range corpusLogs(t) {
		var want []string
		for _, line := range strings.Split(strings.TrimSuffix(log, "\n"), "\n") {
			if parseLine(t, line).Type == "tool_execution_update" {
				want = append(want, line)
			}
		}
		var got []string
		for _, line := range strings.Split(strings.TrimSuffix(filterChunked(t, log, rng), "\n"), "\n") {
			if parseLine(t, line).Type == "tool_execution_update" {
				got = append(got, line)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: tool_execution_update lines changed", name)
		}
		seen += len(want)
	}
	if seen == 0 {
		t.Fatal("corpus holds no tool_execution_update line")
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
