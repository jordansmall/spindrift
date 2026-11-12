package driverkit

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/logscan"
)

// countingStrategy wraps fakeStrategy's decoder so every NewDecoder and every
// decoded line is counted; results carry a session id and cumulative cost so a
// base rebases them.
func countingStrategy() (s Strategy, decoders, lines *int) {
	decoders, lines = new(int), new(int)
	s = fakeStrategy()
	s.Totals = SessionTotals
	s.NewDecoder = func() Decoder {
		*decoders++
		inner := tabDecoder()
		return func(line string) []Event {
			*lines++
			evs := inner(line)
			for i := range evs {
				if evs[i].Kind == KindResult {
					evs[i].SessionID = "s1"
					evs[i].Tokens.CostUSD = float64(evs[i].Tokens.Output)
				}
			}
			return evs
		}
	}
	return s, decoders, lines
}

var passLogLines = []string{
	"text\tm1\thello",
	"usage\tm1\tmodel-a\t10\t5",
	"",
	"tool\tt1\tWrite\tout.md",
	"toolres\tt1\tok",
	"result\tdone\t3",
	"err\tboom",
	"result\tmore\t7",
}

func TestPassLogDecodesEachLineOnce(t *testing.T) {
	s, decoders, lines := countingStrategy()
	d := New(s)
	path := writeFakeLog(t, passLogLines...)
	base := SessionBaselines{"s1": {CostUSD: 1}}
	opts := RenderOptions{TopLevelRole: "reviewer"}

	pl, err := d.Decode(path)
	if err != nil {
		t.Fatal(err)
	}
	gotResult := pl.ResultText()
	gotReport, gotAfter := pl.ExtractUsageAfter(base)
	gotTranscript := pl.RenderTranscript(opts)
	var gotVisited []PassLine
	gotTallies := pl.ReadPasses("out.md", base, func(l PassLine) { gotVisited = append(gotVisited, l) })
	gotBases := pl.SessionBaselinesAfter(base)
	gotClass, gotClassErr := pl.ClassifyTransient()
	if *decoders != 1 || *lines != len(passLogLines) {
		t.Errorf("decoders=%d lines=%d, want 1 and %d", *decoders, *lines, len(passLogLines))
	}

	wantResult, _ := d.ResultText(path)
	wantReport, wantAfter, _ := d.ExtractUsageAfter(path, base)
	wantTranscript, _ := d.RenderTranscript(path, opts)
	var wantVisited []PassLine
	wantTallies, _ := d.ReadPasses(path, "out.md", base, func(l PassLine) { wantVisited = append(wantVisited, l) })
	wantBases, _ := d.SessionBaselinesAfter(path, base)
	wantClass, wantClassErr := d.ClassifyTransient(path)

	for name, c := range map[string][2]any{
		"ResultText":            {gotResult, wantResult},
		"ExtractUsageAfter":     {gotReport, wantReport},
		"baselines after":       {gotAfter, wantAfter},
		"RenderTranscript":      {gotTranscript, wantTranscript},
		"ReadPasses":            {gotTallies, wantTallies},
		"ReadPasses visit":      {gotVisited, wantVisited},
		"SessionBaselinesAfter": {gotBases, wantBases},
		"ClassifyTransient":     {gotClass, wantClass},
		"ClassifyTransient err": {gotClassErr, wantClassErr},
	} {
		if !reflect.DeepEqual(c[0], c[1]) {
			t.Errorf("%s = %+v, want %+v", name, c[0], c[1])
		}
	}
}

func TestExtractUsageAfterDecodesOnce(t *testing.T) {
	s, decoders, lines := countingStrategy()
	path := writeFakeLog(t, passLogLines...)
	if _, _, err := New(s).ExtractUsageAfter(path, SessionBaselines{"s1": {CostUSD: 1}}); err != nil {
		t.Fatal(err)
	}
	if *decoders != 1 || *lines != len(passLogLines) {
		t.Errorf("decoders=%d lines=%d, want 1 and %d", *decoders, *lines, len(passLogLines))
	}
}

func TestRebasedLogLeavesTheSharedDecodeUntouched(t *testing.T) {
	s, _, _ := countingStrategy()
	path := writeFakeLog(t, passLogLines...)
	log, err := DecodeLog(path, s.NewDecoder)
	if err != nil {
		t.Fatal(err)
	}
	base := SessionBaselines{"s1": {CostUSD: 1}}
	read := func() []Event {
		var evs []Event
		log.Rebased(base).Each(func(ev Event) { evs = append(evs, ev) })
		return evs
	}
	first, second := read(), read()
	if !reflect.DeepEqual(first, second) {
		t.Errorf("second rebased read = %+v, want %+v", second, first)
	}
	var raw []Event
	log.Each(func(ev Event) {
		if ev.Kind == KindResult {
			raw = append(raw, ev)
		}
	})
	if len(raw) != 2 || raw[0].Tokens.CostUSD != 3 || raw[1].Tokens.CostUSD != 7 {
		t.Errorf("shared decode results = %+v, want raw costs 3 and 7", raw)
	}
	if first[5].Tokens.CostUSD != 2 {
		t.Errorf("rebased first result cost = %v, want 2", first[5].Tokens.CostUSD)
	}
}

func TestPassLogReadersNeverReopenTheLog(t *testing.T) {
	s, _, _ := countingStrategy()
	s.TransientPatterns = rlExtras
	d := New(s)
	path := writeFakeLog(t, passLogLines...)
	base := SessionBaselines{"s1": {CostUSD: 1}}
	opts := RenderOptions{TopLevelRole: "reviewer"}

	pl, err := d.Decode(path)
	if err != nil {
		t.Fatal(err)
	}
	read := func() map[string]any {
		var visited []PassLine
		tallies := pl.ReadPasses("out.md", base, func(l PassLine) { visited = append(visited, l) })
		report, after := pl.ExtractUsageAfter(base)
		class, classErr := pl.ClassifyTransient()
		return map[string]any{
			"ResultText": pl.ResultText(), "ExtractUsageAfter": report, "baselines": after,
			"RenderTranscript": pl.RenderTranscript(opts), "ReadPasses": tallies, "visit": visited,
			"ClassifyTransient": class, "ClassifyTransient err": classErr,
		}
	}
	want := read()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if got := read(); !reflect.DeepEqual(got, want) {
		t.Errorf("readers after the log is gone = %+v, want %+v", got, want)
	}
}

func TestPassLogClassifyMatchesClassifyOnOversizedLine(t *testing.T) {
	huge := "err\trate_limit" + strings.Repeat("x", 5<<20)
	path := writeFakeLog(t, "text\tm1\thi", huge, "text\tm2\tdone")
	for name, opts := range map[string]ClassifyOptions{
		"skip":              {},
		"chunk":             {Oversized: logscan.ChunkOversized},
		"chunk first match": {Oversized: logscan.ChunkOversized, FirstMatch: true},
	} {
		s := fakeStrategy()
		s.TransientPatterns, s.TerminalPatterns, s.Classify = rlExtras, termExtras, opts
		got, err := mustDecode(t, New(s), path).ClassifyTransient()
		if err != nil {
			t.Fatal(err)
		}
		want, _ := Classify(path, s.NewDecoder, rlExtras, termExtras, opts)
		if got != want {
			t.Errorf("%s: PassLog classified %+v, path-based Classify %+v", name, got, want)
		}
	}
}

func mustDecode(t *testing.T, d Driver, path string) PassLog {
	t.Helper()
	pl, err := d.Decode(path)
	if err != nil {
		t.Fatal(err)
	}
	return pl
}
