package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// TestActivityFeed_ReplaysLatestPassLog_ReturnsOrderedDistinctLines verifies
// ActivityFeed replays the Dispatch's most-recent pass log through drv's
// heartbeat parser -- the same machinery RunningHeartbeat uses -- and returns
// the whole ordered sequence of emitted status lines, not just the last one
// (#1501 AC1).
func TestActivityFeed_ReplaysLatestPassLog_ReturnsOrderedDistinctLines(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Reading the config file."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","id":"e1","input":{}}]}}`,
		`{"type":"result","num_turns":3,"total_cost_usd":0.01,"duration_ms":5000}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-9.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	got := ActivityFeed(drv, dir, "9")

	if len(got) < 2 {
		t.Fatalf("ActivityFeed() = %d lines, want at least 2 (narration + final turns line)", len(got))
	}
	var sawNarration bool
	for _, line := range got[:len(got)-1] {
		if strings.Contains(line.Text, "Reading the config file") {
			sawNarration = true
		}
	}
	if !sawNarration {
		t.Errorf("ActivityFeed() = %v, want one line to contain the narration", got)
	}
	last := got[len(got)-1]
	if !strings.Contains(last.Text, "3 turn") {
		t.Errorf("last line = %q, want it to contain %q", last.Text, "3 turn")
	}
}

// TestActivityFeed_ConsecutiveIdenticalNarration_CollapsesToOneLine verifies
// two events that narrate the exact same trimmed text back-to-back (the
// heartbeat writer emits one line per parsed event, not per distinct step)
// collapse to a single feed entry, so the feed reads as one line per
// distinct step rather than a literal per-event replay (#1501 AC1).
func TestActivityFeed_ConsecutiveIdenticalNarration_CollapsesToOneLine(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	log := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Reading the config file."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Reading the config file."}]}}`,
		`{"type":"result","num_turns":1,"total_cost_usd":0.01,"duration_ms":5000}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "issue-9.log"), []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	got := ActivityFeed(drv, dir, "9")

	var narrationCount int
	for _, line := range got {
		if strings.Contains(line.Text, "Reading the config file") {
			narrationCount++
		}
	}
	if narrationCount != 1 {
		t.Errorf("ActivityFeed() had %d narration lines, want exactly 1 (consecutive duplicates collapsed): %v", narrationCount, got)
	}
}

// TestActivityFeed_NoLogsOnDisk_ReturnsEmpty verifies a pick that hasn't
// written any log yet (claimed but not yet launched) renders no Activity
// feed rather than erroring (#1501 AC1).
func TestActivityFeed_NoLogsOnDisk_ReturnsEmpty(t *testing.T) {
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	if got := ActivityFeed(drv, t.TempDir(), "9"); got != nil {
		t.Errorf("ActivityFeed() = %v, want nil with no log on disk", got)
	}
}

// TestActivityFeed_UnreadableLog_ReturnsEmpty verifies a pass log that
// exists on disk but can't be read (a directory in its place, standing in
// for any read failure) degrades to an empty feed instead of erroring
// (#1501 AC1).
func TestActivityFeed_UnreadableLog_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory at the log's path fails os.ReadFile with EISDIR, standing
	// in for any on-disk read failure LogPaths' existence check can't rule
	// out ahead of time.
	if err := os.MkdirAll(filepath.Join(logDir, "issue-9.log"), 0o755); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	if got := ActivityFeed(drv, dir, "9"); got != nil {
		t.Errorf("ActivityFeed() = %v, want nil for an unreadable log", got)
	}
}
