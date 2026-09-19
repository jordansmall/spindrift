package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// largeTranscript returns a synthetic transcript of at least minBytes, one
// short line per entry, big enough that an accidental O(n) full re-split or
// re-join on every keystroke shows up in a benchmark (issue #722).
func largeTranscript(minBytes int) string {
	const line = "[implementor] line of transcript output for benchmarking purposes\n"
	var b strings.Builder
	b.Grow(minBytes + len(line))
	for b.Len() < minBytes {
		b.WriteString(line)
	}
	return b.String()
}

// openSidebarOnTranscript loads content as both Rendered and Raw, then
// advances the three-step toggle once so the sidebar shows the Transcript
// rather than its default Activity feed, which is empty in these benchmarks.
func openSidebarOnTranscript(m Model, content string) Model {
	m = Update(m, SidebarLoadedMsg{Number: "42", Rendered: content, Raw: content})
	return Update(m, SidebarToggleMsg{})
}

// BenchmarkUpdate_DrillInScroll_LargeTranscript measures the work Update does
// on every scroll keystroke against a 10MB+ transcript (issue #722, inherited
// from the retired DrillInScrollMsg). Before the DrillInState.Lines cache:
// 2.5MB/op, 1 alloc/op; after: 0 allocs/op (issue #1016). Alloc counts are the
// invariant; ns/op and B/op vary by machine and Go version.
func BenchmarkUpdate_DrillInScroll_LargeTranscript(b *testing.B) {
	content := largeTranscript(10 << 20)
	m := openSidebarOnTranscript(NewModel(), content)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m = Update(m, SidebarScrollMsg{Delta: 1})
	}
}

// BenchmarkView_DrillInFullscreen_LargeTranscript measures
// renderSidebarFullscreen against a 10MB+ transcript (issue #722, inherited
// from the retired renderDrillIn). At Offset 0, Height 24, before windowLines
// capped the join to the viewport: 21.0MB/op, 7 allocs/op; after: 3.39KB/op, 5
// allocs/op (issue #1016). Alloc counts are the invariant; ns/op and B/op vary.
func BenchmarkView_DrillInFullscreen_LargeTranscript(b *testing.B) {
	content := largeTranscript(10 << 20)
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = openSidebarOnTranscript(m, content)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = View(m)
	}
}

// BenchmarkUpdateView_DrillInScroll_LargeTranscript measures the full
// per-keystroke cycle, Update then View, on a 10MB+ transcript (issue #722).
func BenchmarkUpdateView_DrillInScroll_LargeTranscript(b *testing.B) {
	content := largeTranscript(10 << 20)
	m := Update(NewModel(), SizeChangedMsg{Width: 80, Height: 24})
	m = openSidebarOnTranscript(m, content)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m = Update(m, SidebarScrollMsg{Delta: 1})
		_ = View(m)
	}
}

// largeHeartbeatLog returns a synthetic pass log of at least minBytes: valid
// input for driver.Driver's heartbeat parser (repeated tool_use events, one
// terminal result event), big enough that the ReadFile and reparse a
// HeartbeatCache miss pays for show up in a benchmark (issue #731).
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

// newHeartbeatBenchFixture writes a 10MB+ pass log to disk and returns the pwd
// and driver RunningHeartbeat needs to replay it.
func newHeartbeatBenchFixture(b *testing.B) (pwd string, drv driver.Driver) {
	b.Helper()
	dir := b.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".spindrift", "logs", "issue-9.log"), []byte(largeHeartbeatLog(10<<20)), 0o644); err != nil {
		b.Fatal(err)
	}
	drv, err := driver.New("")
	if err != nil {
		b.Fatalf("driver.New: %v", err)
	}
	return dir, drv
}

// BenchmarkHeartbeatCache_ColdRead_LargeLog builds a fresh HeartbeatCache every
// iteration, so every RunningHeartbeat call pays the full ReadFile and reparse
// of a 10MB+ pass log (issue #731).
func BenchmarkHeartbeatCache_ColdRead_LargeLog(b *testing.B) {
	pwd, drv := newHeartbeatBenchFixture(b)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NewHeartbeatCache().RunningHeartbeat(drv, pwd, "9")
	}
}

// BenchmarkHeartbeatCache_CacheHit_LargeLog warms the cache once, then repeats
// against the same unchanged 10MB+ pass log. This is the case syncQueue hits
// on every tea.Msg between actual log growth (issue #731).
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

// BenchmarkTryLaunch_EmptyQueue measures the background poll tick's idle case
// against an empty Queue, the wasted drain goroutine issue #754 closes. Before
// the Queue.Empty() gate (measured at 1ff5dff with launch.wg.Wait() after each
// call, so no iteration hits the already-launching fast path): 1080 B/op, 16
// allocs/op; after: 0 allocs/op (issue #1106). Alloc counts are the invariant.
func BenchmarkTryLaunch_EmptyQueue(b *testing.B) {
	launch := &Launcher{queue: NewQueue()}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		launch.tryLaunch(nil, "")
	}
}
