package console

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
)

// RunningHeartbeat must reuse the Driver's own heartbeat parser, the same one
// the live dispatch stdout heartbeat uses, against the on-disk log, and return
// the coarse status line it last emitted (#647 AC2).
func TestRunningHeartbeat_ReplaysLatestPassLog_ReturnsLastEmittedLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"type":"result","num_turns":7,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".spindrift", "logs", "issue-9.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	got := NewHeartbeatCache().RunningHeartbeat(drv, dir, "9")

	if got == "" {
		t.Fatal("RunningHeartbeat() = \"\", want a non-empty heartbeat line")
	}
	if want := "7 turn"; !strings.Contains(got, want) {
		t.Errorf("RunningHeartbeat() = %q, want it to contain %q", got, want)
	}
}

// When the log ends mid-subagent (the implementor spawns a scout and the pass
// result fires while the scout is still acting), the returned line must name
// the scout, not a bare implementor-looking phase tag, so an operator never
// mistakes subagent output for the implementor's (#732).
func TestRunningHeartbeat_RoleSwitchMidLog_ReturnsRoleContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Task","id":"tu_s1","input":{"subagent_type":"scout"}}]}}`,
		`{"type":"assistant","parent_tool_use_id":"tu_s1","message":{"content":[{"type":"tool_use","name":"Read","id":"r1","input":{}}]}}`,
		`{"type":"result","num_turns":3,"total_cost_usd":0.01,"duration_ms":5000}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".spindrift", "logs", "issue-9.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	got := NewHeartbeatCache().RunningHeartbeat(drv, dir, "9")

	if !strings.Contains(got, "scout") {
		t.Errorf("RunningHeartbeat() = %q, want it to name the acting role \"scout\"", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("RunningHeartbeat() = %q, want a single-line row", got)
	}
}

// The count-line path, not just the trailing turns line, must carry role
// context, so a log ending on a scout's tool-count line with no result event
// yet still names the scout (#732).
func TestRunningHeartbeat_LogEndsOnScoutCountLine_ReturnsRoleContext(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Task","id":"tu_s1","input":{"subagent_type":"scout"}}]}}`,
		`{"type":"assistant","parent_tool_use_id":"tu_s1","message":{"content":[{"type":"tool_use","name":"Read","id":"r1","input":{}}]}}`,
		`{"type":"assistant","parent_tool_use_id":"tu_s1","message":{"content":[{"type":"tool_use","name":"Grep","id":"g1","input":{}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".spindrift", "logs", "issue-9.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	got := NewHeartbeatCache().RunningHeartbeat(drv, dir, "9")

	if !strings.Contains(got, "scout") {
		t.Errorf("RunningHeartbeat() = %q, want it to name the acting role \"scout\"", got)
	}
}

// A pick claimed but not yet launched has written no log, and that must render
// no heartbeat rather than erroring.
func TestRunningHeartbeat_NoLogsOnDisk_ReturnsEmpty(t *testing.T) {
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	if got := NewHeartbeatCache().RunningHeartbeat(drv, t.TempDir(), "9"); got != "" {
		t.Errorf("RunningHeartbeat() = %q, want empty with no log on disk", got)
	}
}

// RunningHeartbeat must return the cached line instead of re-reading a file
// whose size and mtime are unchanged (#731). Proving that needs the file
// rewritten underneath the cache with same-length content and its mtime pinned
// back by os.Chtimes, so the stale cached line is the only possible evidence.
func TestHeartbeatCache_UnchangedStat_SkipsReparse(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".spindrift", "logs", "issue-9.log")
	first := `{"type":"result","num_turns":17,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	cache := NewHeartbeatCache()

	first1 := cache.RunningHeartbeat(drv, dir, "9")
	if want := "17 turn"; !strings.Contains(first1, want) {
		t.Fatalf("first call = %q, want it to contain %q", first1, want)
	}

	second := `{"type":"result","num_turns":99,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	if len(second) != len(first) {
		t.Fatalf("test setup: second log must be same length as first, got %d want %d", len(second), len(first))
	}
	if err := os.WriteFile(path, []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	got := cache.RunningHeartbeat(drv, dir, "9")
	if got != first1 {
		t.Errorf("RunningHeartbeat() after unchanged stat = %q, want cached %q (unchanged stat must skip reparse)", got, first1)
	}
}

// A log that grew since the cached call carries a genuinely new heartbeat line
// from the running pick, which the cache must not mask.
func TestHeartbeatCache_ChangedStat_Reparses(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".spindrift", "logs", "issue-9.log")
	first := `{"type":"result","num_turns":7,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	cache := NewHeartbeatCache()

	first1 := cache.RunningHeartbeat(drv, dir, "9")
	if want := "7 turn"; !strings.Contains(first1, want) {
		t.Fatalf("first call = %q, want it to contain %q", first1, want)
	}

	second := first + `{"type":"result","num_turns":42,"total_cost_usd":0.02,"duration_ms":9000}` + "\n"
	if err := os.WriteFile(path, []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}

	got := cache.RunningHeartbeat(drv, dir, "9")
	if want := "42 turn"; !strings.Contains(got, want) {
		t.Errorf("RunningHeartbeat() after grown log = %q, want it to contain %q (must reparse on changed stat)", got, want)
	}
}

// byteCountingWriter adds every Write's length onto total, so a test can assert
// how many bytes a parser consumed across a series of calls.
type byteCountingWriter struct {
	io.Writer
	total *int
}

func (w byteCountingWriter) Write(p []byte) (int, error) {
	*w.total += len(p)
	return w.Writer.Write(p)
}

// spyHeartbeatDriver counts every byte its heartbeat Writer is fed across every
// RunningHeartbeat call, so a test can tell an incremental append-tail (bytes
// fed equals bytes ever appended) apart from a whole-file reparse (bytes fed
// grows with every call).
type spyHeartbeatDriver struct {
	driver.Driver
	fed *int
}

func (d spyHeartbeatDriver) NewHeartbeatWriter(raw io.Writer, issue string, out io.Writer, opts driverkit.RenderOptions) io.Writer {
	inner := d.Driver.NewHeartbeatWriter(raw, issue, out, opts)
	return byteCountingWriter{Writer: inner, total: d.fed}
}

// Successive appends to the same running pick's log must hand the driver's
// heartbeat parser only the bytes appended since the last call, not the whole
// file again. The append-tail replaces an O(file)-per-refresh whole-file reread.
func TestRunningHeartbeat_IncrementalAppend_FeedsOnlyAppendedBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".spindrift", "logs", "issue-9.log")

	real, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	var fed int
	drv := spyHeartbeatDriver{Driver: real, fed: &fed}
	cache := NewHeartbeatCache()

	chunks := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","id":"r1","input":{}}]}}` + "\n",
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","id":"g1","input":{}}]}}` + "\n",
		`{"type":"result","num_turns":3,"total_cost_usd":0.01,"duration_ms":5000}` + "\n",
	}

	var written, got string
	for _, chunk := range chunks {
		written += chunk
		if err := os.WriteFile(path, []byte(written), 0o644); err != nil {
			t.Fatal(err)
		}
		got = cache.RunningHeartbeat(drv, dir, "9")
	}

	if want := "3 turn"; !strings.Contains(got, want) {
		t.Errorf("RunningHeartbeat() after 3 appends = %q, want it to contain %q", got, want)
	}
	if fed != len(written) {
		t.Errorf("bytes fed to heartbeat parser across 3 appends = %d, want %d (exactly the appended bytes, not a whole-file reread each call)", fed, len(written))
	}
}

