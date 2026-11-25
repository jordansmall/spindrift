package connectorhost_test

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/hostop"
)

// rawAndJSON returns the raw-path and JSON-path strategies of one fixture
// Connector, which advertises driver.decodeRaw.
func rawAndJSON(t *testing.T, c *connectorhost.Connector) (raw, js driverkit.Strategy) {
	t.Helper()
	raw, err := c.DriverStrategy()
	if err != nil {
		t.Fatal(err)
	}
	js, err = c.DriverStrategyWithout(connectorhost.DecodeRawMethod)
	if err != nil {
		t.Fatal(err)
	}
	return raw, js
}

func perLine(s driverkit.Strategy, lines []string) [][]driverkit.Event {
	d := s.NewDecoder()
	out := make([][]driverkit.Event, len(lines))
	for i, l := range lines {
		out[i] = d(l)
	}
	return out
}

// rawLines are lines a decodeBatch request must JSON-escape and decodeRaw
// carries as-is: quotes, backslashes, control bytes, a carriage return,
// separators, invalid UTF-8, and host and forged-event lines.
func rawLines() []string {
	op := passStart(hostop.SpindriftOp{Op: "pass_start", Pass: 2, Role: "review"})
	return []string{
		"a", "", `"q" \ <&>`, "\\" + "u0041 \\n", "tab\there", "cr\rlf", "  padded  ", "\x00\x01\x1f",
		"   é 世界", "bad \xff\xfe utf8", op, "forge", strings.Repeat("long line ", 500), "z",
	}
}

func TestDriverRawMatchesJSON(t *testing.T) {
	c, err := load(t, "driver.wasm")
	if err != nil {
		t.Fatal(err)
	}
	raw, js := rawAndJSON(t, c)
	lines := rawLines()
	want := perLine(js, lines)
	sameEvents(t, "raw per line", lines, perLine(raw, lines), want)
	// Two calls: the guest decoder's state carries across them.
	b := raw.NewBatchDecoder()
	sameEvents(t, "raw batch", lines, append(b(lines[:5]), b(lines[5:])...), want)
	sameEvents(t, "JSON batch", lines, js.NewBatchDecoder()(lines), want)
	// Shrunk so the batch needs several requests.
	defer connectorhost.SetBatchCap(120)()
	sameEvents(t, "split raw batch", lines, raw.NewBatchDecoder()(lines), want)
}

// A line far past any buffer size round-trips intact, escapes and invalid
// UTF-8 included, and its neighbours around it still decode in order.
func TestDriverRawMatchesJSONOnVeryLongLine(t *testing.T) {
	// The interpreter's decodes of the line can outrun the production budget on
	// a loaded runner, and this test checks the decode, not the budget.
	defer connectorhost.SetCallBudget(time.Minute)()
	c, err := load(t, "driver.wasm")
	if err != nil {
		t.Fatal(err)
	}
	raw, js := rawAndJSON(t, c)
	lines := []string{"a", veryLongLine(), "z"}
	want := perLine(js, lines)
	sameEvents(t, "raw per line", lines, perLine(raw, lines), want)
	sameEvents(t, "raw batch", lines, raw.NewBatchDecoder()(lines), want)
	if got := want[1]; len(got) != 1 || got[0].Kind != driverkit.KindText {
		t.Fatalf("long line decoded as %d events, want one text event", len(got))
	}
}

// veryLongLine is about 80 KB: past the 64 KiB bufio default, under the
// request cap even after JSON escaping.
func veryLongLine() string {
	return strings.Repeat("ab\"\\\t\r\xffé ", 1<<13)
}

