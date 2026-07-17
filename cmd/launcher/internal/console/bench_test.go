package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
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
// (issue #722). Recorded when the DrillInState.Lines cache landed:
// 1.59ms/op, 2.5MB/op, 1 alloc/op before, 51.5ns/op, 0B/op, 0 allocs/op
// after (issue #1016) — alloc counts are the invariant; ns/op and B/op
// vary by machine and Go version.
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
// half of the keystroke re-render cycle (issue #722). Recorded at Offset 0,
// Height 24 when windowLines landed: 3.88ms/op, 21.0MB/op, 7 allocs/op
// before it capped the join to the viewport, 1.6µs/op, 3.39KB/op, 5
// allocs/op after (issue #1016) — alloc counts are the invariant; ns/op
// and B/op vary by machine and Go version.
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

// largeHeartbeatLog returns a synthetic pass log of at least minBytes, valid
// input for driver.Driver's heartbeat parser (repeated tool_use events, one
// terminal result event) — big enough that the ReadFile+reparse a
// HeartbeatCache miss pays shows up in a benchmark (issue #731).
func largeHeartbeatLog(minBytes int) string {
	const line = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}` + "\n"
	var b strings.Builder
	b.Grow(minBytes + len(line))
	for b.Len() < minBytes {
		b.WriteString(line)
	}
	b.WriteString(`{"type":"result","num_turns":42,"total_cost_usd":0.01,"duration_ms":5000}` + "\n")
	return b.String()
}

// newHeartbeatBenchFixture writes a 10MB+ pass log to disk and returns the
// pwd/driver pair RunningHeartbeat needs to replay it — shared setup for the
// cache-hit/cold-read benchmark pair below.
func newHeartbeatBenchFixture(b *testing.B) (pwd string, drv driver.Driver) {
	b.Helper()
	dir := b.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-9.log"), []byte(largeHeartbeatLog(10<<20)), 0o644); err != nil {
		b.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		b.Fatalf("driver.New: %v", err)
	}
	return dir, drv
}

// BenchmarkHeartbeatCache_ColdRead_LargeLog exercises RunningHeartbeat's
// uncached path — a fresh HeartbeatCache every iteration, so every call pays
// the full ReadFile+reparse — against a 10MB+ pass log (issue #731).
func BenchmarkHeartbeatCache_ColdRead_LargeLog(b *testing.B) {
	pwd, drv := newHeartbeatBenchFixture(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewHeartbeatCache().RunningHeartbeat(drv, pwd, "9")
	}
}

// BenchmarkHeartbeatCache_CacheHit_LargeLog exercises RunningHeartbeat's
// cached path — one warm-up call, then repeat calls against the same
// unchanged 10MB+ pass log — the case syncQueue hits on every tea.Msg
// between actual log growth (issue #731).
func BenchmarkHeartbeatCache_CacheHit_LargeLog(b *testing.B) {
	pwd, drv := newHeartbeatBenchFixture(b)
	cache := NewHeartbeatCache()
	cache.RunningHeartbeat(drv, pwd, "9")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = cache.RunningHeartbeat(drv, pwd, "9")
	}
}

// BenchmarkTryLaunch_EmptyQueue exercises the background poll tick's idle
// case (tea.go pollTickMsg, every interval regardless of queue state)
// against an empty Queue — the drain-goroutine-plus-RunContinuous-pass
// waste #754 closes. Post-fix this is a Queue.Empty() check and return, no
// goroutine spawn, no allocation.
func BenchmarkTryLaunch_EmptyQueue(b *testing.B) {
	launch := &Launcher{Queue: NewQueue()}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		launch.tryLaunch(nil, "")
	}
}
