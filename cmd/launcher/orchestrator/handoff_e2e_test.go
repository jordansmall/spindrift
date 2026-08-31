package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// e2eDriverExecPackage is the import path go build compiles for the real
// driver-exec binary this test spawns.
const e2eDriverExecPackage = "spindrift.dev/launcher/driver-exec"

// moduleRootForTest resolves cmd/launcher (the driver-exec module root) from
// this source file's own location via runtime.Caller, rather than a
// CWD-relative "../.." -- a subprocess's go build Dir and the assemble-prompt
// asset paths must resolve the same whatever working directory `go test`
// happens to run the binary from.
func moduleRootForTest(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed, cannot locate the module root")
	}
	// thisFile is cmd/launcher/orchestrator/handoff_e2e_test.go; two dirs up
	// is cmd/launcher.
	return filepath.Dir(filepath.Dir(thisFile))
}

// buildDriverExec compiles the REAL driver-exec binary into dir under the
// exact name "driver-exec" (so orchestrator's own exec.LookPath("driver-exec")
// resolves it once dir is on PATH) and returns its path. It fails loudly
// rather than skipping when `go build` is unavailable: under nix develop / CI
// it always is, and a silent skip would hide the one integration this test
// exists to prove.
func buildDriverExec(t *testing.T, dir, moduleRoot string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatalf("go toolchain not on PATH, cannot build the real driver-exec binary: %v", err)
	}
	bin := filepath.Join(dir, "driver-exec")
	cmd := exec.Command("go", "build", "-o", bin, e2eDriverExecPackage)
	cmd.Dir = moduleRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build driver-exec: %v\n%s", err, out)
	}
	return bin
}

