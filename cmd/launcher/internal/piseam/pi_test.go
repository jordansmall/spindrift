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
)

type piBin struct {
	path, skipReason string
	err              error
}

var lockedPi = sync.OnceValue(func() piBin {
	if _, err := exec.LookPath("nix"); err != nil {
		return piBin{skipReason: fmt.Sprintf("%s is unset and nix is not on PATH; set %s to a pi binary", BinEnv, BinEnv)}
	}
	wd, err := os.Getwd()
	if err != nil {
		return piBin{err: err}
	}
	root, err := seamtest.FindFlakeRoot(wd)
	if err != nil {
		return piBin{err: err}
	}
	// --inputs-from pins nixpkgs to the flake's lock, not the registry's.
	out, err := seamtest.NixBuild("--inputs-from", root, "nixpkgs#pi-coding-agent")
	if err != nil {
		return piBin{err: fmt.Errorf("build pi-coding-agent: %w", err)}
	}
	return piBin{path: filepath.Join(out, "bin", "pi")}
})

func piPath(t *testing.T) string {
	t.Helper()
	if v := os.Getenv(BinEnv); v != "" {
		return v
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
	err    error
}

// runPi writes a per-test agent dir pointing a custom Provider at the stub,
// runs pi once in JSON mode and parses the stream. A nonzero exit is returned
// in run.err rather than failing the test; JSON-mode failed turns exit zero,
// so callers read the events too. seedWork prepares the work dir first.
func runPi(t *testing.T, s *stub, prompt string, seedWork ...func(work string)) run {
	t.Helper()
	bin := piPath(t)
	home, agentDir, work := t.TempDir(), t.TempDir(), t.TempDir()
	writeJSON(t, filepath.Join(agentDir, "models.json"), map[string]any{
		"providers": map[string]any{stubProvider: map[string]any{
			"baseUrl": s.URL + "/v1",
			"api":     "openai-completions",
			"apiKey":  "stub-key",
			"models":  []map[string]any{{"id": stubModel}},
		}},
	})
	for _, seed := range seedWork {
		seed(work)
	}
	// Retries are wall-clock; the default 2s base delay would stall retry tests.
	writeJSON(t, filepath.Join(agentDir, "settings.json"), map[string]any{
		"retry": map[string]any{"maxRetries": 3, "baseDelayMs": 10},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--print", "--mode", "json", "--no-approve",
		"--model", stubProvider+"/"+stubModel, "--thinking", "off", prompt)
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"PI_CODING_AGENT_DIR="+agentDir,
		"PI_OFFLINE=1",
		"PI_SKIP_VERSION_CHECK=1",
		"PI_TELEMETRY=0",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	capturePath := filepath.Join(t.TempDir(), "stream.jsonl")
	if dir := os.Getenv(CaptureEnv); dir != "" {
		if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
			t.Fatalf("create %s: %v", CaptureEnv, mkErr)
		}
		capturePath = filepath.Join(dir, strings.ReplaceAll(t.Name(), "/", "_")+".jsonl")
	}
	if wErr := os.WriteFile(capturePath, stdout.Bytes(), 0o644); wErr != nil {
		t.Fatalf("write capture: %v", wErr)
	}
	t.Logf("pi event stream: %s", capturePath)

	r := run{stdout: stdout.String(), stderr: stderr.String(), err: err}
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
	var text string
	for _, ev := range events {
		if ev["type"] != "message_end" {
			continue
		}
		msg, _ := ev["message"].(map[string]any)
		if msg["role"] != "assistant" {
			continue
		}
		text = ""
		parts, _ := msg["content"].([]any)
		for _, p := range parts {
			if m, _ := p.(map[string]any); m["type"] == "text" {
				text += fmt.Sprint(m["text"])
			}
		}
	}
	return text
}
