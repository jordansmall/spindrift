package driverkit

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"spindrift.dev/launcher/internal/logscan"
)

func TestScanLogMissingFileDegradesToNil(t *testing.T) {
	called := false
	err := ScanLog(filepath.Join(t.TempDir(), "does-not-exist.log"), logscan.SkipOversized, func(line string) {
		called = true
	})
	if err != nil {
		t.Fatalf("ScanLog: unexpected error: %v", err)
	}
	if called {
		t.Errorf("ScanLog: fn was called for a nonexistent file")
	}
}

func TestScanLogCallsFnPerLineInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.txt")
	if err := os.WriteFile(path, []byte("first\nsecond\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var got []string
	err := ScanLog(path, logscan.SkipOversized, func(line string) {
		got = append(got, line)
	})
	if err != nil {
		t.Fatalf("ScanLog: unexpected error: %v", err)
	}
	want := []string{"first", "second"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
