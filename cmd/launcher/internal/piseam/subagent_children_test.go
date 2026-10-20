//go:build integration

package piseam

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// writeAgentFile writes an agent file the way lib/drivers/pi.nix renders a
// roster entry: JSON-quoted frontmatter scalars, tools as one comma-joined
// string, and the model carrying its effort as a ":<level>" suffix.
func writeAgentFile(t *testing.T, agentDir, name, model, tools, body string) {
	t.Helper()
	dir := filepath.Join(agentDir, "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	q := func(s string) string { return fmt.Sprintf("%q", s) }
	file := "---\nname: " + q(name) + "\ndescription: " + q("stub "+name) + "\nmodel: " + q(model) + "\ntools: " + q(tools) + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
}

// usageReply is a text reply whose usage is the given prompt and completion
// tokens, so each child's usage differs from the others' and from the
// parent's.
func usageReply(text string, prompt, completion int) reply {
	r := textReply(text)
	last := r.chunks[len(r.chunks)-1]
	last["usage"] = map[string]any{
		"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion,
	}
	return r
}

// requestTask is the "Task: ..." brief a child's first user message opens
// with, or "" for a parent request, whose prompt is not a brief.
func requestTask(req map[string]any) string {
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		if mm, _ := m.(map[string]any); mm["role"] == "user" {
			if c := fmt.Sprint(mm["content"]); strings.Contains(c, "Task: ") {
				return c
			}
			return ""
		}
	}
	return ""
}

// subagentStub routes the parent's two turns (the subagent call, then the
// wrap-up) and each child's turns, the latter by the brief that child was
// given: a child request whose brief contains a key gets that key's next reply.
func subagentStub(t *testing.T, callJSON string, children map[string][]reply) *stub {
	t.Helper()
	var mu sync.Mutex
	next := map[string]int{}
	return newRoutedStub(t, func(req map[string]any) reply {
		task := requestTask(req)
		if task == "" {
			if len(toolRoleMessages(req)) == 0 {
				return toolCallReply("call_sub", "subagent", callJSON)
			}
			return textReply("parent done")
		}
		mu.Lock()
		defer mu.Unlock()
		for key, script := range children {
			if strings.Contains(task, key) {
				n := next[key]
				next[key]++
				if n >= len(script) {
					t.Errorf("stub: child %q asked for turn %d of %d", key, n+1, len(script))
					return statusReply(500, "child script exhausted")
				}
				return script[n]
			}
		}
		t.Errorf("stub: child brief %q matches no script", task)
		return statusReply(500, "no child script")
	})
}

// subagentResults returns details and details.results of the one subagent
// tool result.
func subagentResults(t *testing.T, r run) (map[string]any, []map[string]any) {
	t.Helper()
	ends := eventsOfType(r.events, "tool_execution_end")
	if len(ends) != 1 {
		t.Fatalf("got %d tool_execution_end events, want 1\nstdout:\n%s", len(ends), r.stdout)
	}
	res, _ := ends[0]["result"].(map[string]any)
	details, _ := res["details"].(map[string]any)
	var results []map[string]any
	raw, _ := details["results"].([]any)
	for _, e := range raw {
		m, _ := e.(map[string]any)
		results = append(results, m)
	}
	return details, results
}

func runSubagentPi(t *testing.T, s *stub, dirs piDirs, opts piOpts, seed ...func(work string)) run {
	t.Helper()
	linkSubagentExtension(t, piPath(t), dirs.agentDir)
	opts.reasoning = true
	opts.dirs = &dirs
	r := runPiWith(t, s, opts, "Delegate.", seed...)
	if r.err != nil {
		t.Fatalf("pi failed (%s)\nstderr:\n%s", exitDetail(r.err), r.stderr)
	}
	return r
}

type childWant struct {
	task          string
	input, output float64
}

