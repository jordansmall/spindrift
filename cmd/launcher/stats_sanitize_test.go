package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/dispatchrecord"
)

const (
	statsHostileAgent = "evil\tagent\nFAKE\t9\t9\t9\t9\t9\t9\x1b[2J"
	statsHostileRole  = "imp\x1b[31mlement\tX\nFAKE"
	statsHostileModel = "m\x1b[31m\tX\rY"
	statsHostileKnob  = "v\x1b[2J\tA\nB"
)

// statsAssertPlainText fails if out holds a control byte other than the
// newlines ending its lines.
func statsAssertPlainText(t *testing.T, out string) {
	t.Helper()
	for i := 0; i < len(out); i++ {
		if c := out[i]; c != '\n' && (c < 0x20 || c == 0x7f) {
			t.Errorf("output carries control byte %#x at %d:\n%q", c, i, out)
			return
		}
	}
}

// statsTableFrom returns the lines of the table whose header starts with
// header, failing the test when out has no such table.
func statsTableFrom(t *testing.T, out, header string) []string {
	t.Helper()
	_, rest, ok := strings.Cut(out, "\n"+header)
	if !ok {
		t.Fatalf("no %s table in output:\n%q", header, out)
	}
	table, _, _ := strings.Cut(header+rest, "\n\n")
	return strings.Split(strings.TrimRight(table, "\n"), "\n")
}

func statsHostileRecords() []dispatchrecord.Record {
	return []dispatchrecord.Record{{
		Kind: "work", DispatchKey: "1",
		Knobs: map[string]string{"TIMEOUT": statsHostileKnob},
		Passes: []dispatchrecord.Pass{{
			Role: statsHostileRole, Models: []string{statsHostileModel}, USD: 1,
			Agents: []dispatchrecord.PassAgent{{Agent: statsHostileAgent, APICalls: 1}},
		}},
	}}
}

func TestRenderStats_QuotesRecordDerivedLabels(t *testing.T) {
	var buf bytes.Buffer
	if err := renderStats(&buf, statsHostileRecords()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	statsAssertPlainText(t, out)

	t.Run("agent table", func(t *testing.T) {
		rows := statsTableFrom(t, out, "AGENT")
		if len(rows) != 2 {
			t.Fatalf("agent table = %d lines, want header and one row:\n%s", len(rows), strings.Join(rows, "\n"))
		}
		if want := strconv.Quote(statsHostileAgent) + "  1"; !strings.HasPrefix(rows[1], want) {
			t.Errorf("agent row = %q, want prefix %q", rows[1], want)
		}
	})

	t.Run("role table", func(t *testing.T) {
		rows := statsTableFrom(t, out, "ROLE")
		if len(rows) != 2 {
			t.Fatalf("role table = %d lines, want header and one row:\n%s", len(rows), strings.Join(rows, "\n"))
		}
		if want := strconv.Quote(statsHostileRole) + "  1"; !strings.HasPrefix(rows[1], want) {
			t.Errorf("role row = %q, want prefix %q", rows[1], want)
		}
	})
}

func TestRenderStatsGroups_QuotesGroupHeader(t *testing.T) {
	records := statsHostileRecords()
	for _, tc := range []struct {
		name string
		by   statsBy
		want string
	}{
		{"model", statsBy{dim: statsByModel}, "model = " + strconv.Quote(statsHostileModel)},
		{"knob", statsBy{dim: statsByKnob, arg: "TIMEOUT"}, "knob:TIMEOUT = " + strconv.Quote(statsHostileKnob)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dims := []statsBy{tc.by}
			var buf bytes.Buffer
			if err := renderStatsGroups(&buf, groupStats(records, dims), dims); err != nil {
				t.Fatal(err)
			}
			statsAssertPlainText(t, buf.String())
			if header, _, _ := strings.Cut(buf.String(), "\n"); header != tc.want {
				t.Errorf("group header = %q, want %q", header, tc.want)
			}
		})
	}
}

// statsAgentRecord builds a one-Pass Record with Dispatch key key whose model
// and agent are both name.
func statsAgentRecord(key, name string) dispatchrecord.Record {
	return dispatchrecord.Record{
		Kind: "work", DispatchKey: key,
		Passes: []dispatchrecord.Pass{{
			Role: "implement", Models: []string{name}, USD: 1,
			Agents: []dispatchrecord.PassAgent{{Agent: name, APICalls: 1}},
		}},
	}
}

func TestRenderStats_ControlOnlyAgentIsVisible(t *testing.T) {
	var buf bytes.Buffer
	if err := renderStats(&buf, []dispatchrecord.Record{statsAgentRecord("1", "\x1b[2J")}); err != nil {
		t.Fatal(err)
	}
	statsAssertPlainText(t, buf.String())
	rows := statsTableFrom(t, buf.String(), "AGENT")
	if want := `"\x1b[2J"  1`; len(rows) != 2 || !strings.HasPrefix(rows[1], want) {
		t.Errorf("agent rows = %q, want a row starting %q", rows, want)
	}
}

