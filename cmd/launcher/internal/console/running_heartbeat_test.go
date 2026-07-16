package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// TestRunningHeartbeat_ReplaysLatestPassLog_ReturnsLastEmittedLine verifies
// RunningHeartbeat reuses the Driver's own heartbeat parser — the same one
// the live dispatch stdout heartbeat already uses — against the on-disk log,
// rather than a new parser, and returns the coarse status line it last
// emitted (#647 AC2).
func TestRunningHeartbeat_ReplaysLatestPassLog_ReturnsLastEmittedLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"type":"result","num_turns":7,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-9.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	got := RunningHeartbeat(drv, dir, "9")

	if got == "" {
		t.Fatal("RunningHeartbeat() = \"\", want a non-empty heartbeat line")
	}
	if want := "7 turn"; !strings.Contains(got, want) {
		t.Errorf("RunningHeartbeat() = %q, want it to contain %q", got, want)
	}
}

// TestRunningHeartbeat_NoLogsOnDisk_ReturnsEmpty verifies a pick that hasn't
// written any log yet (claimed but not yet launched) renders no heartbeat
// rather than erroring.
func TestRunningHeartbeat_NoLogsOnDisk_ReturnsEmpty(t *testing.T) {
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	if got := RunningHeartbeat(drv, t.TempDir(), "9"); got != "" {
		t.Errorf("RunningHeartbeat() = %q, want empty with no log on disk", got)
	}
}

// TestHeartbeatCache_UnchangedStat_SkipsReparse verifies a second call for
// the same pick number, with the latest pass log's size and mtime unchanged
// since the cached call, returns the cached line instead of re-reading and
// re-parsing the file (issue #731) — proven by rewriting the file's content
// underneath the cache (same size, mtime pinned back with os.Chtimes) and
// showing the stale cached line comes back rather than the new content's.
func TestHeartbeatCache_UnchangedStat_SkipsReparse(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "logs", "issue-9.log")
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

// TestHeartbeatCache_ChangedStat_Reparses verifies a call whose latest pass
// log grew since the cached call (a genuinely new heartbeat line written by
// the running pick) is not masked by the cache — it reparses and returns the
// new content, not the stale cached line.
func TestHeartbeatCache_ChangedStat_Reparses(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "logs", "issue-9.log")
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
