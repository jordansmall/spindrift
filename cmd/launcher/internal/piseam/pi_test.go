//go:build integration

package piseam

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/seamtest"
)

const (
	// BinEnv names the pi binary to test; when unset the locked one is built
	// with nix.
	BinEnv = "SPINDRIFT_PI_BIN"
	// CaptureEnv names a directory that receives each run's raw event stream.
	CaptureEnv = "SPINDRIFT_PI_CAPTURE_DIR"

	stubProvider = "stub"
	stubModel    = "stub-model"
	// stubModelRef is the --model value selecting the stub Provider's model.
	stubModelRef = stubProvider + "/" + stubModel
)

type piBin struct {
	path, skipReason string
	err              error
}

var lockedPi = sync.OnceValue(func() piBin {
	// --inputs-from pins nixpkgs to the flake's lock, not the registry's.
	out, skip, err := seamtest.ResolveBuild(BinEnv, "a pi binary", func(root string) []string {
		return []string{"--inputs-from", root, "nixpkgs#pi-coding-agent"}
	})
	if err != nil || skip != "" {
		return piBin{skipReason: skip, err: err}
	}
	return piBin{path: filepath.Join(out, "bin", "pi")}
})

func piPath(t *testing.T) string {
	t.Helper()
	if v := os.Getenv(BinEnv); v != "" {
		// pi runs with cmd.Dir set to a temp dir, so a relative path would break.
		abs, err := filepath.Abs(v)
		if err != nil {
			t.Fatalf("resolve %s=%q: %v", BinEnv, v, err)
		}
		return abs
	}
	res := lockedPi()
	switch {
	case res.skipReason != "":
		t.Skip(res.skipReason)
	case res.err != nil:
		t.Fatalf("piseam: %v", res.err)
	}
	return res.path
}

type run struct {
	events []map[string]any
	stdout string
	stderr string
	// argv is the full command line pi was launched with.
	argv []string
	err  error
}

// runPi writes a per-test agent dir pointing a custom Provider at the stub,
// runs pi once in JSON mode and parses the stream. A nonzero exit is returned
// in run.err rather than failing the test; JSON-mode failed turns exit zero,
// so callers read the events too. seedWork prepares the work dir first.
func runPi(t *testing.T, s *stub, prompt string, seedWork ...func(work string)) run {
	t.Helper()
	return runPiWith(t, s, piOpts{}, prompt, seedWork...)
}

// piDirs are the directories one pi invocation sees; reusing them across runs
// is what lets a second run find the first run's session.
type piDirs struct{ home, agentDir, work string }

func newPiDirs(t *testing.T) piDirs {
	t.Helper()
	return piDirs{home: t.TempDir(), agentDir: t.TempDir(), work: t.TempDir()}
}

// piOpts varies a runPiWith invocation; the zero value is runPi's.
type piOpts struct {
	// args replace the default "--model stub/stub-model --thinking off".
	args []string
	// reasoning marks the stub model as a reasoning model in models.json.
	reasoning bool
	// noAPIKey leaves apiKey out of the stub Provider, so a key can only come
	// from auth.json.
	noAPIKey bool
	// approve trusts project-local files (--approve) instead of ignoring them.
	approve bool
	// defaultAgentDir leaves PI_CODING_AGENT_DIR unset, so pi uses
	// $HOME/.pi/agent; dirs.agentDir must be that path for the config to land.
	defaultAgentDir bool
	// dirs, when set, are reused instead of fresh temp dirs.
	dirs *piDirs
	// hooks, when set, installs the hook bridge extension into the agent dir
	// with that directory as the hooks it runs.
	hooks string
	// env is appended to pi's environment, after the process's own.
	env []string
}

func runPiWith(t *testing.T, s *stub, opts piOpts, prompt string, seedWork ...func(work string)) run {
	t.Helper()
	bin := piPath(t)
	var dirs piDirs
	if opts.dirs != nil {
		dirs = *opts.dirs
	} else {
		dirs = newPiDirs(t)
	}
	home, agentDir, work := dirs.home, dirs.agentDir, dirs.work
	// Non-zero per-million prices make pi's computed usage.cost non-zero.
	model := map[string]any{"id": stubModel, "cost": map[string]any{
		"input": 1.0, "output": 2.0, "cacheRead": 0.1, "cacheWrite": 0.0,
	}}
	if opts.reasoning {
		model["reasoning"] = true
	}
	provider := map[string]any{
		"baseUrl": s.URL + "/v1",
		"api":     "openai-completions",
		"models":  []map[string]any{model},
	}
	if !opts.noAPIKey {
		provider["apiKey"] = "stub-key"
	}
	writeJSON(t, filepath.Join(agentDir, "models.json"), map[string]any{
		"providers": map[string]any{stubProvider: provider},
	})
	for _, seed := range seedWork {
		seed(work)
	}
	// Retries are wall-clock; the default 2s base delay would stall retry tests.
	writeJSON(t, filepath.Join(agentDir, "settings.json"), map[string]any{
		"retry": map[string]any{"maxRetries": 3, "baseDelayMs": 10},
	})

	if opts.hooks != "" {
		installBridge(t, agentDir, opts.hooks)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := opts.args
	if args == nil {
		args = []string{"--model", stubModelRef, "--thinking", "off"}
	}
	trust := "--no-approve"
	if opts.approve {
		trust = "--approve"
	}
	args = append(append([]string{"--print", "--mode", "json", trust}, args...), prompt)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"PI_OFFLINE=1",
		"PI_SKIP_VERSION_CHECK=1",
		"PI_TELEMETRY=0",
	)
	if opts.defaultAgentDir {
		// Drop an inherited value so pi really falls back to its default.
		cmd.Env = slices.DeleteFunc(cmd.Env, func(e string) bool { return strings.HasPrefix(e, "PI_CODING_AGENT_DIR=") })
	} else {
		cmd.Env = append(cmd.Env, "PI_CODING_AGENT_DIR="+agentDir)
	}
	cmd.Env = append(cmd.Env, opts.env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	if dir := os.Getenv(CaptureEnv); dir != "" {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			t.Fatalf("create %s: %v", CaptureEnv, mkErr)
		}
		capturePath := filepath.Join(dir, captureName(t.Name(), nextCaptureRun(t.Name())))
		if wErr := os.WriteFile(capturePath, stdout.Bytes(), 0o644); wErr != nil {
			t.Fatalf("write capture: %v", wErr)
		}
		t.Logf("pi event stream: %s", capturePath)
	}

	r := run{stdout: stdout.String(), stderr: stderr.String(), argv: cmd.Args, err: err}
	for i, line := range strings.Split(strings.TrimRight(r.stdout, "\n"), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if jErr := json.Unmarshal([]byte(line), &ev); jErr != nil {
			t.Fatalf("stdout line %d is not JSON: %v\n%s", i+1, jErr, line)
		}
		r.events = append(r.events, ev)
	}
	return r
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func exitDetail(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return fmt.Sprintf("exit %d", ee.ExitCode())
	}
	return err.Error()
}