// TestPiSubagentModes runs the extension's three modes against the stub
// Provider. Each child reports different usage, so a sum or a mixed-up child is
// visible; with SPINDRIFT_PI_CAPTURE_DIR set the parent's event stream is the
// corpus log for that mode.
func TestPiSubagentModes(t *testing.T) {
	const model = "stub/stub-model:high"
	for _, tc := range []struct {
		name, call string
		children   map[string][]reply
		want       []childWant
	}{
		{
			name:     "single",
			call:     `{"agent":"scout","task":"look at one"}`,
			children: map[string][]reply{"look at one": {usageReply("one found", 31, 9)}},
			want:     []childWant{{"look at one", 31, 9}},
		},
		{
			name: "parallel",
			call: `{"tasks":[{"agent":"scout","task":"look at alpha"},{"agent":"scout","task":"look at beta"}]}`,
			children: map[string][]reply{
				"look at alpha": {usageReply("alpha found", 41, 5)},
				"look at beta":  {usageReply("beta found", 53, 7)},
			},
			want: []childWant{{"look at alpha", 41, 5}, {"look at beta", 53, 7}},
		},
		{
			name: "chain",
			call: `{"chain":[{"agent":"scout","task":"first step"},{"agent":"scout","task":"second step after {previous}"}]}`,
			children: map[string][]reply{
				"first step":  {usageReply("first result", 61, 3)},
				"second step": {usageReply("second result", 71, 4)},
			},
			want: []childWant{{"first step", 61, 3}, {"second step after first result", 71, 4}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := subagentStub(t, tc.call, tc.children)
			dirs := newPiDirs(t)
			writeAgentFile(t, dirs.agentDir, "scout", model, "read, grep", "You are the scout.")
			r := runSubagentPi(t, s, dirs, piOpts{})

			details, results := subagentResults(t, r)
			if details["mode"] != tc.name {
				t.Errorf("details.mode = %v, want %s", details["mode"], tc.name)
			}
			if len(results) != len(tc.want) {
				t.Fatalf("details.results has %d entries, want %d", len(results), len(tc.want))
			}
			for i, want := range tc.want {
				res := results[i]
				if res["agent"] != "scout" || res["exitCode"] != float64(0) || res["model"] != model {
					t.Errorf("results[%d] agent/exitCode/model = %v/%v/%v, want scout/0/%s", i, res["agent"], res["exitCode"], res["model"], model)
				}
				if res["task"] != want.task {
					t.Errorf("results[%d].task = %v, want %q", i, res["task"], want.task)
				}
				if tc.name == "chain" {
					if res["step"] != float64(i+1) {
						t.Errorf("results[%d].step = %v, want %d", i, res["step"], i+1)
					}
				} else if _, has := res["step"]; has {
					t.Errorf("results[%d].step = %v, want absent outside a chain", i, res["step"])
				}
				// results[].usage is the extension's own per-child sum.
				u, _ := res["usage"].(map[string]any)
				if u["input"] != want.input || u["output"] != want.output {
					t.Errorf("results[%d].usage = %v, want input/output %v/%v", i, u, want.input, want.output)
				}
				// The same numbers sit on the child's own assistant message.
				msg := lastMessage(res)
				mu, _ := msg["usage"].(map[string]any)
				if msg["role"] != "assistant" || mu["input"] != want.input || mu["output"] != want.output {
					t.Errorf("results[%d] last message role/usage = %v/%v, want assistant with input/output %v/%v", i, msg["role"], mu, want.input, want.output)
				}
			}
		})
	}
}

