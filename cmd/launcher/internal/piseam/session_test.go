//go:build integration

package piseam

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sessionFiles lists the persisted sessions under the agent dir. Pi groups
// them in one directory per working directory.
func sessionFiles(t *testing.T, agentDir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(agentDir, "sessions", "*", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// sessionEntries decodes the JSONL entries of one session file.
func sessionEntries(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("%s: not JSON: %v\n%s", path, err, line)
		}
		out = append(out, e)
	}
	return out
}

func TestPiModelThinkingSuffix(t *testing.T) {
	const model = stubModelRef
	for _, tc := range []struct {
		name          string
		args          []string
		reasoning     bool
		wantEffort    any // reasoning_effort in the stub request; nil means absent
		wantLevel     string
		wantModelSent string
		wantStderr    string // substring pi must print; empty means stderr must be empty
	}{
		{name: "flag", args: []string{"--model", model, "--thinking", "high"}, reasoning: true, wantEffort: "high", wantLevel: "high", wantModelSent: stubModel},
		{name: "suffix", args: []string{"--model", model + ":high"}, reasoning: true, wantEffort: "high", wantLevel: "high", wantModelSent: stubModel},
		{name: "flag wins over suffix", args: []string{"--model", model + ":low", "--thinking", "high"}, reasoning: true, wantEffort: "high", wantLevel: "high", wantModelSent: stubModel},
		{name: "suffix off", args: []string{"--model", model + ":off"}, reasoning: true, wantEffort: nil, wantLevel: "off", wantModelSent: stubModel},
		{name: "no level defaults to medium", args: []string{"--model", model}, reasoning: true, wantEffort: "medium", wantLevel: "medium", wantModelSent: stubModel},
		{name: "non-reasoning model ignores suffix", args: []string{"--model", model + ":high"}, reasoning: false, wantEffort: nil, wantLevel: "off", wantModelSent: stubModel},
		// An unrecognised suffix is not an error: pi warns on stderr and keeps it as part of a custom model id.
		{name: "unknown suffix becomes model id", args: []string{"--model", model + ":bogus"}, reasoning: true, wantEffort: "medium", wantLevel: "medium", wantModelSent: stubModel + ":bogus", wantStderr: "Using custom model id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, textReply("ok"))
			dirs := newPiDirs(t)
			r := runPiWith(t, s, piOpts{args: tc.args, reasoning: tc.reasoning, dirs: &dirs}, "Think.")
			if r.err != nil {
				t.Fatalf("pi failed (%s)\nstderr:\n%s\nstdout:\n%s", exitDetail(r.err), r.stderr, r.stdout)
			}
			if tc.wantStderr == "" && r.stderr != "" || !strings.Contains(r.stderr, tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", r.stderr, tc.wantStderr)
			}
			reqs := s.Requests()
			if len(reqs) != 1 {
				t.Fatalf("stub got %d requests, want 1", len(reqs))
			}
			if got := reqs[0]["reasoning_effort"]; got != tc.wantEffort {
				t.Errorf("request reasoning_effort = %v, want %v", got, tc.wantEffort)
			}
			if got := reqs[0]["model"]; got != tc.wantModelSent {
				t.Errorf("request model = %v, want %v", got, tc.wantModelSent)
			}
			if got := lastAssistantMessage(r.events)["thinkingLevel"]; got != tc.wantLevel {
				t.Errorf("assistant message thinkingLevel = %v, want %v", got, tc.wantLevel)
			}
			// The level is not in the session event; the persisted session records it.
			files := sessionFiles(t, dirs.agentDir)
			if len(files) != 1 {
				t.Fatalf("got %d session files, want 1", len(files))
			}
			var levels []any
			for _, e := range sessionEntries(t, files[0]) {
				if e["type"] == "thinking_level_change" {
					levels = append(levels, e["thinkingLevel"])
				}
			}
			if len(levels) != 1 || levels[0] != tc.wantLevel {
				t.Errorf("session thinking_level_change entries = %v, want [%s]", levels, tc.wantLevel)
			}
		})
	}
}

func TestPiSessionIDOpenOrCreate(t *testing.T) {
	const id = "0194c0de-5e55-7a11-9d00-5e551d5e551d"
	s := newStub(t, textReply("first-answer"), textReply("second-answer"))
	dirs := newPiDirs(t)
	opts := piOpts{args: []string{"--model", stubModelRef, "--session-id", id}, dirs: &dirs}

	first := runPiWith(t, s, opts, "first-prompt")
	if first.err != nil {
		t.Fatalf("first run failed (%s)\nstderr:\n%s", exitDetail(first.err), first.stderr)
	}
	if !strings.Contains(first.stderr, "creating a new session with that id") {
		t.Errorf("first run stderr = %q, want a warning that the session is being created", first.stderr)
	}
	second := runPiWith(t, s, opts, "second-prompt")
	if second.err != nil {
		t.Fatalf("second run failed (%s)\nstderr:\n%s", exitDetail(second.err), second.stderr)
	}
	if second.stderr != "" {
		t.Errorf("second run stderr = %q, want empty (the session exists)", second.stderr)
	}

	for i, r := range []run{first, second} {
		if got := r.events[0]; got["type"] != "session" || got["id"] != id {
			t.Errorf("run %d first event = %v, want a session event with id %q", i+1, got, id)
		}
	}
	reqs := s.Requests()
	if len(reqs) != 2 {
		t.Fatalf("stub got %d requests, want 2", len(reqs))
	}
	if msgs := messagesJSON(t, reqs[0]); strings.Contains(msgs, "first-answer") || strings.Contains(msgs, "second-prompt") {
		t.Errorf("first request already carries later turns: %s", msgs)
	}
	msgs := messagesJSON(t, reqs[1])
	for _, want := range []string{"first-prompt", "first-answer", "second-prompt"} {
		if !strings.Contains(msgs, want) {
			t.Errorf("resumed request lacks %q: %s", want, msgs)
		}
	}

	files := sessionFiles(t, dirs.agentDir)
	if len(files) != 1 || !strings.HasSuffix(files[0], "_"+id+".jsonl") {
		t.Errorf("session files = %v, want exactly one ending _%s.jsonl", files, id)
	}
}

func TestPiSessionIDRejectsInvalid(t *testing.T) {
	s := newStub(t)
	r := runPiWith(t, s, piOpts{args: []string{"--model", stubModelRef, "--session-id", "not/valid"}}, "hi")
	if exitDetail(r.err) != "exit 1" {
		t.Errorf("pi exit = %v, want exit 1", exitDetail(r.err))
	}
	if !strings.Contains(r.stderr, "Session id must be") {
		t.Errorf("stderr = %q, want the session id validation error", r.stderr)
	}
	if len(r.events) != 0 || len(s.Requests()) != 0 {
		t.Errorf("got %d events and %d requests, want none", len(r.events), len(s.Requests()))
	}
}
