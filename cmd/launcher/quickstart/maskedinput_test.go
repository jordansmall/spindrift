package main

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// readMasked must only attempt to disable terminal echo for a real TTY file
// descriptor; every other stdin shape falls back to the plain scanner so the
// wizard's existing strings.NewReader-driven tests, and real non-interactive
// input (pipes, redirected files), keep working unchanged.

func TestReadMasked_NonFileStdin_FallsBackToScanner(t *testing.T) {
	stdin := strings.NewReader("secret-value\n")
	scanner := bufio.NewScanner(stdin)

	value, masked := readMasked(stdin, scanner)

	if masked {
		t.Fatal("masked = true, want false for a non-*os.File stdin")
	}
	if value != "secret-value" {
		t.Fatalf("value = %q, want %q", value, "secret-value")
	}
}

func TestReadMasked_NonTTYFile_FallsBackToScanner(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer r.Close()

	if _, err := w.WriteString("secret-value\n"); err != nil {
		t.Fatalf("write to pipe: %v", err)
	}
	w.Close()

	scanner := bufio.NewScanner(r)

	value, masked := readMasked(r, scanner)

	if masked {
		t.Fatal("masked = true, want false for a non-TTY *os.File (pipe)")
	}
	if value != "secret-value" {
		t.Fatalf("value = %q, want %q", value, "secret-value")
	}
}