func TestRenderStats_DistinctKeysStayDistinct(t *testing.T) {
	records := []dispatchrecord.Record{statsAgentRecord("1", "a\tb"), statsAgentRecord("2", "a b")}

	t.Run("agent table", func(t *testing.T) {
		var buf bytes.Buffer
		if err := renderStats(&buf, records); err != nil {
			t.Fatal(err)
		}
		statsAssertPlainText(t, buf.String())
		rows := statsTableFrom(t, buf.String(), "AGENT")
		if len(rows) != 3 {
			t.Fatalf("agent table = %d lines, want header and two rows:\n%s", len(rows), strings.Join(rows, "\n"))
		}
		for _, key := range []string{`"a\tb"`, "a b"} {
			found := false
			for _, row := range rows[1:] {
				if rest, ok := strings.CutPrefix(row, key+" "); ok && strings.Fields(rest)[0] == "1" {
					found = true
				}
			}
			if !found {
				t.Errorf("table lacks a row keyed %q with 1 Pass:\n%s", key, strings.Join(rows, "\n"))
			}
		}
	})

	t.Run("group headers", func(t *testing.T) {
		dims := []statsBy{{dim: statsByModel}}
		var buf bytes.Buffer
		if err := renderStatsGroups(&buf, groupStats(records, dims), dims); err != nil {
			t.Fatal(err)
		}
		statsAssertPlainText(t, buf.String())
		for _, want := range []string{"model = \"a\\tb\"\n", "model = a b\n"} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("missing header %q:\n%s", want, buf.String())
			}
		}
	})
}

func TestStatsKey(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"ordinary", "implement", "implement"},
		{"spaces", "a b", "a b"},
		{"printable non-ASCII", "café", "café"},
		{"empty", "", `""`},
		{"control only", "\x1b[2J", `"\x1b[2J"`},
		{"tab", "a\tb", `"a\tb"`},
		{"newline", "a\nb", `"a\nb"`},
		{"leading quote", `"x`, `"\"x"`},
		{"embedded quote", `a"b`, `"a\"b"`},
		{"backslash", `a\b`, `"a\\b"`},
		{"invalid UTF-8", "a\xffb", `"a\xffb"`},
		{"C1 control", "a\u009bb", `"a\u009bb"`},
		{"trailing space", "a ", `"a "`},
		{"leading space", " a", `" a"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := statsKeyText(tc.in); got != tc.want {
				t.Errorf("statsKeyText(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestWarnEach_SanitizesErrorText(t *testing.T) {
	var buf bytes.Buffer
	warnEach(&buf, "p", errors.Join(
		errors.New("ok one"),
		errors.New("bad\x1b[2J\x1b]0;t\x07\nline"),
	))
	out := buf.String()
	statsAssertPlainText(t, out)
	if !strings.Contains(out, "warning: p: ok one\n") {
		t.Errorf("ordinary message changed:\n%q", out)
	}
	if !strings.Contains(out, "warning: p: bad line\n") {
		t.Errorf("hostile message not cleaned to %q:\n%q", "bad line", out)
	}
	if n := strings.Count(out, "\n"); n != 2 {
		t.Errorf("output = %d lines, want one per joined error:\n%q", n, out)
	}
}

func TestCheckoutEvents_SanitizesWarning(t *testing.T) {
	root := filepath.Join(t.TempDir(), "r\x1b[2J\x1b]0;t\x07")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "init")
	gitDir, err := gitOutput(root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	// A directory where the file belongs makes ReadEvents fail with an error
	// that quotes the hostile path.
	if err := os.Mkdir(filepath.Join(gitDir, "spindrift-daemon.events"), 0o755); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	checkoutEvents(root, &buf)
	out := buf.String()
	statsAssertPlainText(t, out)
	if !strings.Contains(out, "warning: not reading daemon events") {
		t.Errorf("no events warning:\n%q", out)
	}
}

func TestEncodeStatsJSON_KeepsRawLabels(t *testing.T) {
	var buf bytes.Buffer
	if err := encodeStatsJSON(json.NewEncoder(&buf), statsHostileRecords(), nil, nil); err != nil {
		t.Fatal(err)
	}
	var got dispatchrecord.Record
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Passes) != 1 || len(got.Passes[0].Agents) != 1 || len(got.Passes[0].Models) != 1 {
		t.Fatalf("decoded record lost its pass, agent or model: %+v", got)
	}
	if a := got.Passes[0].Agents[0].Agent; a != statsHostileAgent {
		t.Errorf("json agent = %q, want raw %q", a, statsHostileAgent)
	}
	if m := got.Passes[0].Models[0]; m != statsHostileModel {
		t.Errorf("json model = %q, want raw %q", m, statsHostileModel)
	}
}
