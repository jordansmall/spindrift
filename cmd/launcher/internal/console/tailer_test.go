package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// TestTailer_ReadAppended_MissingFile_ReturnsNotOk verifies readAppended
// reports ok=false rather than panicking or returning a zero-value success
// when path no longer exists — the same "can't read" contract
// appendHeartbeat and appendActivity both rely on, exercised here at the
// tailer directly rather than only indirectly through those two callers.
func TestTailer_ReadAppended_MissingFile_ReturnsNotOk(t *testing.T) {
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	tl := &tailer{path: t.TempDir() + "/does-not-exist.log"}
	data, ok := tl.readAppended(drv, "9")

	if ok {
		t.Errorf("readAppended() ok = true, want false for a missing file")
	}
	if data != "" {
		t.Errorf("readAppended() data = %q, want empty on failure", data)
	}
}

// TestTailer_ReadAppended_AdvancesOffsetByBytesRead verifies a first call
// against a fresh tailer feeds the whole file through drv's parser and
// leaves t.offset at the file's length, so a follow-up call against an
// unchanged file has nothing left to read.
func TestTailer_ReadAppended_AdvancesOffsetByBytesRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issue-9.log")
	log := `{"type":"result","num_turns":7,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	if err := os.WriteFile(path, []byte(log), 0o644); err != nil {
		t.Fatal(err)
	}

	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	tl := &tailer{path: path}
	data, ok := tl.readAppended(drv, "9")

	if !ok {
		t.Fatal("readAppended() ok = false, want true")
	}
	if want := "7 turn"; !strings.Contains(data, want) {
		t.Errorf("readAppended() data = %q, want it to contain %q", data, want)
	}
	if tl.offset != int64(len(log)) {
		t.Errorf("tailer.offset after read = %d, want %d (the whole file's length)", tl.offset, len(log))
	}
}

// TestTailer_ReadAppended_DirectoryPath_ReturnsNotOkAndLeavesOffsetUnchanged
// verifies that a read failure past a successful Open (a directory opens
// fine but can't be read as a file) reports ok=false and leaves t.offset
// untouched, so a transient hiccup doesn't clobber state a later call would
// rely on.
func TestTailer_ReadAppended_DirectoryPath_ReturnsNotOkAndLeavesOffsetUnchanged(t *testing.T) {
	drv, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}

	tl := &tailer{path: t.TempDir(), offset: 3}
	data, ok := tl.readAppended(drv, "9")

	if ok {
		t.Errorf("readAppended() ok = true, want false for a directory path")
	}
	if data != "" {
		t.Errorf("readAppended() data = %q, want empty on failure", data)
	}
	if tl.offset != 3 {
		t.Errorf("tailer.offset after failed read = %d, want unchanged 3", tl.offset)
	}
}

// TestTailer_ReadAppended_SecondCall_FeedsOnlyAppendedBytes verifies a
// second readAppended call against the same tailer, after more bytes were
// appended to path, feeds the parser only the bytes appended since the first
// call — not the whole file again — proving entry.out gets reset and reused
// rather than only allocated once and left to accumulate.
func TestTailer_ReadAppended_SecondCall_FeedsOnlyAppendedBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issue-9.log")
	first := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","id":"r1","input":{}}]}}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}

	real, err := driver.New("")
	if err != nil {
		t.Fatalf("driver.New: %v", err)
	}
	var fed int
	drv := spyHeartbeatDriver{Driver: real, fed: &fed}

	tl := &tailer{path: path}
	if _, ok := tl.readAppended(drv, "9"); !ok {
		t.Fatal("first readAppended() ok = false, want true")
	}

	second := first + `{"type":"result","num_turns":3,"total_cost_usd":0.01,"duration_ms":5000}` + "\n"
	if err := os.WriteFile(path, []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}

	data, ok := tl.readAppended(drv, "9")
	if !ok {
		t.Fatal("second readAppended() ok = false, want true")
	}
	if want := "3 turn"; !strings.Contains(data, want) {
		t.Errorf("second readAppended() data = %q, want it to contain %q", data, want)
	}
	if tl.offset != int64(len(second)) {
		t.Errorf("tailer.offset after second read = %d, want %d (the whole file's length)", tl.offset, len(second))
	}
	if fed != len(second) {
		t.Errorf("bytes fed to parser across both calls = %d, want %d (exactly the bytes ever appended, not the second file's length fed twice)", fed, len(second))
	}
}
