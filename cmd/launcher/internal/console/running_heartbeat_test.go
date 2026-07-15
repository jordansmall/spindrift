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
