package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLastSelfReportFromLogs(t *testing.T) {
	dir := t.TempDir()
	logDir := HostLogDirFor(dir)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	initialLog := filepath.Join(logDir, "issue-42.log")
	if err := os.WriteFile(initialLog, []byte("SPINDRIFT_OUTCOME: success\n"), 0o644); err != nil {
		t.Fatalf("WriteFile initial: %v", err)
	}

	report, found := LastSelfReportFromLogs(dir, "42")
	if !found {
		t.Fatalf("LastSelfReportFromLogs: found = false, want true")
	}
	if report.Status != "success" {
		t.Fatalf("Status = %q, want %q", report.Status, "success")
	}

	fixLog := filepath.Join(logDir, "issue-42-fix-1.log")
	fixLine := "SPINDRIFT_OUTCOME issue=42 landing=agent/issue-42 status=ready note=done\n"
	if err := os.WriteFile(fixLog, []byte(fixLine), 0o644); err != nil {
		t.Fatalf("WriteFile fix: %v", err)
	}

	report, found = LastSelfReportFromLogs(dir, "42")
	if !found {
		t.Fatalf("LastSelfReportFromLogs after fix pass: found = false, want true")
	}
	if report.Status != "ready" {
		t.Fatalf("Status after fix pass = %q, want %q (last pass should win)", report.Status, "ready")
	}
}

func TestLastSelfReportFromLogsNoLogs(t *testing.T) {
	dir := t.TempDir()

	report, found := LastSelfReportFromLogs(dir, "99")
	if found {
		t.Fatalf("LastSelfReportFromLogs: found = true, want false; report = %+v", report)
	}
}
