package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
)

// This test pins the "can't read" contract appendHeartbeat and appendActivity
// both rely on, at the tailer directly rather than through those callers.
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

// The offset assertion matters because a follow-up call against an unchanged
// file must find nothing left to read.
func TestTailer_ReadAppended_AdvancesOffsetByBytesRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issue-9.log")
	log := `{"Kind":"result","Tokens":{"CostUSD":0.01},"DurationMs":5000,"NumTurns":7}` + "\n"
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

// The tailer feeds the live Console view, so it shows the host's host_op
// events.
func TestTailer_ReadAppended_ShowsHostOpLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issue-9.log")
	log := `{"Kind":"host_op","HostOp":{"op":"pass_start","pass":1,"role":"implement"}}` + "\n"
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
	if want := "pass 1 (implement) started"; !strings.Contains(data, want) {
		t.Errorf("readAppended() data = %q, want it to contain %q", data, want)
	}
}

// The path is a directory because that is how this test reaches a read
// failure past a successful Open: a directory opens fine but cannot be read
// as a file. The offset must survive such a hiccup for a later call to use.
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

// The byte count proves entry.out gets reset and reused between calls rather
// than allocated once and left to accumulate the whole file again.
func TestTailer_ReadAppended_SecondCall_FeedsOnlyAppendedBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "issue-9.log")
	first := `{"Kind":"start"}` + "\n" +
		`{"Kind":"model"}` + "\n" +
		`{"Kind":"tool_call","ToolID":"r1","ToolName":"Read","ToolInput":{}}` + "\n" +
		`{"Kind":"usage"}` + "\n"
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

	second := first + `{"Kind":"result","Tokens":{"CostUSD":0.01},"DurationMs":5000,"NumTurns":3}` + "\n"
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