// A log truncated or rotated out from under a running pick falls below the
// cache's stored offset, so RunningHeartbeat must reset the offset and start a
// fresh parser at 0 instead of seeking past the file's new end and mis-parsing.
func TestRunningHeartbeat_FileShorterThanOffset_ResetsAndReparses(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".spindrift", "logs", "issue-9.log")
	long := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","id":"r1","input":{}}]}}` + "\n" +
		`{"type":"result","num_turns":7,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	if err := os.WriteFile(path, []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	cache := NewHeartbeatCache()

	first := cache.RunningHeartbeat(drv, dir, "9")
	if want := "7 turn"; !strings.Contains(first, want) {
		t.Fatalf("first call = %q, want it to contain %q", first, want)
	}

	short := `{"type":"result","num_turns":2,"total_cost_usd":0.01,"duration_ms":1000}` + "\n"
	if len(short) >= len(long) {
		t.Fatalf("test setup: short log must be shorter than long, got %d want < %d", len(short), len(long))
	}
	if err := os.WriteFile(path, []byte(short), 0o644); err != nil {
		t.Fatal(err)
	}

	got := cache.RunningHeartbeat(drv, dir, "9")
	if want := "2 turn"; !strings.Contains(got, want) {
		t.Errorf("RunningHeartbeat() after truncation = %q, want it to contain %q (must reset offset and reparse from 0)", got, want)
	}
}

// A new Dispatch pass means a new log path, per LogPaths' chronological pass
// discovery, and must start a fresh parser at offset 0. The fix-1 log is
// deliberately longer than the initial pass log: a regression that dropped the
// path-change reset would seek into the middle of fix-1's content and corrupt
// the parse rather than merely fail to reset.
func TestRunningHeartbeat_NewPassPath_ResetsOffsetAndReparses(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".spindrift", "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := `{"type":"result","num_turns":7,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".spindrift", "logs", "issue-9.log"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	cache := NewHeartbeatCache()

	first := cache.RunningHeartbeat(drv, dir, "9")
	if want := "7 turn"; !strings.Contains(first, want) {
		t.Fatalf("first call = %q, want it to contain %q", first, want)
	}

	fix1 := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","id":"r1","input":{}}]}}` + "\n" +
		`{"type":"result","num_turns":4,"total_cost_usd":0.02,"duration_ms":6000}` + "\n"
	if len(fix1) <= len(initial) {
		t.Fatalf("test setup: fix-1 log must be longer than the initial pass log, got %d want > %d", len(fix1), len(initial))
	}
	if err := os.WriteFile(filepath.Join(dir, ".spindrift", "logs", "issue-9-fix-1.log"), []byte(fix1), 0o644); err != nil {
		t.Fatal(err)
	}

	got := cache.RunningHeartbeat(drv, dir, "9")
	if want := "4 turn"; !strings.Contains(got, want) {
		t.Errorf("RunningHeartbeat() on new fix-1 pass = %q, want it to contain %q (a new pass path must reset offset and reparse from 0, not reuse the initial pass's parser)", got, want)
	}
}
