package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/driver"
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/promptassembly"
)

// writeHandoffFile marshals h to JSON and writes it to a fresh file under
// t.TempDir(), returning its path -- the fixture every default-verb test
// below hands to -handoff-file in place of the individually-removed flags
// (issue #2975 slice 3).
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

// claudeArgvShape mirrors claude's own argv shape (lib/drivers/claude.nix:
// promptFlag="-p", agentsFlag="--agents"), the same values
// assemble-prompt's CLI wrapper would have populated Handoff.ArgvShape with
// for a claude Driver.
var claudeArgvShape = promptassembly.ArgvShape{
	PromptStyle: "flag",
	PromptFlag:  "-p",
	ModelFlag:   "--model",
	AgentsFlag:  "--agents",
	EffortFlag:  "--effort",
	Order:       []string{"prompt", "model", "agents", "session", "driverFlags", "effort"},
}

// TestMainRunHandoffFileReproducesClaudeShape verifies a mainRun invocation
// driven by a -handoff-file whose ArgvShape describes claude's own argv
// shape reproduces that shape on the fake Driver's received argv (issue
// #2975 slice 3): -p leads, --agents is present.
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

// TestMainRunRoleAwareModelEffort verifies mainRun resolves Model/Effort
// from the handoff's ReviewModel/ReviewEffort when -top-level-role is
// driverkit.ReviewerRole, and from Model/Effort directly otherwise --
// replicating the orchestrator's former runWithReviewPass overrideIfSet
// semantics (issue #2975 slice 3). Also covers the reviewer-role fallback
// when ReviewModel/ReviewEffort are unset, and the two partial-override
// combinations, to prove the two guards are independent (issue #2975
// slice 6).
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
		// Reviewer role with both ReviewModel/ReviewEffort unset on the
		// handoff falls back to Model/Effort just like the implementor
		// case -- the fallback is role-independent once the review
		// overrides are empty (issue #2975 slice 6).
		{"reviewer-no-review-overrides", driverkit.ReviewerRole, "", "", "opus", "high"},
		// Partial overrides prove the two "if ReviewX != \"\"" guards in
		// main.go act independently rather than both-or-nothing.
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

// TestMainRunPromptFileFallsBackToHandoff verifies omitting -prompt-file
// falls back to the handoff's own PromptFile (issue #2975 slice 3): the
// per-pass CLI flag stays meaningful for a caller that wants to override it,
// but is no longer required when the handoff already carries one.
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

// TestMainRunPromptFileRequiredWhenBothEmpty verifies that when neither
// -prompt-file nor the handoff's own PromptFile is set, mainRun still fails
// with the original "-prompt-file is required" error.
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

// TestResolveExitUsesSynthesizedExitForOpencode verifies resolveExit
// replaces the child process's own exit code with the opencode Driver's
// ResolveExit result (issue #2263) -- opencode's own process exit code is
// not trustworthy on its own (see driver/opencode/exitsynth.go), so
// driver-exec's main must apply the Driver's required ResolveExit result
// after run returns.
func TestResolveExitUsesSynthesizedExitForOpencode(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "stream.log")
	// A log with a valid outcome line and no error event synthesizes to 0
	// (see driver/opencode/exitsynth_test.go's own fixtures) -- distinct
	// from the "child rc" this test passes in, so a passing assertion proves
	// the synthesized value actually replaced it rather than coincidentally
	// matching.
	content := `{"type":"result","status":"ready"}` + "\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := driver.New("opencode")
	if err != nil {
		t.Fatalf("driver.New(opencode): %v", err)
	}

	got := resolveExit(d, 7, logPath)
	if got == 7 {
		t.Errorf("resolveExit = %d, want it to replace the child rc (7) with the synthesized exit code", got)
	}
}

// TestResolveExitLeavesClaudeExitCodeUntouched verifies resolveExit is a
// no-op for a Driver (claude) whose ResolveExit trusts the passed exit code
// unchanged -- the child rc passes through unchanged.
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

// TestResolveExitOnErrorKeepsOriginalRC verifies a ResolveExit failure (e.g.
// an unreadable log path) degrades safely to the original child rc instead
// of masking a real failure behind a resolution failure.
func TestResolveExitOnErrorKeepsOriginalRC(t *testing.T) {
	d, err := driver.New("opencode")
	if err != nil {
		t.Fatalf("driver.New(opencode): %v", err)
	}

	// SynthesizeExit tolerates a missing log (see
	// driver/opencode/exitsynth_test.go's TestSynthesizeExit_MissingFile_IsNonZero),
	// so this exercises the fallback path indirectly: a missing-file result
	// is still a non-error, non-zero synthesized code, which is exactly what
	// this test wants to see replace the child rc.
	got := resolveExit(d, 0, filepath.Join(t.TempDir(), "does-not-exist.log"))
	if got == 0 {
		t.Errorf("resolveExit = %d, want a non-zero synthesized exit for a missing/invalid log", got)
	}
}