// TestPiSubagentMultiRole runs a parallel call across two roster roles with
// different models and distinct usage, one of them making a tool call, so a
// per-role usage mix-up or a dropped child toolResult is visible; with
// SPINDRIFT_PI_CAPTURE_DIR set the parent's event stream is the corpus log.
func TestPiSubagentMultiRole(t *testing.T) {
	s := subagentStub(t,
		`{"tasks":[{"agent":"scout","task":"read the file"},{"agent":"auditor","task":"audit beta"}]}`,
		map[string][]reply{
			"read the file": {toolCallReply("call_read", "read", `{"path":"plain.txt"}`), usageReply("scout done", 41, 5)},
			"audit beta":    {usageReply("auditor done", 53, 7)},
		})
	dirs := newPiDirs(t)
	writeAgentFile(t, dirs.agentDir, "scout", "stub/stub-model:high", "read", "You are the scout.")
	writeAgentFile(t, dirs.agentDir, "auditor", "stub/stub-model:low", "read", "You are the auditor.")
	r := runSubagentPi(t, s, dirs, piOpts{}, seedFiles(t, map[string]string{"plain.txt": "plain contents"}))

	details, results := subagentResults(t, r)
	if details["mode"] != "parallel" {
		t.Errorf("details.mode = %v, want parallel", details["mode"])
	}
	if len(results) != 2 {
		t.Fatalf("details.results has %d entries, want 2", len(results))
	}
	for i, want := range []struct {
		agent, model, task string
		input, output      float64
	}{
		// Two turns: the tool-call turn carries the stub's default usage
		// (input 80, output 30, cacheRead 40).
		{"scout", "stub/stub-model:high", "read the file", 121, 35},
		{"auditor", "stub/stub-model:low", "audit beta", 53, 7},
	} {
		res := results[i]
		if res["agent"] != want.agent || res["model"] != want.model || res["exitCode"] != float64(0) {
			t.Errorf("results[%d] agent/model/exitCode = %v/%v/%v, want %s/%s/0", i, res["agent"], res["model"], res["exitCode"], want.agent, want.model)
		}
		if res["task"] != want.task {
			t.Errorf("results[%d].task = %v, want %q", i, res["task"], want.task)
		}
		u, _ := res["usage"].(map[string]any)
		if u["input"] != want.input || u["output"] != want.output {
			t.Errorf("results[%d].usage = %v, want input/output %v/%v", i, u, want.input, want.output)
		}
	}
	var toolResults int
	msgs, _ := results[0]["messages"].([]any)
	for _, m := range msgs {
		if mm, _ := m.(map[string]any); mm["role"] == "toolResult" && mm["isError"] != true {
			toolResults++
		}
	}
	if toolResults != 1 {
		t.Errorf("scout has %d clean toolResult messages, want 1: %v", toolResults, msgs)
	}
}

// TestPiSubagentChildLoadsHookBridge has a child read a planted credential: the
// bridge, installed in the agent dir the child inherits, must block the read
// inside the child's own process.
func TestPiSubagentChildLoadsHookBridge(t *testing.T) {
	const call = `{"agent":"scout","task":"read the file"}`
	// The planted credential file is named as the hooks name it.
	credFile := "." + "env"
	runRead := func(t *testing.T, path string) (run, *stub) {
		t.Helper()
		s := subagentStub(t, call, map[string][]reply{
			"read the file": {toolCallReply("call_read", "read", `{"path":"`+path+`"}`), textReply("child done")},
		})
		dirs := newPiDirs(t)
		writeAgentFile(t, dirs.agentDir, "scout", "stub/stub-model:high", "read", "You are the scout.")
		return runSubagentPi(t, s, dirs, piOpts{hooks: hooksDir(t)},
			seedFiles(t, map[string]string{credFile: planted, "plain.txt": planted})), s
	}

	t.Run("credential read is blocked in the child", func(t *testing.T) {
		r, s := runRead(t, credFile)
		_, results := subagentResults(t, r)
		if len(results) != 1 || results[0]["exitCode"] != float64(0) {
			t.Fatalf("results = %v, want one clean child", results)
		}
		var toolResult map[string]any
		msgs, _ := results[0]["messages"].([]any)
		for _, m := range msgs {
			if mm, _ := m.(map[string]any); mm["role"] == "toolResult" {
				toolResult = mm
			}
		}
		if toolResult == nil {
			t.Fatalf("child has no toolResult message: %v", msgs)
		}
		if toolResult["isError"] != true || !strings.Contains(fmt.Sprint(toolResult["content"]), "credential") {
			t.Errorf("child tool result = %v, want an error naming the credential block", toolResult)
		}
		if strings.Contains(r.stdout, planted) {
			t.Errorf("planted credential reached the parent's event stream")
		}
		for i, req := range s.Requests() {
			if strings.Contains(messagesJSON(t, req), planted) {
				t.Errorf("request %d carries the planted credential", i)
			}
		}
	})

	// Control: the same child reads a plain file, so the block above is the
	// bridge's doing and not a child that cannot read.
	t.Run("plain file is read in the child", func(t *testing.T) {
		r, _ := runRead(t, "plain.txt")
		if !strings.Contains(r.stdout, planted) {
			t.Errorf("child did not read the plain file; stdout:\n%s", r.stdout)
		}
	})
}

