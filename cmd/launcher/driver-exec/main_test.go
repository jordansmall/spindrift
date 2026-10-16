package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/promptassembly"
)

// writeHandoffFile builds the -handoff-file fixture that replaced the
// individually-removed per-pass flags (issue #2975 slice 3).
func writeHandoffFile(t *testing.T, h promptassembly.Handoff) string {
	t.Helper()
	data, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal handoff: %v", err)
	}
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// claudeArgvShape mirrors the argv shape lib/drivers/claude.nix declares.
// It holds what assemble-prompt's CLI wrapper would put in Handoff.ArgvShape
// for a claude Driver.
var claudeArgvShape = promptassembly.ArgvShape{
	PromptStyle: "flag",
	PromptFlag:  "-p",
	ModelFlag:   "--model",
	AgentsFlag:  "--agents",
	EffortFlag:  "--effort",
	Order:       []string{"prompt", "model", "agents", "session", "driverFlags", "effort"},
}

// TestMainRunHandoffFileReproducesClaudeShape pins issue #2975 slice 3: the
// handoff's ArgvShape, not driver-exec, decides the driver's argv shape.
func TestMainRunHandoffFileReproducesClaudeShape(t *testing.T) {
	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	driverBin := writeFakeDriver(t, dir, "fake-driver", "echo \"$@\" >> "+callLog+"\nexit 0\n")

	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("implement the thing"), 0o600); err != nil {
		t.Fatal(err)
	}
	agentsFile := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(agentsFile, []byte(`{"scout":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	handoffPath := writeHandoffFile(t, promptassembly.Handoff{
		PromptFile: promptFile,
		AgentsFile: agentsFile,
		DriverBin:  driverBin,
		ArgvShape:  claudeArgvShape,
	})

	argv := []string{
		"-handoff-file", handoffPath,
		"-log-path", filepath.Join(dir, "stream.log"),
	}

	var stdout, stderr bytes.Buffer
	rc := mainRun(argv, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}

	calls, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("read callLog: %v", err)
	}
	got := strings.TrimSpace(string(calls))
	fields := strings.Fields(got)
	if len(fields) == 0 || fields[0] != "-p" {
		t.Errorf("driver argv = %q, want it to start with \"-p\" (claude's promptFlag)", got)
	}
	if !strings.Contains(got, "--agents") {
		t.Errorf("driver argv = %q, want it to contain \"--agents\" (claude's agentsFlag)", got)
	}
}

// TestMainRunRoleAwareModelEffort pins the reviewer-role override semantics
// the orchestrator's runWithReviewPass used to own (issue #2975 slice 3),
// plus the fallback and partial-override cases that prove the two guards
// act independently (issue #2975 slice 6).
func TestMainRunRoleAwareModelEffort(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}

	handoff := promptassembly.Handoff{
		PromptFile:   promptFile,
		Model:        "opus",
		Effort:       "high",
		ReviewModel:  "sonnet",
		ReviewEffort: "low",
		ArgvShape:    claudeArgvShape,
	}

	cases := []struct {
		name         string
		role         string
		reviewModel  string
		reviewEffort string
		wantModel    string
		wantEffort   string
	}{
		{"implementor", "", handoff.ReviewModel, handoff.ReviewEffort, "opus", "high"},
		{"reviewer", driverkit.ReviewerRole, handoff.ReviewModel, handoff.ReviewEffort, "sonnet", "low"},
		// With both review overrides empty the fallback to Model/Effort is
		// role-independent (issue #2975 slice 6).
		{"reviewer-no-review-overrides", driverkit.ReviewerRole, "", "", "opus", "high"},
		{"reviewer-model-only", driverkit.ReviewerRole, handoff.ReviewModel, "", "sonnet", "high"},
		{"reviewer-effort-only", driverkit.ReviewerRole, "", handoff.ReviewEffort, "opus", "low"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			callLog := filepath.Join(dir, tc.name+"-calls.log")
			driverBin := writeFakeDriver(t, dir, "fake-driver-"+tc.name, "echo \"$@\" >> "+callLog+"\nexit 0\n")
			h := handoff
			h.DriverBin = driverBin
			h.ReviewModel = tc.reviewModel
			h.ReviewEffort = tc.reviewEffort
			handoffPath := writeHandoffFile(t, h)

			argv := []string{
				"-handoff-file", handoffPath,
				"-log-path", filepath.Join(dir, tc.name+"-stream.log"),
			}
			if tc.role != "" {
				argv = append(argv, "-top-level-role", tc.role)
			}

			var stdout, stderr bytes.Buffer
			rc := mainRun(argv, &stdout, &stderr)
			if rc != 0 {
				t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
			}

			calls, err := os.ReadFile(callLog)
			if err != nil {
				t.Fatalf("read callLog: %v", err)
			}
			got := strings.TrimSpace(string(calls))
			if !strings.Contains(got, "--model "+tc.wantModel) {
				t.Errorf("driver argv = %q, want it to contain \"--model %s\"", got, tc.wantModel)
			}
			if !strings.Contains(got, "--effort "+tc.wantEffort) {
				t.Errorf("driver argv = %q, want it to contain \"--effort %s\"", got, tc.wantEffort)
			}
		})
	}
}

// TestMainRunPromptFileFallsBackToHandoff pins issue #2975 slice 3:
// -prompt-file still overrides the handoff, but is no longer required when
// the handoff already carries a PromptFile.
func TestMainRunPromptFileFallsBackToHandoff(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	driverBin := writeFakeDriver(t, dir, "fake-driver", "exit 0\n")

	handoffPath := writeHandoffFile(t, promptassembly.Handoff{
		PromptFile: promptFile,
		DriverBin:  driverBin,
		ArgvShape:  claudeArgvShape,
	})

	argv := []string{
		"-handoff-file", handoffPath,
		"-log-path", filepath.Join(dir, "stream.log"),
	}

	var stdout, stderr bytes.Buffer
	rc := mainRun(argv, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}
}

// TestMainRunPromptFileRequiredWhenBothEmpty pins that the handoff fallback
// did not drop the original "-prompt-file is required" error.
func TestMainRunPromptFileRequiredWhenBothEmpty(t *testing.T) {
	dir := t.TempDir()
	driverBin := writeFakeDriver(t, dir, "fake-driver", "exit 0\n")

	handoffPath := writeHandoffFile(t, promptassembly.Handoff{
		DriverBin: driverBin,
		ArgvShape: claudeArgvShape,
	})

	argv := []string{
		"-handoff-file", handoffPath,
		"-log-path", filepath.Join(dir, "stream.log"),
	}

	var stdout, stderr bytes.Buffer
	rc := mainRun(argv, &stdout, &stderr)
	if rc != 1 {
		t.Fatalf("mainRun exit code = %d, want 1", rc)
	}
	if !strings.Contains(stderr.String(), "-prompt-file is required") {
		t.Errorf("stderr = %q, want it to mention \"-prompt-file is required\"", stderr.String())
	}
}

// TestResolveExitAppliesOpencodeErrorEvent pins issue #2263: opencode exits 0
// after a mid-run error, so driver-exec must apply the Driver's ResolveExit
// result after run returns.
func TestResolveExitAppliesOpencodeErrorEvent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "stream.log")
	if err := os.WriteFile(logPath, []byte(`{"type":"error","error":"boom"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := driver.New("opencode")
	if err != nil {
		t.Fatalf("driver.New(opencode): %v", err)
	}

	if got := resolveExit(d, 0, logPath); got != 1 {
		t.Errorf("resolveExit = %d, want 1 (error event behind exit 0)", got)
	}
	if got := resolveExit(d, 7, logPath); got != 7 {
		t.Errorf("resolveExit = %d, want 7 (non-zero rc kept)", got)
	}
}

// TestResolveExitLeavesClaudeExitCodeUntouched pins the no-op case: claude's
// ResolveExit trusts the child rc, so resolveExit must not change it.
func TestResolveExitLeavesClaudeExitCodeUntouched(t *testing.T) {
	d, err := driver.New("claude")
	if err != nil {
		t.Fatalf("driver.New(claude): %v", err)
	}

	got := resolveExit(d, 7, filepath.Join(t.TempDir(), "does-not-exist.log"))
	if got != 7 {
		t.Errorf("resolveExit = %d, want 7 (claude's own rc, untouched)", got)
	}
}

// errResolveDriver is a Driver whose ResolveExit always fails.
type errResolveDriver struct{ driver.Driver }

func (errResolveDriver) ResolveExit(string, int) (int, error) {
	return 99, errors.New("resolve failed")
}

// A ResolveExit error must degrade to the child's own rc, not mask a possibly
// successful run behind the resolution failure.
func TestResolveExitDegradesToRCOnDriverError(t *testing.T) {
	for _, rc := range []int{0, 3} {
		if got := resolveExit(errResolveDriver{}, rc, "unused.log"); got != rc {
			t.Errorf("resolveExit(rc=%d) = %d, want the original rc", rc, got)
		}
	}
}

// A handoff without a HeartbeatLog still gets the coarse heartbeat at
// /tmp/heartbeat.log, the path a human tails inside the Box (issue #183).
func TestMainRunHeartbeatLogDefaultsToTmp(t *testing.T) {
	const defaultLog = "/tmp/heartbeat.log"
	os.Remove(defaultLog)
	t.Cleanup(func() { os.Remove(defaultLog) })

	dir := t.TempDir()
	promptFile := filepath.Join(dir, "prompt.txt")
	if err := os.WriteFile(promptFile, []byte("do it"), 0o600); err != nil {
		t.Fatal(err)
	}
	driverBin := writeFakeDriver(t, dir, "fake-driver", `printf '{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"main.go"}}]}}\n{"type":"result","num_turns":1}\n'`)
	handoffPath := writeHandoffFile(t, promptassembly.Handoff{
		PromptFile: promptFile,
		DriverBin:  driverBin,
		Issue:      "7",
		ArgvShape:  claudeArgvShape,
	})

	var stdout, stderr bytes.Buffer
	if rc := mainRun([]string{"-handoff-file", handoffPath, "-log-path", filepath.Join(dir, "stream.log")}, &stdout, &stderr); rc != 0 {
		t.Fatalf("mainRun exit code = %d, want 0 (stderr: %q)", rc, stderr.String())
	}
	got, err := os.ReadFile(defaultLog)
	if err != nil {
		t.Fatalf("read %s: %v", defaultLog, err)
	}
	if !bytes.Contains(got, []byte("#7")) {
		t.Errorf("heartbeat missing issue prefix: %q", got)
	}
}
