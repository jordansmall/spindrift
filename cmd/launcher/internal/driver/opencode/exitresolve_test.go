package opencode_test

import (
	"path/filepath"
	"testing"

	"spindrift.dev/launcher/internal/driver/opencode"
)

const outcomeLine = `{"type":"text","part":{"text":"SPINDRIFT_OUTCOME issue=42 landing=https://example/pr/1 status=ready note=done"}}`

const errLine = `{"type":"error","error":"boom"}`

func TestResolveExit(t *testing.T) {
	cases := []struct {
		name     string
		lines    []string
		exitCode int
		want     int
	}{
		{"clean exit, outcome, no error", []string{outcomeLine}, 0, 0},
		{"clean exit, no outcome line", []string{`{"type":"text","part":{"text":"Investigating."}}`}, 0, 0},
		{"clean exit, error event", []string{errLine}, 0, 1},
		{"clean exit, outcome plus error event", []string{outcomeLine, errLine}, 0, 1},
		{"clean exit, error event with non-integer timestamp", []string{`{"type":"error","timestamp":"soon","error":"boom"}`}, 0, 1},
		{"clean exit, error event with fractional timestamp", []string{`{"type":"error","timestamp":1.5,"error":"boom"}`}, 0, 1},
		{"clean exit, error event with malformed token count", []string{`{"type":"error","part":{"tokens":{"input":1.5}},"error":"boom"}`}, 0, 1},
		{"clean exit, error event with undecodable part.text is dropped", []string{`{"type":"error","part":{"text":5},"error":"boom"}`}, 0, 0},
		{"clean exit, error then text is recovered", []string{errLine, `{"type":"text","part":{"text":"Retrying."}}`}, 0, 0},
		{"clean exit, error then step_start is recovered", []string{errLine, `{"type":"step_start","part":{"messageID":"msg_2"}}`}, 0, 0},
		{"clean exit, error then completed tool call is recovered", []string{errLine, `{"type":"tool_use","part":{"callID":"c1","tool":"bash","state":{"status":"completed","input":{"command":"ls"},"output":"x"}}}`}, 0, 0},
		{"clean exit, error then step_finish only is unrecovered", []string{errLine, `{"type":"step_finish","part":{"messageID":"msg_1","tokens":{"input":1,"output":1}}}`}, 0, 1},
		{"clean exit, recovered error then later unrecovered error", []string{errLine, `{"type":"text","part":{"text":"Retrying."}}`, errLine}, 0, 1},
		{"crash after outcome keeps rc", []string{outcomeLine}, 17, 17},
		{"non-zero rc without outcome keeps rc", []string{`{"type":"text","part":{"text":"x"}}`}, 3, 3},
		{"non-zero rc with error event keeps rc", []string{errLine}, 3, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := opencode.ResolveExit(opencode.WriteLog(t, tc.lines...), tc.exitCode)
			if err != nil {
				t.Fatalf("ResolveExit: %v", err)
			}
			if got != tc.want {
				t.Errorf("code: got %d, want %d", got, tc.want)
			}
		})
	}
}

// A missing log means no error event was observed, so the exit code passes
// through, as with claude.
func TestResolveExit_MissingFile_PassesExitCodeThrough(t *testing.T) {
	for _, rc := range []int{0, 7} {
		got, err := opencode.ResolveExit(filepath.Join(t.TempDir(), "does-not-exist.log"), rc)
		if err != nil {
			t.Fatalf("ResolveExit: %v", err)
		}
		if got != rc {
			t.Errorf("rc %d: got %d, want it passed through", rc, got)
		}
	}
}