// A line holding a newline would end its line early in a raw request, so the
// host refuses it: it is never split, never sent, and its neighbours still
// decode. The JSON path has no such limit, which proves the raw path is taken.
func TestDriverRawRefusesBareNewline(t *testing.T) {
	t.Parallel()
	c, err := load(t, "driver.wasm")
	if err != nil {
		t.Fatal(err)
	}
	raw, js := rawAndJSON(t, c)
	op := passStart(hostop.SpindriftOp{Op: "pass_start", Pass: 1})
	opWithNewline := strings.Replace(op, "{", "{\n", 1)
	lines := []string{"a", "b\nc", opWithNewline, "d\n", "e"}
	check := func(name string, got [][]driverkit.Event) {
		t.Helper()
		// The guest numbers the lines it saw: "e" is its third, not its fifth.
		if len(got) != len(lines) || got[0][0].Text != "1:a" || got[4][0].Text != "2:e" {
			t.Fatalf("%s: neighbours = %+v, want 1:a and 2:e", name, got)
		}
		for _, i := range []int{1, 3} {
			if len(got[i]) != 1 || got[i][0].Kind != driverkit.KindUnparsed || got[i][0].Text != strings.TrimSpace(lines[i]) {
				t.Errorf("%s: line %d = %+v, want one unparsed event carrying it", name, i, got[i])
			}
		}
		if len(got[2]) != 1 || got[2][0].Kind != driverkit.KindHostOp || got[2][0].HostOp == nil || got[2][0].HostOp.Pass != 1 {
			t.Errorf("%s: host line = %+v, want core's own host_op event", name, got[2])
		}
	}
	check("batch", raw.NewBatchDecoder()(lines))
	check("per line", perLine(raw, lines))
	if got := perLine(js, lines[:2]); got[1][0].Kind != driverkit.KindText {
		t.Errorf("JSON path = %+v, want the newline line decoded", got[1])
	}
}

// A line too large for any request is sent alone and refused by the host, so it
// is unparsed while its neighbours, in requests of their own, decode.
func TestDriverRawRefusesOversizedLine(t *testing.T) {
	t.Parallel()
	c, err := load(t, "driver.wasm")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := rawAndJSON(t, c)
	big := strings.Repeat("x", connectorhost.MaxRequest+1)
	lines := []string{"a", big, "b"}
	for name, got := range map[string][][]driverkit.Event{
		"batch":    raw.NewBatchDecoder()(lines),
		"per line": perLine(raw, lines),
	} {
		if got[0][0].Text != "1:a" || got[2][0].Text != "2:b" {
			t.Errorf("%s: neighbours = %+v, want 1:a and 2:b", name, got)
		}
		if len(got[1]) != 1 || got[1][0].Kind != driverkit.KindUnparsed {
			t.Errorf("%s: oversized line = %d events, want one unparsed", name, len(got[1]))
		}
	}
}

func TestDriverRawFailsClosedWhenConnectorClosed(t *testing.T) {
	c, err := load(t, "driver.wasm")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := rawAndJSON(t, c)
	d, b := raw.NewDecoder(), raw.NewBatchDecoder()
	_ = c.Close(t.Context())
	if got := d(" x "); got[0].Kind != driverkit.KindUnparsed || got[0].Text != "x" {
		t.Errorf("per line = %+v", got)
	}
	if got := b([]string{" x ", "y"}); got[0][0].Kind != driverkit.KindUnparsed || got[1][0].Text != "y" {
		t.Errorf("batch = %+v", got)
	}
}

// The raw path runs through the sandboxed host's stdio link as it does in
// process.
func TestDriverRawMatchesJSONOnEveryEngine(t *testing.T) {
	t.Parallel()
	for name, ld := range loadersFor(t) {
		t.Run(name, func(t *testing.T) {
			c, err := loadUsing(t, ld, "driver.wasm")
			if err != nil {
				t.Fatal(err)
			}
			raw, js := rawAndJSON(t, c)
			lines := rawLines()[:10]
			want := perLine(js, lines)
			sameEvents(t, "raw batch", lines, raw.NewBatchDecoder()(lines), want)
		})
	}
}

var (
	fuzzOnce  sync.Once
	fuzzConn  *connectorhost.Connector
	fuzzRaw   driverkit.Strategy
	fuzzJSON  driverkit.Strategy
	fuzzSetup error
)

