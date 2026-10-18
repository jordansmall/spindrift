package main

import (
	"bytes"
	"encoding/json"
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

func TestRenderStats_SanitizesRecordDerivedLabels(t *testing.T) {
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
		if want := "evil agent FAKE 9 9 9 9 9 9"; !strings.HasPrefix(rows[1], want) {
			t.Errorf("agent row = %q, want prefix %q", rows[1], want)
		}
	})

	t.Run("role table", func(t *testing.T) {
		rows := statsTableFrom(t, out, "ROLE")
		if len(rows) != 2 {
			t.Fatalf("role table = %d lines, want header and one row:\n%s", len(rows), strings.Join(rows, "\n"))
		}
		if want := "implement X FAKE"; !strings.HasPrefix(rows[1], want) {
			t.Errorf("role row = %q, want prefix %q", rows[1], want)
		}
	})
}

func TestRenderStatsGroups_SanitizesGroupHeader(t *testing.T) {
	records := statsHostileRecords()
	for _, tc := range []struct {
		name string
		by   statsBy
		want string
	}{
		{"model", statsBy{dim: statsByModel}, "model = m X Y"},
		{"knob", statsBy{dim: statsByKnob, arg: "TIMEOUT"}, "knob:TIMEOUT = v A B"},
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
