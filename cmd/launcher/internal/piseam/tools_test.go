//go:build integration

package piseam

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// eventsOfType returns the events whose "type" is typ, in stream order.
func eventsOfType(events []map[string]any, typ string) []map[string]any {
	var out []map[string]any
	for _, ev := range events {
		if ev["type"] == typ {
			out = append(out, ev)
		}
	}
	return out
}

// resultText joins the text parts of a tool_execution_end result.
func resultText(ev map[string]any) string {
	res, _ := ev["result"].(map[string]any)
	parts, _ := res["content"].([]any)
	var sb strings.Builder
	for _, p := range parts {
		if m, _ := p.(map[string]any); m["type"] == "text" {
			sb.WriteString(fmt.Sprint(m["text"]))
		}
	}
	return sb.String()
}

// toolRoleMessages returns the content of every tool-role message in a request.
func toolRoleMessages(req map[string]any) []string {
	var out []string
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		if mm, _ := m.(map[string]any); mm["role"] == "tool" {
			out = append(out, fmt.Sprint(mm["content"]))
		}
	}
	return out
}

func TestPiToolCall(t *testing.T) {
	const content = "tool-call-fixture-content"
	for _, tc := range []struct {
		name, tool, argsJSON string
		wantArgs             map[string]string
		wantResult           string
	}{
		{
			name: "read", tool: "read", argsJSON: `{"path":"fixture.txt"}`,
			wantArgs:   map[string]string{"path": "fixture.txt"},
			wantResult: content,
		},
		{
			name: "bash", tool: "bash", argsJSON: `{"command":"echo hi-from-bash"}`,
			wantArgs:   map[string]string{"command": "echo hi-from-bash"},
			wantResult: "hi-from-bash",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const final = "Tool done."
			s := newStub(t, toolCallReply("call_1", tc.tool, tc.argsJSON), textReply(final))
			r := runPi(t, s, "Use the tool.", func(work string) {
				if err := os.WriteFile(filepath.Join(work, "fixture.txt"), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			})
			if r.err != nil {
				t.Fatalf("pi failed (%s)\nstderr:\n%s\nstdout:\n%s", exitDetail(r.err), r.stderr, r.stdout)
			}

			starts := eventsOfType(r.events, "tool_execution_start")
			ends := eventsOfType(r.events, "tool_execution_end")
			if len(starts) != 1 || len(ends) != 1 {
				t.Fatalf("got %d tool_execution_start and %d tool_execution_end, want 1 each\nstdout:\n%s", len(starts), len(ends), r.stdout)
			}
			for _, ev := range []map[string]any{starts[0], ends[0]} {
				if ev["toolCallId"] != "call_1" || ev["toolName"] != tc.tool {
					t.Errorf("%v: toolCallId/toolName = %v/%v, want call_1/%s", ev["type"], ev["toolCallId"], ev["toolName"], tc.tool)
				}
			}
			args, _ := starts[0]["args"].(map[string]any)
			for k, v := range tc.wantArgs {
				if args[k] != v {
					t.Errorf("tool_execution_start args[%q] = %v, want %q", k, args[k], v)
				}
			}
			if ends[0]["isError"] != false {
				t.Errorf("tool_execution_end isError = %v, want false", ends[0]["isError"])
			}
			if got := resultText(ends[0]); !strings.Contains(got, tc.wantResult) {
				t.Errorf("tool result = %q, want it to contain %q", got, tc.wantResult)
			}

			reqs := s.Requests()
			if len(reqs) != 2 {
				t.Fatalf("stub got %d requests, want 2", len(reqs))
			}
			tm := toolRoleMessages(reqs[1])
			if len(tm) != 1 || !strings.Contains(tm[0], tc.wantResult) {
				t.Errorf("second request tool messages = %q, want one containing %q", tm, tc.wantResult)
			}
			if got := finalAssistantText(r.events); got != final {
				t.Errorf("final assistant text = %q, want %q", got, final)
			}
		})
	}
}

func TestPiRetriesOverload(t *testing.T) {
	const final = "Recovered after retry."
	s := newStub(t,
		statusReply(429, `{"error":{"message":"Rate limit reached, try again later","type":"rate_limit_error","code":"rate_limit_exceeded"}}`),
		textReply(final))

	r := runPi(t, s, "Say hello.")
	if r.err != nil {
		t.Fatalf("pi failed (%s)\nstderr:\n%s\nstdout:\n%s", exitDetail(r.err), r.stderr, r.stdout)
	}
	starts := eventsOfType(r.events, "auto_retry_start")
	ends := eventsOfType(r.events, "auto_retry_end")
	if len(starts) != 1 || len(ends) != 1 {
		t.Fatalf("got %d auto_retry_start and %d auto_retry_end, want 1 each\nstdout:\n%s", len(starts), len(ends), r.stdout)
	}
	if starts[0]["attempt"] != float64(1) {
		t.Errorf("auto_retry_start attempt = %v, want 1", starts[0]["attempt"])
	}
	if ends[0]["success"] != true {
		t.Errorf("auto_retry_end success = %v, want true", ends[0]["success"])
	}
	if n := len(s.Requests()); n != 2 {
		t.Errorf("stub got %d requests, want 2", n)
	}
	if got := finalAssistantText(r.events); got != final {
		t.Errorf("final assistant text = %q, want %q", got, final)
	}
}

func TestPiOutcomeLine(t *testing.T) {
	const outcome = "SPINDRIFT_OUTCOME issue=1 landing=agent/issue-1 status=ready note=stub"
	s := newStub(t, textReply("All work is finished.\n"+outcome))

	r := runPi(t, s, "Finish and report.")
	if r.err != nil {
		t.Fatalf("pi failed (%s)\nstderr:\n%s\nstdout:\n%s", exitDetail(r.err), r.stderr, r.stdout)
	}
	var found bool
	for _, line := range strings.Split(finalAssistantText(r.events), "\n") {
		found = found || line == outcome
	}
	if !found {
		t.Errorf("final assistant text lacks %q on its own line:\n%s", outcome, finalAssistantText(r.events))
	}
	last := r.events[len(r.events)-1]
	if last["type"] != "agent_settled" || last["aborted"] != false {
		t.Errorf("last event = %v, want agent_settled with aborted=false", last)
	}
}