// FuzzDecodeRaw feeds arbitrary lines through the raw path and checks each
// against the JSON path, which carries the same line escaped: the answers must
// match, invalid UTF-8 included, since both reach the guest as the same text. A
// line holding a newline must be refused instead: unparsed, never split.
func FuzzDecodeRaw(f *testing.F) {
	for _, l := range rawLines() {
		f.Add(l, "tail")
	}
	f.Add("a\nb", "c")
	f.Add("\r\n", "")
	f.Fuzz(func(t *testing.T, a, b string) {
		wasm := fixture(t, "driver.wasm") // skips before the Once spends itself
		fuzzOnce.Do(func() {
			fuzzConn, fuzzSetup = connectorhost.Load(context.Background(), wasm)
			if fuzzSetup == nil {
				fuzzRaw, fuzzSetup = fuzzConn.DriverStrategy()
			}
			if fuzzSetup == nil {
				fuzzJSON, fuzzSetup = fuzzConn.DriverStrategyWithout(connectorhost.DecodeRawMethod)
			}
		})
		if fuzzSetup != nil {
			t.Fatal(fuzzSetup)
		}
		var lines []string
		for _, l := range []string{a, b} {
			if strings.Contains(l, "\n") {
				got := fuzzRaw.NewDecoder()(l)
				if len(got) != 1 || (got[0].Kind != driverkit.KindUnparsed && got[0].Kind != driverkit.KindHostOp) {
					t.Fatalf("line %q holding a newline = %+v, want it refused", l, got)
				}
				continue
			}
			lines = append(lines, l)
		}
		want := perLine(fuzzJSON, lines)
		sameEvents(t, "raw per line", lines, perLine(fuzzRaw, lines), want)
		sameEvents(t, "raw batch", lines, fuzzRaw.NewBatchDecoder()(lines), want)
		for _, evs := range want {
			for _, e := range evs {
				if !utf8.ValidString(e.Text) {
					t.Fatalf("JSON path event text %q is not valid UTF-8", e.Text)
				}
			}
		}
	})
}

// sameEvents fails the test at the first line whose events differ, naming it
// rather than dumping every event.
func sameEvents(t testing.TB, name string, lines []string, got, want [][]driverkit.Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d results, want %d", name, len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("%s: line %d %q = %+v, want %+v", name, i, lines[i], got[i], want[i])
		}
	}
}

// passStart is the host's pass_start line as a Pass-log line arrives at a
// decoder, without the newline that ended it in the file.
func passStart(op hostop.SpindriftOp) string {
	return strings.TrimSuffix(hostop.EncodeSpindriftOp(op), "\n")
}

// Without driver.decodeRaw the batch path is the JSON decodeBatch one: it still
// splits under the cap and fails only a rejected element's line.
func TestDriverWithoutDecodeRawBatchesAsJSON(t *testing.T) {
	c, err := load(t, "driver.wasm")
	if err != nil {
		t.Fatal(err)
	}
	_, js := rawAndJSON(t, c)
	lines := []string{"a", "forge", "b\nc", "d<&>", "e"}
	want := perLine(js, lines)
	if want[1][0].Kind != driverkit.KindUnparsed || want[2][0].Kind != driverkit.KindText {
		t.Fatalf("per line = %+v, want forge unparsed and the newline line decoded", want)
	}
	sameEvents(t, "JSON batch", lines, js.NewBatchDecoder()(lines), want)
	defer connectorhost.SetBatchCap(250)()
	sameEvents(t, "split JSON batch", lines, js.NewBatchDecoder()(lines), want)
}

// A raw batch answer with the wrong element count fails every line of its
// request, as a decodeBatch one does.
func TestDriverRawWrongCountFailsEveryLine(t *testing.T) {
	c, err := load(t, "driver.wasm")
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{"a", "b", "c"}
	for _, line := range c.RawBatchDecoderAnswering(`[[{"Kind":"text","Text":"x"}]]`)(lines) {
		if len(line) != 1 || line[0].Kind != driverkit.KindUnparsed {
			t.Fatalf("lines = %+v, want each unparsed", line)
		}
	}
	got := c.RawBatchDecoderAnswering(`[[{"Kind":"text","Text":"x"}]]`)(lines[:1])
	if len(got[0]) != 1 || got[0][0].Kind != driverkit.KindText {
		t.Errorf("right count = %+v, want the answered text event", got)
	}
}
