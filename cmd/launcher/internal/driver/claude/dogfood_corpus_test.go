package claude_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/claude"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// dogfoodLines returns each non-empty line of every dogfood pass log, keyed by
// case name.
func dogfoodLines(t testing.TB) map[string][][]byte {
	t.Helper()
	out := map[string][][]byte{}
	for _, p := range passLogPaths(t, "dogfood") {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var lines [][]byte
		for _, l := range bytes.Split(data, []byte("\n")) {
			if len(l) > 0 {
				lines = append(lines, l)
			}
		}
		out[filepath.Base(filepath.Dir(p))] = lines
	}
	return out
}

func meanLen(lines [][]byte) float64 {
	n := 0
	for _, l := range lines {
		n += len(l)
	}
	return float64(n) / float64(len(lines))
}

// TestDogfoodCorpusShape pins the corpus to dogfood shadow summaries (issue
// #6849): a 2-8 KB mean line with a tail of single lines from ~100 KB to
// ~800 KB.
func TestDogfoodCorpusShape(t *testing.T) {
	const kb = 1000
	cases := dogfoodLines(t)
	if len(cases) < 2 {
		t.Fatalf("want at least two passes, got %d", len(cases))
	}
	var all [][]byte
	largest := ""
	maxLine := 0
	for name, lines := range cases {
		all = append(all, lines...)
		if largest == "" || len(lines) > len(cases[largest]) {
			largest = name
		}
		for _, l := range lines {
			maxLine = max(maxLine, len(l))
		}
		if last := lines[len(lines)-1]; !bytes.Contains(last, []byte(`"type":"result"`)) {
			t.Errorf("%s: last line is not a result event", name)
		}
	}
	if n := len(cases[largest]); n < 300 {
		t.Errorf("largest pass %s has %d lines, want several hundred", largest, n)
	}
	// Each mean includes the tail: the dogfood summaries' per-line mean does too.
	byLabel := map[string][][]byte{"corpus": all}
	for name, lines := range cases {
		byLabel["pass "+name] = lines
	}
	for label, lines := range byLabel {
		if m := meanLen(lines); m < 2*kb || m > 8*kb {
			t.Errorf("%s mean line is %.0f B, want 2-8 KB", label, m)
		}
	}
	if maxLine < 700*kb || maxLine > 900*kb {
		t.Errorf("max line is %d B, want ~800 KB", maxLine)
	}
	big := 0
	for _, l := range all {
		if len(l) >= 100*kb {
			big++
		}
	}
	if big < 4 {
		t.Errorf("%d lines of 100 KB or more, want a tail of at least 4", big)
	}
}

// Padding must not push a line outside the claude grammar.
func TestDogfoodCorpusDecodes(t *testing.T) {
	for name, lines := range dogfoodLines(t) {
		decode := claude.Strategy.NewDecoder()
		for i, l := range lines {
			evs := decode(string(l))
			if len(evs) == 0 {
				t.Fatalf("%s line %d (%d B) decoded to no events", name, i+1, len(l))
			}
			for _, ev := range evs {
				if ev.Kind == driverkit.KindUnparsed {
					t.Fatalf("%s line %d (%d B) is unparsed", name, i+1, len(l))
				}
			}
		}
	}
}
