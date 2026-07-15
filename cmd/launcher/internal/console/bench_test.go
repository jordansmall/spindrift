package console

import (
	"strings"
	"testing"
)

// largeTranscript returns a synthetic transcript of at least minBytes, one
// short line per entry — big enough that an accidental O(n) full
// re-split/re-join on every keystroke shows up in a benchmark (issue #722).
func largeTranscript(minBytes int) string {
	const line = "[implementor] line of transcript output for benchmarking purposes\n"
	var b strings.Builder
	b.Grow(minBytes + len(line))
	for b.Len() < minBytes {
		b.WriteString(line)
	}
	return b.String()
}

// BenchmarkUpdate_DrillInScroll_LargeTranscript exercises the keystroke path
// (DrillInScrollMsg -> clampDrillInOffset) against a 10MB+ transcript — the
// work Update does on every scroll keystroke while a drill-in is open
// (issue #722).
func BenchmarkUpdate_DrillInScroll_LargeTranscript(b *testing.B) {
	content := largeTranscript(10 << 20)
	m := Update(NewModel(), DrillInMsg{Number: "42", Rendered: content, Raw: content})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m = Update(m, DrillInScrollMsg{Delta: 1})
	}
}

// BenchmarkView_DrillInFullscreen_LargeTranscript exercises the render path
// (renderDrillIn) against a 10MB+ transcript on every View call — the other
// half of the keystroke re-render cycle (issue #722).
func BenchmarkView_DrillInFullscreen_LargeTranscript(b *testing.B) {
	content := largeTranscript(10 << 20)
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, DrillInMsg{Number: "42", Rendered: content, Raw: content})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = View(m)
	}
}

// BenchmarkUpdateView_DrillInScroll_LargeTranscript exercises the full
// keystroke re-render cycle — Update then View — the actual per-keystroke
// cost while a drill-in is open on a 10MB+ transcript (issue #722).
func BenchmarkUpdateView_DrillInScroll_LargeTranscript(b *testing.B) {
	content := largeTranscript(10 << 20)
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = Update(m, DrillInMsg{Number: "42", Rendered: content, Raw: content})

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m = Update(m, DrillInScrollMsg{Delta: 1})
		_ = View(m)
	}
}
