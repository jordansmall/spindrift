package logscan_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/logscan"
)

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func writeBigLog(t *testing.T, bigLineSize int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "big.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	big := make([]byte, bigLineSize)
	for i := range big {
		big[i] = 'x'
	}
	if _, err := f.Write(big); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestForEachLine_ChunkOversized_FindsMarkerInsideOversizedLine(t *testing.T) {
	const fiveMiB = 5 * 1024 * 1024
	path := writeBigLog(t, fiveMiB)

	// Plant a marker at the tail of the oversized line, past the internal
	// 4 MiB buffer boundary, so it only surfaces via chunked re-reads.
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("MARKER"), fiveMiB-10); err != nil {
		t.Fatal(err)
	}
	f.Close()

	found := false
	err = logscan.ForEachLine(path, logscan.ChunkOversized, func(line string) {
		if strings.Contains(line, "MARKER") {
			found = true
		}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected marker to be found under ChunkOversized policy")
	}
}

func TestForEachLine_SkipOversized_SkipsMarkerInsideOversizedLine(t *testing.T) {
	const fiveMiB = 5 * 1024 * 1024
	path := writeBigLog(t, fiveMiB)

	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("MARKER"), fiveMiB-10); err != nil {
		t.Fatal(err)
	}
	f.Close()

	found := false
	err = logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		if strings.Contains(line, "MARKER") {
			found = true
		}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected marker to be skipped under SkipOversized policy")
	}
}

func TestForEachLine_FileNotFound(t *testing.T) {
	err := logscan.ForEachLine("/nonexistent/path/test.log", logscan.SkipOversized, func(string) {})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v, want an os.ErrNotExist-wrapping error", err)
	}
}

func TestForEachLine_VisitsEveryLine(t *testing.T) {
	path := writeLog(t, "one", "two", "three")

	var got []string
	err := logscan.ForEachLine(path, logscan.SkipOversized, func(line string) {
		got = append(got, line)
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"one", "two", "three"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("line %d: got %q, want %q", i, got[i], w)
		}
	}
}
