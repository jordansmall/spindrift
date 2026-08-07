package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateFindsMarkedTypes(t *testing.T) {
	got, err := generate("../testdata/fixture")
	if err != nil {
		t.Fatalf("generate returned error: %v", err)
	}

	for _, want := range []string{`"AMsg"`, `"BMsg"`} {
		if !strings.Contains(string(got), want) {
			t.Errorf("generate(../testdata/fixture) = %q, want to contain %q", got, want)
		}
	}
}

func TestResolveConsoleDir(t *testing.T) {
	dir, err := resolveConsoleDir()
	if err != nil {
		t.Fatalf("resolveConsoleDir returned error: %v", err)
	}

	msgcensusDir := filepath.Join(dir, "msgcensus")
	info, err := os.Stat(msgcensusDir)
	if err != nil {
		t.Fatalf("os.Stat(%q) = %v, want no error", msgcensusDir, err)
	}
	if !info.IsDir() {
		t.Errorf("os.Stat(%q).IsDir() = false, want true", msgcensusDir)
	}
}

func TestRunWritesFormattedOutput(t *testing.T) {
	origWriteFile := writeFile
	t.Cleanup(func() { writeFile = origWriteFile })

	var gotName string
	var gotData []byte
	var gotPerm os.FileMode
	writeFile = func(name string, data []byte, perm os.FileMode) error {
		gotName = name
		gotData = data
		gotPerm = perm
		return nil
	}

	if err := run(); err != nil {
		t.Fatalf("run() returned error: %v", err)
	}

	if !strings.HasSuffix(gotName, "msg_census_gen.go") {
		t.Errorf("run() wrote to %q, want suffix %q", gotName, "msg_census_gen.go")
	}
	if len(gotData) == 0 {
		t.Errorf("run() wrote empty data, want non-empty formatted output")
	}
	if !strings.Contains(string(gotData), "var msgCensus = []string{") {
		t.Errorf("run() wrote %q, want to contain %q", gotData, "var msgCensus = []string{")
	}
	if gotPerm != 0o644 {
		t.Errorf("run() wrote with perm %v, want %v", gotPerm, os.FileMode(0o644))
	}
}

func TestGenerateNoMatches(t *testing.T) {
	got, err := generate("../testdata/empty")
	if err != nil {
		t.Fatalf("generate returned error: %v", err)
	}

	s := string(got)
	if strings.Contains(s, `"`) {
		t.Errorf("generate(../testdata/empty) = %q, want no quoted type names", s)
	}
	if !strings.Contains(s, "var msgCensus = []string{") {
		t.Errorf("generate(../testdata/empty) = %q, want to contain header var declaration", s)
	}
	if !strings.Contains(s, "}\n") {
		t.Errorf("generate(../testdata/empty) = %q, want to contain closing brace", s)
	}
}
