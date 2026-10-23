//go:build integration

package piseam

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// BridgeEnv and HooksEnv name the hook bridge extension and the baked
	// hooks directory it runs. The nix seam check sets both, since only
	// cmd/launcher is copied into its sandbox and the hooks there need
	// store-pinned shebangs; unset, the repo's own agent/ directory serves.
	BridgeEnv = "SPINDRIFT_PI_BRIDGE"
	HooksEnv  = "SPINDRIFT_PI_HOOKS_DIR"

	// planted is the content of a credential file the hooks must keep from the model.
	planted = "planted-credential-content"
)

func repoAgentDir() string { return filepath.Join("..", "..", "..", "..", "agent") }

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// hooksDir is the baked hooks directory, or a copy of the repo's hooks with
// their shebangs pinned to this host's bash: a Box has no /usr/bin/env, the way
// lib/agent-hooks.nix pins them for the image.
func hooksDir(t *testing.T) string {
	t.Helper()
	if v := os.Getenv(HooksEnv); v != "" {
		return v
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	entries, err := os.ReadDir(repoAgentDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(repoAgentDir(), e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		b = []byte(strings.Replace(string(b), "#!/usr/bin/env bash", "#!"+bash, 1))
		if err := os.WriteFile(filepath.Join(dst, e.Name()), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dst
}

// bridgeSource reads the bridge under test, which BridgeEnv can redirect.
func bridgeSource(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(envOr(BridgeEnv, filepath.Join(repoAgentDir(), "pi-hook-bridge.js")))
	if err != nil {
		t.Fatal(err)
	}
	return string(src)
}

// installBridge writes the bridge into the agent dir's extensions, pointing it
// at hooksDir. The bridge keeps its hooks directory on one line so a test can
// retarget it, the way the opencode guards check does.
func installBridge(t *testing.T, agentDir, hooks string) {
	t.Helper()
	abs, err := filepath.Abs(hooks)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(bridgeSource(t), "\n")
	var replaced bool
	for i, line := range lines {
		if strings.HasPrefix(line, "const HOOKS_DIR = ") {
			lines[i] = `const HOOKS_DIR = "` + abs + `";`
			replaced = true
		}
	}
	if !replaced {
		t.Fatal("bridge has no `const HOOKS_DIR = ` line to retarget")
	}
	ext := filepath.Join(agentDir, "extensions")
	if err := os.MkdirAll(ext, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ext, "spindrift-hooks.js"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}

// hooksWith copies the hooks directory and replaces one script's body, which
// is how a hook that fails or answers garbage is made.
func hooksWith(t *testing.T, script, body string) string {
	t.Helper()
	dst := t.TempDir()
	if out, err := exec.Command("cp", "-r", hooksDir(t)+"/.", dst).CombinedOutput(); err != nil {
		t.Fatalf("copy hooks: %v\n%s", err, out)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dst, script)
	// The copy may be read-only (store files), so replace rather than write through.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!"+sh+"\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dst
}

// toolOutcome is what one scripted tool call came to, seen from pi's event
// stream and from the stub Provider's next request.
type toolOutcome struct {
	isError  bool
	result   string // the tool result in pi's event stream
	modelSaw string // the tool message the model got back
	work     string
}

// runHooked has the stub Provider call one tool with argsJSON under the hook
// bridge and reports what happened. A command can leave a file in the work
// dir, which is how a test sees that a blocked command never ran.
func runHooked(t *testing.T, hooks, tool, argsJSON string, env []string, seed map[string]string) toolOutcome {
	t.Helper()
	s := newStub(t, toolCallReply("call_1", tool, argsJSON), textReply("done"))
	dirs := newPiDirs(t)
	r := runPiWith(t, s, piOpts{hooks: hooks, env: env, dirs: &dirs}, "Use the tool.", seedFiles(t, seed))
	if r.err != nil {
		t.Fatalf("pi failed (%s)\nstderr:\n%s\nstdout:\n%s", exitDetail(r.err), r.stderr, r.stdout)
	}
	ends := eventsOfType(r.events, "tool_execution_end")
	reqs := s.Requests()
	if len(ends) != 1 || len(reqs) != 2 {
		t.Fatalf("got %d tool_execution_end and %d requests, want 1 and 2\nstderr:\n%s\nstdout:\n%s", len(ends), len(reqs), r.stderr, r.stdout)
	}
	tm := toolRoleMessages(reqs[1])
	if len(tm) != 1 {
		t.Fatalf("second request has tool messages %q, want exactly one", tm)
	}
	return toolOutcome{isError: ends[0]["isError"] == true, result: resultText(ends[0]), modelSaw: tm[0], work: dirs.work}
}

func (o toolOutcome) ran(marker string) bool {
	_, err := os.Stat(filepath.Join(o.work, marker))
	return err == nil
}

func TestPiHooksBlockCredentialReads(t *testing.T) {
	seed := map[string]string{".env": planted}
	for _, tc := range []struct{ name, tool, argsJSON string }{
		{"read tool", "read", `{"path":".env"}`},
		{"bash", "bash", `{"command":"cat .env"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := runHooked(t, hooksDir(t), tc.tool, tc.argsJSON, nil, seed)
			if !o.isError {
				t.Errorf("credential read was not an error; result = %q", o.result)
			}
			for _, seen := range []string{o.result, o.modelSaw} {
				if strings.Contains(seen, planted) {
					t.Errorf("credential content reached the model: %q", seen)
				}
				if !strings.Contains(seen, "credential") {
					t.Errorf("block reason missing from %q", seen)
				}
			}
		})
	}
	// Control: the same read of a plain file succeeds, so the blocks above are
	// the hook's doing and not a fixture pi cannot read.
	t.Run("plain file is read", func(t *testing.T) {
		o := runHooked(t, hooksDir(t), "read", `{"path":"plain.txt"}`, nil, map[string]string{"plain.txt": planted})
		if o.isError || !strings.Contains(o.modelSaw, planted) {
			t.Errorf("plain read isError = %v, model saw %q, want its content", o.isError, o.modelSaw)
		}
	})
}

func TestPiHooksScrubCredentialVars(t *testing.T) {
	const secret = "sekrit-token-value"
	o := runHooked(t, hooksDir(t), "bash", `{"command":"echo \"key=[$ANTHROPIC_API_KEY] other=[$KEEP_ME]\""}`,
		[]string{"ANTHROPIC_API_KEY=" + secret, "KEEP_ME=kept"}, nil)
	if o.isError {
		t.Fatalf("scrubbed command failed: %q", o.result)
	}
	if strings.Contains(o.result, secret) || strings.Contains(o.modelSaw, secret) {
		t.Errorf("credential variable leaked: tool result %q, model saw %q", o.result, o.modelSaw)
	}
	// The variable is unset, not the whole environment.
	if !strings.Contains(o.result, "key=[] other=[kept]") {
		t.Errorf("tool result = %q, want the credential unset and other variables kept", o.result)
	}
}

func TestPiHooksRejectBackgroundBash(t *testing.T) {
	o := runHooked(t, hooksDir(t), "bash", `{"command":"touch ran.marker & wait"}`, nil, nil)
	if !o.isError {
		t.Errorf("backgrounded command was not an error; result = %q", o.result)
	}
	if !strings.Contains(o.result, "background") {
		t.Errorf("block reason missing from %q", o.result)
	}
	if o.ran("ran.marker") {
		t.Error("backgrounded command ran despite the block")
	}
}

func TestPiHooksSummariseLongOutput(t *testing.T) {
	teeDir := t.TempDir()
	env := []string{"BASH_OUTPUT_TEE_DIR=" + teeDir, "BASH_OUTPUT_SUMMARY_TAIL_BYTES=64"}
	o := runHooked(t, hooksDir(t), "bash", `{"command":"seq 1 2000"}`, env, nil)
	if o.isError {
		t.Fatalf("command failed: %q", o.result)
	}

	logs, err := filepath.Glob(filepath.Join(teeDir, "bash-*.log"))
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("tee logs = %q, want exactly one", logs)
	}
	full, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(full), "1\n2\n3\n") || !strings.HasSuffix(string(full), "\n1999\n2000\n") {
		t.Errorf("log does not hold the full output: %d bytes", len(full))
	}

	for _, seen := range []string{o.result, o.modelSaw} {
		if !strings.Contains(seen, logs[0]) || !strings.Contains(seen, "\n2000") {
			t.Errorf("summary lacks the log path or the output tail: %q", seen)
		}
		if strings.Contains(seen, "\n1000\n") || len(seen) > len(full)/4 {
			t.Errorf("model saw %d bytes, not a bounded summary of the %d-byte output", len(seen), len(full))
		}
	}
}

func TestPiHooksFailClosed(t *testing.T) {
	for _, tc := range []struct{ name, script, body string }{
		{"guard exits nonzero", "credential-deny.sh", "exit 1"},
		{"guard prints malformed output", "credential-deny.sh", "echo not-json"},
		{"rewriter exits nonzero", "env-credential-scrub.sh", "exit 1"},
		{"rewriter prints malformed output", "bash-output-tee.sh", "echo not-json"},
		{"rewriter prints no rewrite", "env-credential-scrub.sh", "exit 0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hooks := hooksWith(t, tc.script, tc.body)
			o := runHooked(t, hooks, "bash", `{"command":"touch ran.marker"}`, nil, nil)
			if !o.isError {
				t.Errorf("call was allowed through; result = %q", o.result)
			}
			if !strings.Contains(o.result, tc.script) {
				t.Errorf("failure does not name the hook %s: %q", tc.script, o.result)
			}
			if o.ran("ran.marker") {
				t.Error("command ran despite the failing hook")
			}
		})
	}
	t.Run("read tool", func(t *testing.T) {
		hooks := hooksWith(t, "credential-deny.sh", "exit 1")
		o := runHooked(t, hooks, "read", `{"path":"plain.txt"}`, nil, map[string]string{"plain.txt": planted})
		if !o.isError || strings.Contains(o.modelSaw, planted) {
			t.Errorf("read isError = %v, model saw %q, want it blocked", o.isError, o.modelSaw)
		}
	})
}

// The summary hook runs after the call has, so a broken one fails open: the
// model gets the command's own output, not a refusal.
func TestPiHooksSummaryFailsOpen(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"summary exits nonzero", "exit 1"},
		{"summary prints malformed output", "echo not-json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hooks := hooksWith(t, "bash-output-summary.sh", tc.body)
			env := []string{"BASH_OUTPUT_TEE_DIR=" + t.TempDir()}
			o := runHooked(t, hooks, "bash", `{"command":"echo kept-output; touch ran.marker"}`, env, nil)
			if o.isError {
				t.Errorf("call was blocked; result = %q", o.result)
			}
			if !o.ran("ran.marker") {
				t.Error("command did not run")
			}
			for _, seen := range []string{o.result, o.modelSaw} {
				if !strings.Contains(seen, "kept-output") {
					t.Errorf("model lost the unsummarised output: %q", seen)
				}
			}
		})
	}
}

// The bridge's load-time throw is the only guard against a hook naming a tool it
// cannot map, so pi must treat the failed load as fatal and not start without it.
func TestPiHooksUnmappedToolRefusesStart(t *testing.T) {
	src := bridgeSource(t)
	const row = `{ script: "credential-deny.sh", tools: ["read", "bash"`
	if !strings.Contains(src, row) {
		t.Fatalf("bridge has no %q row to add an unmapped tool to", row)
	}
	mutated := strings.Replace(src, row, row+`, "no-such-tool"`, 1)
	bridge := filepath.Join(t.TempDir(), "pi-hook-bridge.js")
	if err := os.WriteFile(bridge, []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(BridgeEnv, bridge)

	s := newStub(t, textReply("done"))
	r := runPiWith(t, s, piOpts{hooks: hooksDir(t)}, "Say done.")
	if r.err == nil {
		t.Errorf("pi started with a bridge that failed to load\nstdout:\n%s", r.stdout)
	}
	if !strings.Contains(r.stderr, "no CLAUDE_TOOLS entry") {
		t.Errorf("stderr does not carry the bridge's load error:\n%s", r.stderr)
	}
	if n := len(s.Requests()); n != 0 {
		t.Errorf("model received %d requests, want 0", n)
	}
}

// Project trust and the bridge are independent: with the bridge installed, a
// planted project extension still stays unloaded, AGENTS.md is still read, and
// the bridge still guards the call.
func TestPiHooksWithProjectTrust(t *testing.T) {
	const (
		extMarkerFile = "PROJECT-EXTENSION-LOADED"
		contextMarker = "CONTEXT-MARKER-WITH-BRIDGE"
	)
	s := newStub(t, toolCallReply("call_1", "read", `{"path":".env"}`), textReply("done"))
	dirs := newPiDirs(t)
	seed := map[string]string{
		".env":      planted,
		"AGENTS.md": contextMarker,
		".pi/extensions/marker.ts": `import { writeFileSync } from "node:fs";
export default function () {
	writeFileSync("` + extMarkerFile + `", "loaded");
}
`,
	}
	r := runPiWith(t, s, piOpts{hooks: hooksDir(t), dirs: &dirs}, "Use the tool.", seedFiles(t, seed))
	if r.err != nil {
		t.Fatalf("pi failed (%s)\nstderr:\n%s\nstdout:\n%s", exitDetail(r.err), r.stderr, r.stdout)
	}
	reqs := s.Requests()
	if len(reqs) != 2 {
		t.Fatalf("got %d requests, want 2\nstderr:\n%s", len(reqs), r.stderr)
	}
	if _, err := os.Stat(filepath.Join(dirs.work, extMarkerFile)); err == nil {
		t.Error("project extension ran despite --no-approve")
	}
	if sys := systemPrompt(reqs[0]); !strings.Contains(sys, contextMarker) {
		t.Errorf("system prompt lacks AGENTS.md content %q", contextMarker)
	}
	tm := toolRoleMessages(reqs[1])
	if len(tm) != 1 || strings.Contains(tm[0], planted) || !strings.Contains(tm[0], "credential") {
		t.Errorf("tool messages = %q, want the credential read blocked", tm)
	}
}