// TestPiSubagentChildIgnoresProjectFiles pins that --no-approve on the parent
// reaches the children: the extension spawns them without the flag, so a child
// must still ignore the Target repo's project-local .pi/.
func TestPiSubagentChildIgnoresProjectFiles(t *testing.T) {
	const (
		projectMarker = "PROJECT-APPEND-MARKER"
		extMarkerFile = "PROJECT-EXTENSION-LOADED"
	)
	s := subagentStub(t, `{"agent":"scout","task":"look around"}`, map[string][]reply{
		"look around": {textReply("looked")},
	})
	dirs := newPiDirs(t)
	writeAgentFile(t, dirs.agentDir, "scout", "stub/stub-model:high", "read", "You are the scout.")
	r := runSubagentPi(t, s, dirs, piOpts{}, seedFiles(t, map[string]string{
		".pi/APPEND_SYSTEM.md": projectMarker,
		".pi/extensions/marker.ts": `import { writeFileSync } from "node:fs";
export default function () {
	writeFileSync("` + extMarkerFile + `", "loaded");
}
`,
	}))
	if _, results := subagentResults(t, r); len(results) != 1 || results[0]["exitCode"] != float64(0) {
		t.Fatalf("results = %v, want one clean child", results)
	}
	reqs := s.Requests()
	if len(reqs) != 3 {
		t.Fatalf("stub got %d requests, want 3 (parent, child, parent)", len(reqs))
	}
	for i, req := range reqs {
		if strings.Contains(messagesJSON(t, req), projectMarker) {
			t.Errorf("request %d carries project-local %q: a process trusted the project", i, projectMarker)
		}
	}
	if _, err := os.Stat(filepath.Join(dirs.work, extMarkerFile)); err == nil {
		t.Error("a project extension ran: the child trusted the project")
	}
}

// TestPiSubagentRefusesProjectAgentScope pins that the roster stays user-level:
// agentScope is a model-supplied parameter and the extension's own confirm
// prompt needs a UI, so only the hook bridge keeps the Target repo's
// .pi/agents/*.md from overriding the baked roster.
func TestPiSubagentRefusesProjectAgentScope(t *testing.T) {
	const projectMarker = "PROJECT-AGENT-MARKER"
	plant := func(work string) {
		dir := filepath.Join(work, ".pi", "agents")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		file := "---\nname: scout\ndescription: project scout\nmodel: stub/project-model\ntools: read\n---\n" + projectMarker + "\n"
		if err := os.WriteFile(filepath.Join(dir, "scout.md"), []byte(file), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runScope := func(t *testing.T, scope string) (run, *stub) {
		t.Helper()
		s := subagentStub(t, `{"agent":"scout","task":"look around","agentScope":"`+scope+`"}`, map[string][]reply{
			"look around": {textReply("looked")},
		})
		dirs := newPiDirs(t)
		writeAgentFile(t, dirs.agentDir, "scout", "stub/stub-model:high", "read", "You are the scout.")
		return runSubagentPi(t, s, dirs, piOpts{hooks: hooksDir(t)}, plant), s
	}
	requireNoMarker := func(t *testing.T, s *stub) {
		t.Helper()
		for _, req := range s.Requests() {
			b, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), projectMarker) {
				t.Errorf("a request carries the project agent's %q", projectMarker)
				return
			}
		}
	}

	for _, scope := range []string{"project", "both"} {
		t.Run(scope+" is refused", func(t *testing.T) {
			r, s := runScope(t, scope)
			reqs := s.Requests()
			if len(reqs) != 2 {
				t.Fatalf("stub got %d requests, want 2 (parent, parent): no child may start\nstdout:\n%s", len(reqs), r.stdout)
			}
			tool := toolRoleMessages(reqs[1])
			if len(tool) != 1 || !strings.Contains(tool[0], "project-local") {
				t.Errorf("parent tool results = %v, want one error naming the project-local refusal", tool)
			}
			requireNoMarker(t, s)
		})
	}

	// Control: an explicit "user" scope still runs the baked scout, so the
	// refusals above are the bridge's doing and not a subagent that cannot run.
	t.Run("user runs the baked roster", func(t *testing.T) {
		r, s := runScope(t, "user")
		if _, results := subagentResults(t, r); len(results) != 1 || results[0]["exitCode"] != float64(0) {
			t.Fatalf("results = %v, want one clean child", results)
		}
		requireNoMarker(t, s)
	})
}