// writeFakeClaudeDriver writes an executable bash script standing in for the
// Driver ("claude") that the REAL driver-exec spawns and tees stdout from --
// one layer deeper than writeFakeDriverExec's fakes, which stand in for
// driver-exec itself. It prints real claude-shaped stream-json to its OWN
// stdout (the real driver-exec, not this script, does the log teeing), keyed
// off an invocation count kept in callLog: call 1 (implement pass) narrates
// without an outcome so the loop proceeds into a review pass, call 2 (review
// pass) issues an APPROVE verdict, and call 3 (land pass) prints the terminal
// SPINDRIFT_OUTCOME line.
func writeFakeClaudeDriver(t *testing.T, dir, callLog string) string {
	t.Helper()
	script := "#!/bin/sh\n" +
		`echo call >> "` + callLog + `"` + "\n" +
		`n=$(wc -l < "` + callLog + `")` + "\n" +
		"case \"$n\" in\n" +
		"  1) printf '%s' '" + streamJSONOutcomeLine("Implement pass narration, no outcome yet.") + "' ;;\n" +
		"  2) printf '%s' '" + streamJSONOutcomeLine("VERDICT: APPROVE\\n\\n## Blocking\\n- none\\n\\n## Non-blocking\\n- none") + "' ;;\n" +
		"  3) printf '%s' '" + streamJSONOutcomeLine("SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc") + "' ;;\n" +
		"esac\n" +
		"exit 0\n"
	path := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestHandoffEndToEnd is the first end-to-end turn test (issue #2975): the
// real assemble-prompt verb produces a real handoff file that feeds the real
// orchestrator loop, which spawns the real driver-exec binary as an actual
// subprocess across an implement pass and a review pass to an outcome -- only
// the Driver ("claude") underneath driver-exec is faked. It proves the whole
// handoff interface hangs together: assemble-prompt writes it, orchestrator
// forwards -handoff-file, and driver-exec loads it and sources the driver/
// model/effort/argv-shape facts from it rather than from per-pass flags.
func TestHandoffEndToEnd(t *testing.T) {
	dir := t.TempDir()
	moduleRoot := moduleRootForTest(t)

	// The real driver-exec binary, named exactly "driver-exec" and placed on
	// PATH so orchestrator's own exec.LookPath finds it.
	driverExecBin := buildDriverExec(t, dir, moduleRoot)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	callLog := filepath.Join(dir, "driver-calls.log")
	fakeClaude := writeFakeClaudeDriver(t, dir, callLog)

	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")
	reviewPromptOutput := filepath.Join(dir, "review-prompt.txt")

	// The real assemble-prompt verb, invoked as a subprocess exactly the way
	// entrypoint.sh's phase_prompt_assembly does. Since issue #2979, the 29
	// Box-env-sourced Env fields (promptassembly.EnvFromEnviron) reach it via
	// the process environment rather than a CLI flag; this subprocess is
	// exec'd with no explicit .Env, so it inherits this test process's
	// environment and the t.Setenv calls below reach it exactly like a flag
	// would have. ORCHESTRATOR_ENABLED=1 (plus the review-loop env vars) puts
	// the render on the one cell that emits a review prompt, and
	// --review-prompt-output makes Handoff.ReviewPromptFile non-empty -- the
	// master switch that dispatches the orchestrator into its
	// implement/review/land loop. The registry/prompts paths are still passed
	// as CLI flags (not the PROMPTASSEMBLY_* env vars entrypoint.sh reads),
	// per assembleprompt_cmd_test.go's own convention.
	// This test process may itself run inside a spindrift Box, so the
	// ambient environment can already carry real values for the 14
	// Box-env vars below that this test doesn't otherwise pin -- clear
	// them first so the subprocess sees only what this test sets,
	// matching boxenv_test.go's TestEnvFromEnviron guard.
	for _, envVar := range []string{
		"AGENTS_JSON_TEMPLATE",
		"BOX_FILER_ENABLED",
		"BOX_WORKER_PROVISIONED",
		"BOX_TRACKER_AXIS_READ",
		"BOX_TRACKER_AXIS_WRITE",
		"BOX_TRACKER_AXIS_FILER",
		"LOCAL_ISSUE_REFERENCE",
		"BOX_FORGE_BACKEND",
		"SELF_CONTAINED",
		"RESUME_AFTER_HOLD",
		"AUTO_FORMAT",
		"AUTO_LINT",
		"CI_FAILURE_SUMMARY",
		"RESEARCH_STATUS_ENUM",
	} {
		t.Setenv(envVar, "")
	}
	t.Setenv("ORCHESTRATOR_ENABLED", "1")
	t.Setenv("BOX_REVIEW_LOOP_INLINE", "")
	t.Setenv("BOX_REVIEW_LOOP_ORCHESTRATOR", "1")
	t.Setenv("ISSUE_TRACKER", "github")
	t.Setenv("BOX_WRITE_ENABLED", "1")
	t.Setenv("CODE_FORGE", "github")
	t.Setenv("DISPATCH_KIND", "work")
	t.Setenv("FIX_PASS", "0")
	t.Setenv("ISSUE_NUMBER", "7")
	t.Setenv("ISSUE_TITLE", "End-to-end handoff turn")
	t.Setenv("BRANCH", "agent/issue-7")
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("IN_PROGRESS_LABEL", "agent-in-progress")
	t.Setenv("COMPLETE_LABEL", "agent-complete")
	t.Setenv("RUN_NONCE", "run-nonce-e2e")
	assembleArgs := []string{
		"assemble-prompt",
		"--caveman-skill-baked=true",
		"--tdd-skill-baked=true",
		"--commit-skill-baked=true",
		"--code-review-skill-baked=true",
		"--auto-format-skill-baked=true",
		"--auto-lint-skill-baked=true",
		"--prompts-dir", filepath.Join(moduleRoot, "..", "..", "templates", "default", "prompts"),
		"--skills-found", "caveman, tdd, commit, code-review",
		"--registry", filepath.Join(moduleRoot, "internal", "promptassembly", "testdata", "registry.json"),
		"--validate-markers-registry", filepath.Join(moduleRoot, "internal", "promptassembly", "testdata", "validate-markers.json"),
		"--driver", "claude",
		"--driver-bin", fakeClaude,
		"--driver-flags", "",
		// claude's real argv shape (lib/drivers/claude.nix), the same values
		// entrypoint-orchestrator-handoff.bats pins.
		"--argv-prompt-style", "flag",
		"--argv-prompt-flag", "-p",
		"--argv-model-flag", "--model",
		"--argv-agents-flag", "--agents",
		"--argv-effort-flag", "--effort",
		"--argv-order", "prompt model agents session driverFlags effort",
		"--prompt-output", promptOutput,
		"--agents-json-output", agentsJSONOutput,
		"--handoff-output", handoffOutput,
		"--review-prompt-output", reviewPromptOutput,
	}
	assembleCmd := exec.Command(driverExecBin, assembleArgs...)
	if out, err := assembleCmd.CombinedOutput(); err != nil {
		t.Fatalf("assemble-prompt subprocess: %v\n%s", err, out)
	}
	if _, err := os.Stat(handoffOutput); err != nil {
		t.Fatalf("assemble-prompt wrote no handoff file: %v", err)
	}

	// -prompt-file needs a real file with some content: the implement pass's
	// own seed prompt, which every pass reseeds from.
	promptFile := filepath.Join(dir, "seed-prompt.txt")
	if err := os.WriteFile(promptFile, []byte("Implement the change for issue #7."), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	rc := mainRun([]string{
		"-handoff-file", handoffOutput,
		"-prompt-file", promptFile,
		"-log-path", filepath.Join(dir, "stream.log"),
		"-state-file", filepath.Join(dir, "run-state.json"),
	}, &stdout, &stderr)
	if rc != 0 {
		t.Fatalf("mainRun exit = %d, want 0 (stdout=%q stderr=%q)", rc, stdout.String(), stderr.String())
	}

	out := stdout.String()
	// Both an implement pass and a review pass must actually have run -- not
	// just one -- proving the loop drove a full implement -> review -> land
	// turn through the real driver-exec subprocess.
	if !strings.Contains(out, `"pass_start"`) || !strings.Contains(out, `"role":"implement"`) {
		t.Errorf("stdout missing the implement pass_start op, want an implement pass to have run (stdout=%q)", out)
	}
	if !strings.Contains(out, `"role":"review"`) {
		t.Errorf("stdout missing the review pass_start op, want a review pass to have run (stdout=%q)", out)
	}
	wantOutcome := "SPINDRIFT_OUTCOME issue=7 landing=agent/issue-7 status=ready note=done nonce=abc"
	if !strings.Contains(out, wantOutcome) {
		t.Errorf("stdout missing the terminal outcome line %q (stdout=%q)", wantOutcome, out)
	}
}
