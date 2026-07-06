package heartbeat

import (
	"encoding/json"
	"testing"
)

func TestToolToPhase(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"Read", `{"file_path":"main.go"}`, "explore"},
		{"Grep", `{"pattern":"foo"}`, "explore"},
		{"Glob", `{"pattern":"*.go"}`, "explore"},
		{"WebSearch", `{"query":"go test"}`, "explore"},
		{"WebFetch", `{"url":"https://example.com"}`, "explore"},
		{"Agent", `{}`, "explore"},
		{"Edit", `{"file_path":"main.go"}`, "edit"},
		{"Write", `{"file_path":"main.go"}`, "edit"},
		{"NotebookEdit", `{}`, "edit"},
		{"Bash", `{"command":"go test ./..."}`, "test"},
		{"Bash", `{"command":"go test ./internal/... > /tmp/out 2>&1"}`, "test"},
		{"Bash", `{"command":"git commit -m 'fix: something'"}`, "commit"},
		{"Bash", `{"command":"git commit --amend"}`, "commit"},
		{"Bash", `{"command":"ls -la"}`, "explore"},
		{"Bash", `{"command":"find . -name '*.go'"}`, "explore"},
		{"Bash", `{"command":"gh issue view 1"}`, "explore"},
	}
	for _, tc := range cases {
		got := toolToPhase(tc.name, json.RawMessage(tc.input))
		if got != tc.want {
			t.Errorf("toolToPhase(%q, %s) = %q, want %q", tc.name, tc.input, got, tc.want)
		}
	}
}
