package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

// ResolveFromLogs walks every pass log for an issue through the same
// outcome.Resolve seam a live dispatch applies to one log, and a later pass
// overrides an earlier one. Both fixture lines parse the full
// SPINDRIFT_OUTCOME grammar, so they populate the genuine tier and the
// self-report walk that runs alongside it (issue #2268 slice 1) at once.
func TestResolveFromLogs(t *testing.T) {
	dir := t.TempDir()
	logDir := HostLogDirFor(dir)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	initialLog := filepath.Join(logDir, "issue-42.log")
	initialLine := "SPINDRIFT_OUTCOME issue=42 landing=agent/issue-42 status=ready note=first\n"
	if err := os.WriteFile(initialLog, []byte(initialLine), 0o644); err != nil {
		t.Fatalf("WriteFile initial: %v", err)
	}

	resolved, err := ResolveFromLogs(dir, "42", "work")
	if err != nil {
		t.Fatalf("ResolveFromLogs: %v", err)
	}
	if !resolved.Found {
		t.Fatalf("Resolved.Found = false, want true")
	}
	if resolved.Outcome.Status != "ready" {
		t.Fatalf("Resolved.Outcome.Status = %q, want %q", resolved.Outcome.Status, "ready")
	}
	if !resolved.SelfReportFound {
		t.Fatalf("Resolved.SelfReportFound = false, want true")
	}
	if resolved.SelfReport.Status != "ready" {
		t.Fatalf("Resolved.SelfReport.Status = %q, want %q", resolved.SelfReport.Status, "ready")
	}

	fixLog := filepath.Join(logDir, "issue-42-fix-1.log")
	fixLine := "SPINDRIFT_OUTCOME issue=42 landing=agent/issue-42 status=blocked note=fixed\n"
	if err := os.WriteFile(fixLog, []byte(fixLine), 0o644); err != nil {
		t.Fatalf("WriteFile fix: %v", err)
	}

	resolved, err = ResolveFromLogs(dir, "42", "work")
	if err != nil {
		t.Fatalf("ResolveFromLogs after fix pass: %v", err)
	}
	if !resolved.Found {
		t.Fatalf("Resolved.Found after fix pass = false, want true")
	}
	if resolved.Outcome.Status != "blocked" {
		t.Fatalf("Resolved.Outcome.Status after fix pass = %q, want %q (last pass should win)", resolved.Outcome.Status, "blocked")
	}
	if resolved.SelfReport.Status != "blocked" {
		t.Fatalf("Resolved.SelfReport.Status after fix pass = %q, want %q (last pass should win)", resolved.SelfReport.Status, "blocked")
	}
}

// Not-found is not an error: with no pass log on disk, ResolveFromLogs
// returns Resolved{Found: false} and nil, matching outcome.Resolve.
func TestResolveFromLogsNoLogs(t *testing.T) {
	dir := t.TempDir()

	resolved, err := ResolveFromLogs(dir, "99", "work")
	if err != nil {
		t.Fatalf("ResolveFromLogs: %v", err)
	}
	if resolved.Found {
		t.Fatalf("Resolved.Found = true, want false; resolved = %+v", resolved)
	}
}

// ADR 0055 retired the nonce gate, so a near-miss leading-token line that
// does not parse the full grammar is Resolve's own error, not a fallback to
// the self-report tier.
func TestResolveFromLogsNearMissPropagatesError(t *testing.T) {
	dir := t.TempDir()
	logDir := HostLogDirFor(dir)
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	initialLog := filepath.Join(logDir, "issue-7.log")
	if err := os.WriteFile(initialLog, []byte("SPINDRIFT_OUTCOME: success\n"), 0o644); err != nil {
		t.Fatalf("WriteFile initial: %v", err)
	}

	_, err := ResolveFromLogs(dir, "7", "work")
	if err == nil {
		t.Fatal("ResolveFromLogs: want error for a near-miss leading-token line, got nil")
	}
}
