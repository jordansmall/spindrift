package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver/corpus"
)

// Planted at runtime so this file never holds a literal the guard rejects.
func plantedLog() string {
	return `{"a":"` + "gh" + "p_" + strings.Repeat("a", 36) + `"}` + "\n" +
		`{"b":"` + "AK" + "IA" + strings.Repeat("A", 16) + `"}` + "\n" +
		`{"c":"` + "gh" + "o_" + strings.Repeat("b", 36) + `"}` + "\n" +
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t","content":"private"}]}}` + "\n"
}

func TestRunStdinToStdout(t *testing.T) {
	for _, args := range [][]string{nil, {"-"}} {
		var stdout, stderr bytes.Buffer
		if err := run(args, strings.NewReader(plantedLog()), &stdout, &stderr); err != nil {
			t.Fatalf("run(%v): %v", args, err)
		}
		if f := corpus.Find(stdout.Bytes()); len(f) != 0 {
			t.Errorf("run(%v): output still holds findings %v", args, f)
		}
		want := "redacted 1 aws-access-key-id\nredacted 2 github-token\nredacted 1 tool-output\n"
		if stderr.String() != want {
			t.Errorf("run(%v) stderr = %q, want %q", args, stderr.String(), want)
		}

		var again, againErr bytes.Buffer
		if err := run(args, bytes.NewReader(stdout.Bytes()), &again, &againErr); err != nil {
			t.Fatalf("rerun(%v): %v", args, err)
		}
		if !bytes.Equal(again.Bytes(), stdout.Bytes()) || againErr.Len() != 0 {
			t.Errorf("run(%v) is not idempotent: %q, stderr %q", args, again.Bytes(), againErr.String())
		}
	}
}

func TestRunFileToFile(t *testing.T) {
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.jsonl"), filepath.Join(dir, "out.jsonl")
	if err := os.WriteFile(in, []byte(plantedLog()), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{in, out}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty when an output file is named", stdout.String())
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, corpus.Scrub([]byte(plantedLog()))) {
		t.Errorf("output file = %q, want the scrubbed log", got)
	}
	if f := corpus.Find(got); len(f) != 0 {
		t.Errorf("output file still holds findings %v", f)
	}
	if !strings.Contains(stderr.String(), "redacted 2 github-token") {
		t.Errorf("stderr = %q, want a github-token count", stderr.String())
	}
}

func TestRunRejectsExtraArgs(t *testing.T) {
	err := run([]string{"a", "b", "c"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("err = %v, want a usage error", err)
	}
}