func TestPiPlainAnswer(t *testing.T) {
	const answer = "The stubbed model says hello."
	const prompt = "Say hello to the seam."
	s := newStub(t, textReply(answer))

	r := runPi(t, s, prompt)
	if r.err != nil {
		t.Fatalf("pi failed (%s)\nstderr:\n%s\nstdout:\n%s", exitDetail(r.err), r.stderr, r.stdout)
	}
	if got := finalAssistantText(r.events); got != answer {
		t.Errorf("final assistant text = %q, want %q\nstdout:\n%s", got, answer, r.stdout)
	}
	reqs := s.Requests()
	if len(reqs) != 1 {
		t.Fatalf("stub got %d requests, want 1", len(reqs))
	}
	if !strings.Contains(fmt.Sprint(reqs[0]["messages"]), prompt) {
		t.Errorf("request messages lack the prompt: %v", reqs[0]["messages"])
	}
}

// finalAssistantText returns the text of the last assistant message_end event.
func finalAssistantText(events []map[string]any) string {
	return textParts(lastAssistantMessage(events)["content"])
}

// textParts joins the "text" parts of a message or tool-result content list.
func textParts(content any) string {
	parts, _ := content.([]any)
	var sb strings.Builder
	for _, p := range parts {
		if m, _ := p.(map[string]any); m["type"] == "text" {
			sb.WriteString(fmt.Sprint(m["text"]))
		}
	}
	return sb.String()
}

// lastAssistantMessage returns the last assistant message_end message from the
// event stream.
func lastAssistantMessage(events []map[string]any) map[string]any {
	var out map[string]any
	for _, ev := range events {
		if msg, _ := ev["message"].(map[string]any); ev["type"] == "message_end" && msg["role"] == "assistant" {
			out = msg
		}
	}
	return out
}

// messagesJSON renders a request's messages for substring checks.
func messagesJSON(t *testing.T, req map[string]any) string {
	t.Helper()
	b, err := json.Marshal(req["messages"])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var (
	captureMu   sync.Mutex
	captureRuns = map[string]int{}
)

// nextCaptureRun counts the pi runs a test has made, so a test that runs pi
// more than once does not overwrite its own capture.
func nextCaptureRun(testName string) int {
	captureMu.Lock()
	defer captureMu.Unlock()
	captureRuns[testName]++
	return captureRuns[testName]
}

// captureName is the capture file for the nth run of a test: subtest slashes
// become "_", and later runs carry a ".<n>" suffix.
func captureName(testName string, n int) string {
	name := strings.ReplaceAll(testName, "/", "_")
	if n > 1 {
		name += "." + strconv.Itoa(n)
	}
	return name + ".jsonl"
}

func TestCaptureNameSuffixesLaterRuns(t *testing.T) {
	for _, tc := range []struct {
		test string
		n    int
		want string
	}{
		{"TestA", 1, "TestA.jsonl"},
		{"TestA", 2, "TestA.2.jsonl"},
		{"TestA/sub case", 1, "TestA_sub case.jsonl"},
		{"TestA/sub", 3, "TestA_sub.3.jsonl"},
	} {
		if got := captureName(tc.test, tc.n); got != tc.want {
			t.Errorf("captureName(%q, %d) = %q, want %q", tc.test, tc.n, got, tc.want)
		}
	}
}

func TestCaptureKeepsEveryRunInATest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(CaptureEnv, dir)
	s := newStub(t, textReply("one"), textReply("two"))
	var stdouts []string
	for range 2 {
		r := runPi(t, s, "hi")
		if r.err != nil {
			t.Fatalf("pi failed (%s)\nstderr:\n%s", exitDetail(r.err), r.stderr)
		}
		stdouts = append(stdouts, r.stdout)
	}
	if stdouts[0] == stdouts[1] {
		t.Fatal("the two runs produced identical stdout, so a swapped capture would go unnoticed")
	}
	for i, name := range []string{"TestCaptureKeepsEveryRunInATest.jsonl", "TestCaptureKeepsEveryRunInATest.2.jsonl"} {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("missing capture: %v", err)
			continue
		}
		if string(got) != stdouts[i] {
			t.Errorf("%s = %q, want run %d's stdout %q", name, got, i+1, stdouts[i])
		}
	}
}
