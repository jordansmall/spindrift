//go:build integration

package piseam

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// linkSubagentExtension links the subagent extension shipped beside the pi
// binary into the agent dir, as ADR 0009's pi Driver plans to. The extension spawns its
// children from process.argv[1] and process.execPath, so they run the same pi
// with the same environment and need nothing on PATH.
func linkSubagentExtension(t *testing.T, bin, agentDir string) {
	t.Helper()
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		t.Fatal(err)
	}
	// bin/pi -> <out>/lib/node_modules/pi-monorepo/examples/extensions/subagent
	src := filepath.Join(filepath.Dir(filepath.Dir(real)), "lib", "node_modules", "pi-monorepo", "examples", "extensions", "subagent")
	if _, err := os.Stat(filepath.Join(src, "index.ts")); err != nil {
		t.Fatalf("subagent extension not found beside %s: %v", bin, err)
	}
	if err := os.MkdirAll(filepath.Join(agentDir, "extensions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(src, filepath.Join(agentDir, "extensions", "subagent")); err != nil {
		t.Fatal(err)
	}
}

func TestPiSubagent(t *testing.T) {
	const (
		agentBody     = "You are the scout. Report tersely."
		task          = "find the thing"
		projectMarker = "PROJECT-APPEND-MARKER"
	)
	childReply := textReply("child answer")
	childReply.chunks = append(childReply.chunks, map[string]any{
		"id": "chatcmpl-stub", "object": "chat.completion.chunk", "created": 0, "model": stubModel,
		"choices": []map[string]any{},
		"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
	})
	s := newStub(t,
		toolCallReply("call_sub", "subagent", `{"agent":"scout","task":"`+task+`"}`),
		childReply,
		textReply("parent done"))
	dirs := newPiDirs(t)
	linkSubagentExtension(t, piPath(t), dirs.agentDir)
	if err := os.MkdirAll(filepath.Join(dirs.agentDir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	agent := "---\nname: scout\ndescription: stub scout\nmodel: stub/stub-model:high\n---\n" + agentBody + "\n"
	if err := os.WriteFile(filepath.Join(dirs.agentDir, "agents", "scout.md"), []byte(agent), 0o644); err != nil {
		t.Fatal(err)
	}

	r := runPiWith(t, s, piOpts{reasoning: true, dirs: &dirs}, "Delegate to the scout.",
		seedFiles(t, map[string]string{".pi/APPEND_SYSTEM.md": projectMarker}))
	if r.err != nil {
		t.Fatalf("pi failed (%s)\nstderr:\n%s", exitDetail(r.err), r.stderr)
	}

	starts := eventsOfType(r.events, "tool_execution_start")
	ends := eventsOfType(r.events, "tool_execution_end")
	updates := eventsOfType(r.events, "tool_execution_update")
	if len(starts) != 1 || len(ends) != 1 {
		t.Fatalf("got %d tool_execution_start and %d tool_execution_end, want 1 each\nstdout:\n%s", len(starts), len(ends), r.stdout)
	}
	if starts[0]["toolName"] != "subagent" || starts[0]["toolCallId"] != "call_sub" {
		t.Errorf("tool_execution_start toolName/toolCallId = %v/%v, want subagent/call_sub", starts[0]["toolName"], starts[0]["toolCallId"])
	}

	// The child's messages stream through partial tool updates, not only the
	// final result: both carry details.results[0].messages.
	if len(updates) == 0 {
		t.Fatal("no tool_execution_update events: the child's messages do not stream to the parent")
	}
	endIdx := slices.IndexFunc(r.events, func(ev map[string]any) bool { return ev["type"] == "tool_execution_end" })
	streamedBeforeEnd := false
	for _, ev := range r.events[:endIdx] {
		if ev["type"] != "tool_execution_update" {
			continue
		}
		pr, _ := ev["partialResult"].(map[string]any)
		details, _ := pr["details"].(map[string]any)
		results, _ := details["results"].([]any)
		if len(results) == 0 {
			continue
		}
		if first, _ := results[0].(map[string]any); lastMessage(first) != nil {
			streamedBeforeEnd = true
		}
	}
	if !streamedBeforeEnd {
		t.Error("no tool_execution_update before tool_execution_end carries the child's messages")
	}
	partial := subagentResult(t, updates[len(updates)-1]["partialResult"])
	if got := lastMessage(partial); got["role"] != "assistant" {
		t.Errorf("last partial message role = %v, want assistant", got["role"])
	}

	res := subagentResult(t, ends[0]["result"])
	if res["agent"] != "scout" || res["exitCode"] != float64(0) {
		t.Errorf("result agent/exitCode = %v/%v, want scout/0", res["agent"], res["exitCode"])
	}
	// The extension reports the agent file's model verbatim, suffix included.
	if res["model"] != "stub/stub-model:high" {
		t.Errorf("result model = %v, want the agent file's stub/stub-model:high", res["model"])
	}
	msg := lastMessage(res)
	if msg["role"] != "assistant" || msg["model"] != stubModel {
		t.Errorf("child last message role/model = %v/%v, want assistant/%s", msg["role"], msg["model"], stubModel)
	}
	// The suffix took effect: the child's own message reports it.
	if msg["thinkingLevel"] != "high" {
		t.Errorf("child message thinkingLevel = %v, want high", msg["thinkingLevel"])
	}
	usage, _ := msg["usage"].(map[string]any)
	if usage["input"] != float64(11) || usage["output"] != float64(7) || usage["totalTokens"] != float64(18) {
		t.Errorf("child message usage = %v, want input 11, output 7, totalTokens 18", usage)
	}

	reqs := s.Requests()
	if len(reqs) != 3 {
		t.Fatalf("stub got %d requests, want 3 (parent, child, parent)", len(reqs))
	}
	if reqs[0]["reasoning_effort"] != nil || reqs[2]["reasoning_effort"] != nil {
		t.Errorf("parent reasoning_effort = %v/%v, want absent (--thinking off)", reqs[0]["reasoning_effort"], reqs[2]["reasoning_effort"])
	}
	if reqs[1]["reasoning_effort"] != "high" {
		t.Errorf("child reasoning_effort = %v, want high from the model suffix", reqs[1]["reasoning_effort"])
	}
	// A reasoning model's system prompt travels as a "developer" message, so
	// match on the whole message list.
	if got := messagesJSON(t, reqs[1]); !strings.Contains(got, agentBody) || !strings.Contains(got, "Task: "+task) {
		t.Errorf("child request lacks the agent body or task:\n%s", got)
	}
	// Neither process was told --approve, so neither reads .pi/ project files.
	for i, req := range reqs {
		if strings.Contains(messagesJSON(t, req), projectMarker) {
			t.Errorf("request %d carries project-local %q despite no --approve", i, projectMarker)
		}
	}
}

// subagentResult returns details.results[0] of a subagent tool result.
func subagentResult(t *testing.T, result any) map[string]any {
	t.Helper()
	res, _ := result.(map[string]any)
	details, _ := res["details"].(map[string]any)
	results, _ := details["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("details.results has %d entries, want 1: %v", len(results), res)
	}
	first, _ := results[0].(map[string]any)
	return first
}

// lastMessage returns the last entry of a subagent result's messages array;
// unlike lastAssistantMessage, it reads result.messages, not the event stream.
func lastMessage(result map[string]any) map[string]any {
	msgs, _ := result["messages"].([]any)
	if len(msgs) == 0 {
		return nil
	}
	m, _ := msgs[len(msgs)-1].(map[string]any)
	return m
}
