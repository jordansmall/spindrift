package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/runstate"
	"spindrift.dev/launcher/internal/seedblock"
	"spindrift.dev/launcher/internal/testutil/repopath"
)

// coveredCellArgs puts runAssemblePrompt's Env in promptassembly.Assemble's
// covered cell (issue #2540: checkCoveredCell checks only dispatch kind
// "work"). Since issue #2979 the Box-env-sourced fields arrive via t.Setenv,
// so every one is set explicitly, even to "", or a leftover value in the test
// process's own environment leaks into the run. Blanking the generated
// promptassembly.BoxEnvVarNames keeps that true as rows are added (issue
// #4044).
func coveredCellArgs(t *testing.T, promptOutput, agentsJSONOutput, handoffOutput string) []string {
	t.Helper()
	for _, name := range promptassembly.BoxEnvVarNames {
		t.Setenv(name, "")
	}
	t.Setenv("ISSUE_TRACKER", "github")
	t.Setenv("BOX_WRITE_ENABLED", "1")
	t.Setenv("CODE_FORGE", "github")
	t.Setenv("DISPATCH_KIND", "work")
	t.Setenv("FIX_PASS", "0")
	t.Setenv("ISSUE_NUMBER", "2349")
	t.Setenv("ISSUE_TITLE", "Add assemble-prompt CLI verb")
	t.Setenv("BRANCH", "agent/issue-2349")
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("IN_PROGRESS_LABEL", "agent-in-progress")
	t.Setenv("COMPLETE_LABEL", "agent-complete")
	t.Setenv("RUN_NONCE", "run-nonce-abc123")
	return []string{
		"--caveman-skill-baked=true",
		"--tdd-skill-baked=true",
		"--commit-skill-baked=true",
		"--code-review-skill-baked=true",
		"--auto-format-skill-baked=true",
		"--auto-lint-skill-baked=true",
		"--prompts-dir", repopath.PromptsDir(),
		"--skills-found", "caveman, tdd, commit, code-review",
		"--registry", repopath.RegistryJSON(),
		"--validate-markers-registry", repopath.ValidateMarkersJSON(),
		"--prompt-output", promptOutput,
		"--agents-json-output", agentsJSONOutput,
		"--handoff-output", handoffOutput,
	}
}

// Pins that flag parsing reaches promptassembly.Assemble with the right Env
// and Registry, and that the run writes all three output files (issue #2349).
func TestRunAssemblePrompt_CoveredCellWritesOutputs(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	var stdout bytes.Buffer
	rc := runAssemblePrompt(coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput), &stdout, io.Discard)
	if rc != 0 {
		t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	promptBytes, err := os.ReadFile(promptOutput)
	if err != nil {
		t.Fatalf("read prompt output: %v", err)
	}
	if len(promptBytes) == 0 {
		t.Error("prompt output is empty, want non-empty")
	}

	if _, err := os.Stat(agentsJSONOutput); err != nil {
		t.Fatalf("agents json output not written: %v", err)
	}

	handoffBytes, err := os.ReadFile(handoffOutput)
	if err != nil {
		t.Fatalf("read handoff output: %v", err)
	}
	var handoff struct {
		SessionMode      string
		ReviewPromptFile string
		ReviewModel      string
	}
	if err := json.Unmarshal(handoffBytes, &handoff); err != nil {
		t.Fatalf("unmarshal handoff output: %v\n%s", err, handoffBytes)
	}
	if handoff.SessionMode == "" {
		t.Errorf("handoff.SessionMode is empty, want a session mode")
	}
}

// The dispatch kind is the one axis checkCoveredCell still validates as of
// issue #2540, because it has no eval-time or launcher-side guard the way
// IssueTracker and CodeForge do. An unsupported value must come back as a
// CLI failure, not a panic.
func TestRunAssemblePrompt_UnsupportedCellReturnsNonZero(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	t.Setenv("DISPATCH_KIND", "bogus-kind")

	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout, io.Discard)
	if rc == 0 {
		t.Fatal("runAssemblePrompt exit = 0, want non-zero for an unsupported cell")
	}
	if !strings.Contains(stdout.String(), "bogus-kind") {
		t.Errorf("stdout = %q, want it to mention the rejected dispatch kind bogus-kind", stdout.String())
	}
}

// A missing -handoff-output must fail loudly instead of running Assemble
// against a zero-value output path.
func TestRunAssemblePrompt_MissingRequiredFlagReturnsNonZero(t *testing.T) {
	dir := t.TempDir()
	var stdout bytes.Buffer
	rc := runAssemblePrompt([]string{
		"--registry", repopath.RegistryJSON(),
		"--prompt-output", filepath.Join(dir, "prompt.txt"),
		"--agents-json-output", filepath.Join(dir, "agents.json"),
	}, &stdout, io.Discard)
	if rc == 0 {
		t.Fatal("runAssemblePrompt exit = 0, want non-zero for a missing -handoff-output")
	}
}

// A missing -validate-markers-registry must fail loudly instead of running
// Assemble and Validate against a zero-value registry path (issue #2356).
func TestRunAssemblePrompt_ValidateMarkersRegistryRequired(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--validate-markers-registry" {
			i++ // also skip its value
			continue
		}
		filtered = append(filtered, args[i])
	}

	var stdout bytes.Buffer
	rc := runAssemblePrompt(filtered, &stdout, io.Discard)
	if rc == 0 {
		t.Fatal("runAssemblePrompt exit = 0, want non-zero for a missing -validate-markers-registry")
	}
	if !strings.Contains(stdout.String(), "validate-markers-registry") {
		t.Errorf("stdout = %q, want it to mention validate-markers-registry", stdout.String())
	}
}

// The dir deliberately has no fragments subdir: Assemble swallows a missing
// fragment file as an empty render (see assemble.go's fragment loop), so the
// readOnlyResearch validate row's SPINDRIFT_COMMENT marker cannot creep back in.
func researchPromptDirLackingSpindriftComment(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	content := "# TASK\n\nResearch issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}\n\nNo verdict marker here.\n"
	if err := os.WriteFile(filepath.Join(dir, "research-prompt.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write research-prompt.md: %v", err)
	}
	return dir
}

// This dir omits the fragments subdir for the same reason as the one above: the
// boxAccessReadOnly validate row's SPINDRIFT_PR_INTENT marker must stay missing.
func issuePromptDirLackingSpindriftPRIntent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	content := "# TASK\n\nWork issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}\n\nNo PR-intent marker here.\n"
	if err := os.WriteFile(filepath.Join(dir, "issue-prompt.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write issue-prompt.md: %v", err)
	}
	// A fresh work dispatch always renders the review prompt, so it must exist.
	if err := os.WriteFile(filepath.Join(dir, "review-prompt.md"), []byte("VERDICT: APPROVE\n"), 0o644); err != nil {
		t.Fatalf("write review-prompt.md: %v", err)
	}
	return dir
}

// replaceArg drops flag in either the two-token or the "--flag=value" form
// coveredCellArgs mixes, then re-adds it as one "--flag=value" token, which
// both flag kinds accept. Go's flag package requires the "=" for an explicit
// bool value: a bare "--flag next-token" reads next-token as a positional
// argument and halts flag parsing entirely.
func replaceArg(args []string, flag, value string) []string {
	out := make([]string, 0, len(args)+1)
	for i := 0; i < len(args); i++ {
		if args[i] == flag && i+1 < len(args) {
			i++ // drop its paired value token too
			continue
		}
		if strings.HasPrefix(args[i], flag+"=") {
			continue
		}
		out = append(out, args[i])
	}
	out = append(out, flag+"="+value)
	return out
}

// validatorCarrierCases pairs each BOX_SIGNAL_CARRIER value with the call the
// validator names for a missing reject (comment) and warn (pr-intent) row:
// the log carrier (unset; Gates reads anything but "socket" as log) names the
// marker, the socket carrier the driver-exec signal call (issue #4044).
var validatorCarrierCases = []struct {
	carrier, reject, warn string
}{
	{"", "SPINDRIFT_COMMENT", "SPINDRIFT_PR_INTENT"},
	{"socket", "driver-exec signal comment", "driver-exec signal pr-intent"},
}

func carrierName(carrier string) string {
	if carrier == "" {
		return "log"
	}
	return carrier
}

// A reject-severity validate row with its gate active and its marker missing
// must exit non-zero and write none of the three output files: the Driver
// must never run against an unmet contract (issue #2356).
func TestRunAssemblePrompt_ValidatorRejectBlocksOutputs(t *testing.T) {
	for _, tc := range validatorCarrierCases {
		t.Run(carrierName(tc.carrier), func(t *testing.T) {
			dir := t.TempDir()
			promptOutput := filepath.Join(dir, "prompt.txt")
			agentsJSONOutput := filepath.Join(dir, "agents.json")
			handoffOutput := filepath.Join(dir, "handoff.json")

			args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
			t.Setenv("DISPATCH_KIND", "research")
			t.Setenv("BOX_WRITE_ENABLED", "")
			t.Setenv("BOX_SIGNAL_CARRIER", tc.carrier)
			args = replaceArg(args, "--prompts-dir", researchPromptDirLackingSpindriftComment(t))

			var stdout bytes.Buffer
			rc := runAssemblePrompt(args, &stdout, io.Discard)
			if rc == 0 {
				t.Fatalf("runAssemblePrompt exit = 0, want non-zero for a reject-gate missing marker (stdout=%q)", stdout.String())
			}
			if !strings.Contains(stdout.String(), tc.reject) {
				t.Errorf("stdout = %q, want it to mention %s", stdout.String(), tc.reject)
			}

			for _, p := range []string{promptOutput, agentsJSONOutput, handoffOutput} {
				if info, err := os.Stat(p); err == nil {
					t.Errorf("output file %s exists (size %d), want it never written on reject", p, info.Size())
				} else if !os.IsNotExist(err) {
					t.Errorf("stat %s: %v", p, err)
				}
			}
		})
	}
}

// The warn counterpart: a warn-severity row is advisory, so the run still
// succeeds and writes all three output files (issue #2356).
func TestRunAssemblePrompt_ValidatorWarnStillWritesOutputs(t *testing.T) {
	for _, tc := range validatorCarrierCases {
		t.Run(carrierName(tc.carrier), func(t *testing.T) {
			dir := t.TempDir()
			promptOutput := filepath.Join(dir, "prompt.txt")
			agentsJSONOutput := filepath.Join(dir, "agents.json")
			handoffOutput := filepath.Join(dir, "handoff.json")

			args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
			t.Setenv("BOX_WRITE_ENABLED", "")
			t.Setenv("BOX_SIGNAL_CARRIER", tc.carrier)
			args = replaceArg(args, "--prompts-dir", issuePromptDirLackingSpindriftPRIntent(t))

			var stdout bytes.Buffer
			rc := runAssemblePrompt(args, &stdout, io.Discard)
			if rc != 0 {
				t.Fatalf("runAssemblePrompt exit = %d, want 0 for a warn-gate missing marker (stdout=%q)", rc, stdout.String())
			}
			if !strings.Contains(stdout.String(), tc.warn) {
				t.Errorf("stdout = %q, want it to mention %s", stdout.String(), tc.warn)
			}

			// agentsJSONOutput is only Stat-checked, not size-checked: with no
			// AGENTS_JSON_TEMPLATE configured, Assemble's AgentsJSON legitimately
			// renders empty.
			if _, err := os.Stat(agentsJSONOutput); err != nil {
				t.Fatalf("agents json output not written: %v", err)
			}
			for _, p := range []string{promptOutput, handoffOutput} {
				info, err := os.Stat(p)
				if err != nil {
					t.Fatalf("output file %s not written: %v", p, err)
				}
				if info.Size() == 0 {
					t.Errorf("output file %s is empty, want non-empty", p)
				}
			}
		})
	}
}

// The BOX_TRACKER_AXIS_* env vars must reach Env's TrackerAxis* fields
// (issue #2533 slice 2). ISSUE_TRACKER stays "github" on purpose:
// gates_tracker.go reads the axis fields directly instead of re-deriving
// them, and checkCoveredCell no longer re-validates IssueTracker (issue
// #2540), so the axis fields are the sole gate input.
func TestRunAssemblePrompt_TrackerAxisEnvVarsReachGates(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	t.Setenv("BOX_TRACKER_AXIS_READ", "FORGEJO")
	t.Setenv("BOX_TRACKER_AXIS_WRITE", "FORGEJO")
	t.Setenv("BOX_TRACKER_AXIS_FILER", "FORGEJO")

	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout, io.Discard)
	if rc != 0 {
		t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	promptBytes, err := os.ReadFile(promptOutput)
	if err != nil {
		t.Fatalf("read prompt output: %v", err)
	}
	prompt := string(promptBytes)
	if !strings.Contains(prompt, "via Forgejo") {
		t.Errorf("prompt does not contain %q (forgejo issue-read fragment), want it rendered when --tracker-axis-read=FORGEJO", "via Forgejo")
	}
	if strings.Contains(prompt, "via GitHub") {
		t.Errorf("prompt contains %q (github issue-read fragment), want it absent when --tracker-axis-read=FORGEJO", "via GitHub")
	}
}

// BOX_FORGE_BACKEND must reach Env.ForgeBackend: gates_access_forge.go reads
// it directly rather than re-deriving it from CODE_FORGE (issue #2533 slice
// 2), so FORGEJO fires FIX_CI_READ_FORGEJO even with CODE_FORGE left at
// "github".
func TestRunAssemblePrompt_ForgeBackendEnvVarReachesGates(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	t.Setenv("FIX_PASS", "1")
	t.Setenv("BOX_FORGE_BACKEND", "FORGEJO")

	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout, io.Discard)
	if rc != 0 {
		t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	promptBytes, err := os.ReadFile(promptOutput)
	if err != nil {
		t.Fatalf("read prompt output: %v", err)
	}
	prompt := string(promptBytes)
	if !strings.Contains(prompt, "fj pr status") {
		t.Errorf("prompt does not contain %q (forgejo fix-ci-read fragment), want it rendered when --forge-backend=FORGEJO", "fj pr status")
	}
	if strings.Contains(prompt, "gh pr view --json") {
		t.Errorf("prompt contains %q (github fix-ci-read fragment), want it absent when --forge-backend=FORGEJO", "gh pr view --json")
	}
}

// Marker strings copied from the fragment files under
// templates/default/prompts/fragments/ and grep-confirmed to appear nowhere
// else under templates/default/prompts/, so a Contains check on the rendered
// prompt is unambiguous. file-issues-direct.md and file-issues-relay.md share
// filerEnabledMarker, and both forks require e.FilerEnabled.
const (
	filerEnabledMarker      = "# FILE ISSUES"
	workerProvisionedMarker = "rather than editing the source yourself"
)

func assemblePromptForTest(t *testing.T, dir string, args []string) string {
	t.Helper()
	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout, io.Discard)
	if rc != 0 {
		t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	promptBytes, err := os.ReadFile(filepath.Join(dir, "prompt.txt"))
	if err != nil {
		t.Fatalf("read prompt output: %v", err)
	}
	return string(promptBytes)
}

// BOX_FILER_ENABLED must reach Env.FilerEnabled and, through it, the
// FILER_ENABLED gate (issue #2533 slice 2).
func TestRunAssemblePrompt_FilerEnabledEnvVarReachesPrompt(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			promptOutput := filepath.Join(dir, "prompt.txt")
			agentsJSONOutput := filepath.Join(dir, "agents.json")
			handoffOutput := filepath.Join(dir, "handoff.json")

			args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
			t.Setenv("BOX_FILER_ENABLED", presenceEnvValue(enabled))

			prompt := assemblePromptForTest(t, dir, args)
			got := strings.Contains(prompt, filerEnabledMarker)
			if got != enabled {
				t.Errorf("prompt contains %q = %v, want %v (BOX_FILER_ENABLED=%v)", filerEnabledMarker, got, enabled, enabled)
			}
		})
	}
}

// BOX_WORKER_PROVISIONED must reach Env.WorkerProvisioned and, through it,
// the WORKER_PROVISIONED gate (issue #2533 slice 2).
func TestRunAssemblePrompt_WorkerProvisionedEnvVarReachesPrompt(t *testing.T) {
	for _, provisioned := range []bool{true, false} {
		name := "unprovisioned"
		if provisioned {
			name = "provisioned"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			promptOutput := filepath.Join(dir, "prompt.txt")
			agentsJSONOutput := filepath.Join(dir, "agents.json")
			handoffOutput := filepath.Join(dir, "handoff.json")

			args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
			t.Setenv("BOX_WORKER_PROVISIONED", presenceEnvValue(provisioned))

			prompt := assemblePromptForTest(t, dir, args)
			got := strings.Contains(prompt, workerProvisionedMarker)
			if got != provisioned {
				t.Errorf("prompt contains %q = %v, want %v (BOX_WORKER_PROVISIONED=%v)", workerProvisionedMarker, got, provisioned, provisioned)
			}
		})
	}
}

// Turning on exactly one of BOX_FILER_ENABLED and BOX_WORKER_PROVISIONED must
// render only that one's marker. A bug that swapped the two Env destinations
// would show both markers together or neither (issue #2533 slice 2, the
// review finding this pins closed).
func TestRunAssemblePrompt_FilerAndWorkerEnvVarsNotCrossWired(t *testing.T) {
	cases := []struct {
		name       string
		filer      bool
		worker     bool
		wantFiler  bool
		wantWorker bool
	}{
		{"filer on, worker off", true, false, true, false},
		{"filer off, worker on", false, true, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			promptOutput := filepath.Join(dir, "prompt.txt")
			agentsJSONOutput := filepath.Join(dir, "agents.json")
			handoffOutput := filepath.Join(dir, "handoff.json")

			args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
			t.Setenv("BOX_FILER_ENABLED", presenceEnvValue(c.filer))
			t.Setenv("BOX_WORKER_PROVISIONED", presenceEnvValue(c.worker))

			prompt := assemblePromptForTest(t, dir, args)
			if gotFiler := strings.Contains(prompt, filerEnabledMarker); gotFiler != c.wantFiler {
				t.Errorf("prompt contains %q = %v, want %v (BOX_FILER_ENABLED=%v, BOX_WORKER_PROVISIONED=%v)", filerEnabledMarker, gotFiler, c.wantFiler, c.filer, c.worker)
			}
			if gotWorker := strings.Contains(prompt, workerProvisionedMarker); gotWorker != c.wantWorker {
				t.Errorf("prompt contains %q = %v, want %v (BOX_FILER_ENABLED=%v, BOX_WORKER_PROVISIONED=%v)", workerProvisionedMarker, gotWorker, c.wantWorker, c.filer, c.worker)
			}
		})
	}
}

// A presence-kind Env field tests os.Getenv(k) != "", so false must be the
// empty string, not "false" (issue #2979).
func presenceEnvValue(b bool) string {
	if b {
		return "1"
	}
	return ""
}

// Only a bare "assemble-prompt" first arg selects the subcommand; every other
// shape must fall through to the Driver path or to another subcommand.
func TestIsAssemblePromptInvocation(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"assemble-prompt first arg", []string{"assemble-prompt", "--registry", "x"}, true},
		{"no args", nil, false},
		{"ordinary flag invocation", []string{"--driver", "claude"}, false},
		{"bundle-out", []string{"bundle-out"}, false},
	}
	for _, c := range cases {
		if got := isAssemblePromptInvocation(c.args); got != c.want {
			t.Errorf("%s: isAssemblePromptInvocation(%v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}

// The passthrough flags must reach result.Handoff untouched by Assemble, and
// PromptFile, AgentsFile and Issue must come from the output and issue flags
// this command already parsed (issue #2975).
func TestRunAssemblePrompt_PopulatesPassthroughHandoffFields(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	args = append(args,
		"--model", "gpt-codex",
		"--effort", "high",
		"--driver", "claude",
		"--driver-bin", "/usr/bin/claude",
		"--driver-flags", "--foo --bar",
		"--devshell",
		"--devshell-name", "myshell",
		"--argv-prompt-style", "positional",
		"--argv-prompt-flag", "-p",
		"--argv-model-flag", "--model",
		"--argv-agents-flag", "--agents",
		"--argv-effort-flag", "--effort",
		"--argv-order", "prompt model agents session driverFlags effort",
		"--argv-model-omit-empty",
		"--max-review-rounds", "3",
		"--max-slices", "10",
		"--max-budget-tokens", "50000",
		"--max-budget-usd", "12.5",
		"--heartbeat-log", "/tmp/hb.log",
	)

	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout, io.Discard)
	if rc != 0 {
		t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	handoffBytes, err := os.ReadFile(handoffOutput)
	if err != nil {
		t.Fatalf("read handoff output: %v", err)
	}
	var handoff promptassembly.Handoff
	if err := json.Unmarshal(handoffBytes, &handoff); err != nil {
		t.Fatalf("unmarshal handoff output: %v\n%s", err, handoffBytes)
	}

	if handoff.PromptFile != promptOutput {
		t.Errorf("Handoff.PromptFile = %q, want %q", handoff.PromptFile, promptOutput)
	}
	if handoff.AgentsFile != "" {
		t.Errorf("Handoff.AgentsFile = %q, want empty (this covered cell renders no --agents JSON)", handoff.AgentsFile)
	}
	if handoff.Model != "gpt-codex" {
		t.Errorf("Handoff.Model = %q, want gpt-codex", handoff.Model)
	}
	if handoff.Effort != "high" {
		t.Errorf("Handoff.Effort = %q, want high", handoff.Effort)
	}
	if handoff.Driver != "claude" {
		t.Errorf("Handoff.Driver = %q, want claude", handoff.Driver)
	}
	if handoff.DriverBin != "/usr/bin/claude" {
		t.Errorf("Handoff.DriverBin = %q, want /usr/bin/claude", handoff.DriverBin)
	}
	if handoff.DriverFlags != "--foo --bar" {
		t.Errorf("Handoff.DriverFlags = %q, want %q", handoff.DriverFlags, "--foo --bar")
	}
	if !handoff.Devshell {
		t.Error("Handoff.Devshell = false, want true")
	}
	if handoff.DevshellName != "myshell" {
		t.Errorf("Handoff.DevshellName = %q, want myshell", handoff.DevshellName)
	}
	if handoff.Issue != "2349" {
		t.Errorf("Handoff.Issue = %q, want 2349", handoff.Issue)
	}
	if handoff.HeartbeatLog != "/tmp/hb.log" {
		t.Errorf("Handoff.HeartbeatLog = %q, want /tmp/hb.log", handoff.HeartbeatLog)
	}

	wantArgvShape := promptassembly.ArgvShape{
		PromptStyle:    "positional",
		PromptFlag:     "-p",
		ModelFlag:      "--model",
		ModelOmitEmpty: true,
		AgentsFlag:     "--agents",
		EffortFlag:     "--effort",
		Order:          []string{"prompt", "model", "agents", "session", "driverFlags", "effort"},
	}
	if handoff.ArgvShape.PromptStyle != wantArgvShape.PromptStyle ||
		handoff.ArgvShape.PromptFlag != wantArgvShape.PromptFlag ||
		handoff.ArgvShape.ModelFlag != wantArgvShape.ModelFlag ||
		handoff.ArgvShape.ModelOmitEmpty != wantArgvShape.ModelOmitEmpty ||
		handoff.ArgvShape.AgentsFlag != wantArgvShape.AgentsFlag ||
		handoff.ArgvShape.EffortFlag != wantArgvShape.EffortFlag ||
		!reflectStringSlicesEqual(handoff.ArgvShape.Order, wantArgvShape.Order) {
		t.Errorf("Handoff.ArgvShape = %+v, want %+v", handoff.ArgvShape, wantArgvShape)
	}

	wantCaps := promptassembly.Caps{
		MaxSlices:       10,
		MaxReviewRounds: 3,
		MaxBudgetTokens: 50000,
		MaxBudgetUSD:    12.5,
	}
	if handoff.Caps != wantCaps {
		t.Errorf("Handoff.Caps = %+v, want %+v", handoff.Caps, wantCaps)
	}
}

// This is hand-rolled so the package needs neither reflect nor slices for a
// single comparison.
func reflectStringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Pins the graceful-degrade contract for the budget caps (issue #2975 review
// finding #1, restoring coverage dropped when
// TestMainRunToleratesMalformedOrNegativeBudgetCaps was deleted). entrypoint.sh
// forwards the values verbatim, so an operator typo must degrade to 0 here
// rather than fail fs.Parse and kill the box run under set -euo pipefail (#2694).
func TestRunAssemblePrompt_MalformedBudgetCapsDegradeToZero(t *testing.T) {
	tests := []struct {
		name            string
		maxBudgetTokens string
		maxBudgetUSD    string
	}{
		{"malformed strings", "not-a-number", "not-a-number"},
		{"negative values", "-1", "-0.01"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			promptOutput := filepath.Join(dir, "prompt.txt")
			agentsJSONOutput := filepath.Join(dir, "agents.json")
			handoffOutput := filepath.Join(dir, "handoff.json")

			args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
			args = append(args,
				"--max-budget-tokens", tt.maxBudgetTokens,
				"--max-budget-usd", tt.maxBudgetUSD,
			)

			var stdout bytes.Buffer
			rc := runAssemblePrompt(args, &stdout, io.Discard)
			if rc != 0 {
				t.Fatalf("runAssemblePrompt exit = %d, want 0 (malformed/negative budget caps must degrade to 0, not fail the run) (stdout=%q)", rc, stdout.String())
			}

			handoffBytes, err := os.ReadFile(handoffOutput)
			if err != nil {
				t.Fatalf("read handoff output: %v", err)
			}
			var handoff promptassembly.Handoff
			if err := json.Unmarshal(handoffBytes, &handoff); err != nil {
				t.Fatalf("unmarshal handoff output: %v\n%s", err, handoffBytes)
			}

			if handoff.Caps.MaxBudgetTokens != 0 {
				t.Errorf("Handoff.Caps.MaxBudgetTokens = %d, want 0", handoff.Caps.MaxBudgetTokens)
			}
			if handoff.Caps.MaxBudgetUSD != 0 {
				t.Errorf("Handoff.Caps.MaxBudgetUSD = %v, want 0", handoff.Caps.MaxBudgetUSD)
			}
		})
	}
}

// The default-work, FixPass==0 cell is the only one that
// renders a review prompt at all, so both subtests set it up. Omitting the
// flag on that same cell must still exit 0: a rendered but unrequested review
// prompt is not an error (issue #2975).
func TestRunAssemblePrompt_ReviewPromptOutput(t *testing.T) {
	orchestratorOnArgs := func(t *testing.T, promptOutput, agentsJSONOutput, handoffOutput string) []string {
		args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
		return args
	}

	t.Run("with review-prompt-output", func(t *testing.T) {
		dir := t.TempDir()
		promptOutput := filepath.Join(dir, "prompt.txt")
		agentsJSONOutput := filepath.Join(dir, "agents.json")
		handoffOutput := filepath.Join(dir, "handoff.json")
		reviewPromptOutput := filepath.Join(dir, "review-prompt.txt")

		args := orchestratorOnArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
		args = append(args, "--review-prompt-output", reviewPromptOutput)

		var stdout bytes.Buffer
		rc := runAssemblePrompt(args, &stdout, io.Discard)
		if rc != 0 {
			t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
		}

		reviewPromptBytes, err := os.ReadFile(reviewPromptOutput)
		if err != nil {
			t.Fatalf("read review prompt output: %v", err)
		}
		if len(reviewPromptBytes) == 0 {
			t.Error("review prompt output is empty, want non-empty")
		}

		handoffBytes, err := os.ReadFile(handoffOutput)
		if err != nil {
			t.Fatalf("read handoff output: %v", err)
		}
		var handoff promptassembly.Handoff
		if err := json.Unmarshal(handoffBytes, &handoff); err != nil {
			t.Fatalf("unmarshal handoff output: %v\n%s", err, handoffBytes)
		}
		if handoff.ReviewPromptFile != reviewPromptOutput {
			t.Errorf("Handoff.ReviewPromptFile = %q, want %q", handoff.ReviewPromptFile, reviewPromptOutput)
		}
	})

	t.Run("without review-prompt-output", func(t *testing.T) {
		dir := t.TempDir()
		promptOutput := filepath.Join(dir, "prompt.txt")
		agentsJSONOutput := filepath.Join(dir, "agents.json")
		handoffOutput := filepath.Join(dir, "handoff.json")

		args := orchestratorOnArgs(t, promptOutput, agentsJSONOutput, handoffOutput)

		var stdout bytes.Buffer
		rc := runAssemblePrompt(args, &stdout, io.Discard)
		if rc != 0 {
			t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
		}

		handoffBytes, err := os.ReadFile(handoffOutput)
		if err != nil {
			t.Fatalf("read handoff output: %v", err)
		}
		var handoff promptassembly.Handoff
		if err := json.Unmarshal(handoffBytes, &handoff); err != nil {
			t.Fatalf("unmarshal handoff output: %v\n%s", err, handoffBytes)
		}
		if handoff.ReviewPromptFile != "" {
			t.Errorf("Handoff.ReviewPromptFile = %q, want empty when --review-prompt-output is omitted", handoff.ReviewPromptFile)
		}
	})
}

// The default-work, FixPass==0 path is the one cell Compose
// reports five passes for (implement, fix and land share the base body;
// review and deltaReview share the review body), so the composition tests
// exercise more than the single-pass legacy default.
func newOrchestratorOnArgs(t *testing.T, promptOutput, agentsJSONOutput, handoffOutput string) []string {
	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	return args
}

// Omitting --composition-output must leave the existing outputs untouched and
// write no composition report at all (issue #3444 slice 3).
func TestRunAssemblePrompt_CompositionOutputOmittedIsANoop(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	var stdout bytes.Buffer
	rc := runAssemblePrompt(coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput), &stdout, io.Discard)
	if rc != 0 {
		t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 3 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("dir entries = %v, want exactly prompt/agents/handoff (no composition report)", names)
	}
}

// A zero Remainder on every pass is how the report says the per-source totals
// reconcile (issue #3444 slice 3).
func TestRunAssemblePrompt_CompositionOutputFile(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")
	compositionOutput := filepath.Join(dir, "composition.json")

	args := newOrchestratorOnArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	args = append(args, "--composition-output", compositionOutput)

	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout, io.Discard)
	if rc != 0 {
		t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	compositionBytes, err := os.ReadFile(compositionOutput)
	if err != nil {
		t.Fatalf("read composition output: %v", err)
	}
	var composition promptassembly.Composition
	if err := json.Unmarshal(compositionBytes, &composition); err != nil {
		t.Fatalf("unmarshal composition output: %v\n%s", err, compositionBytes)
	}
	if len(composition.Passes) == 0 {
		t.Fatal("composition.Passes is empty, want at least one pass")
	}
	for _, p := range composition.Passes {
		if p.Remainder != 0 {
			t.Errorf("pass %q Remainder = %d, want 0", p.Pass, p.Remainder)
		}
	}
	if len(composition.Diffs) == 0 {
		t.Error("composition.Diffs is empty, want at least one pairwise diff")
	}
}

// "-" must write the JSON to the command's own stdout writer, and no file
// literally named "-" may appear (issue #3444 slice 3).
func TestRunAssemblePrompt_CompositionOutputStdout(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	args := newOrchestratorOnArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	args = append(args, "--composition-output", "-")

	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout, io.Discard)
	if rc != 0 {
		t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	var composition promptassembly.Composition
	if err := json.Unmarshal(stdout.Bytes(), &composition); err != nil {
		t.Fatalf("unmarshal composition from stdout: %v\n%s", err, stdout.Bytes())
	}
	if len(composition.Passes) == 0 {
		t.Fatal("composition.Passes is empty, want at least one pass")
	}
	if _, err := os.Stat(filepath.Join(dir, "-")); err == nil {
		t.Error(`a file literally named "-" was written, want stdout only`)
	}
}

// A ":" inside <path> must never be mistaken for the pass separator, with or
// without a pass prefix (issue #3444 slice 3 review finding).
func TestCarriedTextFlag_SetColonInPath(t *testing.T) {
	var f carriedTextFlag
	if err := f.Set("name=/tmp/a:b/c.md"); err != nil {
		t.Fatalf("Set(no pass prefix) error: %v", err)
	}
	if err := f.Set("review:name=/tmp/a:b/c.md"); err != nil {
		t.Fatalf("Set(with pass prefix) error: %v", err)
	}
	want := []carriedTextSpec{
		{pass: "", name: "name", path: "/tmp/a:b/c.md"},
		{pass: "review", name: "name", path: "/tmp/a:b/c.md"},
	}
	if len(f) != len(want) {
		t.Fatalf("f = %+v, want %d specs", f, len(want))
	}
	for i, w := range want {
		if f[i] != w {
			t.Errorf("f[%d] = %+v, want %+v", i, f[i], w)
		}
	}
}

// Set splits on the first "=" only, so an "=" inside <path> stays in the path.
func TestCarriedTextFlag_SetEqualsInPath(t *testing.T) {
	var f carriedTextFlag
	if err := f.Set("name=/tmp/a=b.md"); err != nil {
		t.Fatalf("Set error: %v", err)
	}
	want := carriedTextSpec{pass: "", name: "name", path: "/tmp/a=b.md"}
	if len(f) != 1 || f[0] != want {
		t.Fatalf("f = %+v, want [%+v]", f, want)
	}
}

// Repeated Set calls accumulate in call order, as a repeatable flag.Value must.
func TestCarriedTextFlag_SetAccumulates(t *testing.T) {
	var f carriedTextFlag
	for _, v := range []string{"a=/x", "b=/y", "review:c=/z"} {
		if err := f.Set(v); err != nil {
			t.Fatalf("Set(%q) error: %v", v, err)
		}
	}
	want := []carriedTextSpec{
		{pass: "", name: "a", path: "/x"},
		{pass: "", name: "b", path: "/y"},
		{pass: "review", name: "c", path: "/z"},
	}
	if len(f) != len(want) {
		t.Fatalf("f = %+v, want %d specs", f, len(want))
	}
	for i, w := range want {
		if f[i] != w {
			t.Errorf("f[%d] = %+v, want %+v", i, f[i], w)
		}
	}
}

// An empty <name> must be rejected rather than silently accepted as a blank
// name (issue #3444 slice 3 non-blocking finding).
func TestCarriedTextFlag_SetEmptyNameRejected(t *testing.T) {
	for _, v := range []string{"=/tmp/a.md", "review:=/tmp/a.md"} {
		var f carriedTextFlag
		err := f.Set(v)
		if err == nil {
			t.Fatalf("Set(%q) error = nil, want an error for an empty name", v)
		}
		if !strings.Contains(err.Error(), v) {
			t.Errorf("Set(%q) error = %q, want it to mention the malformed value", v, err.Error())
		}
	}
}

// An empty pass prefix matches no pass kind, so Set must reject it rather than
// fall back to "every pass".
func TestCarriedTextFlag_SetEmptyPassRejected(t *testing.T) {
	var f carriedTextFlag
	v := ":name=/tmp/a.md"
	err := f.Set(v)
	if err == nil {
		t.Fatalf("Set(%q) error = nil, want an error for an empty pass prefix", v)
	}
	if !strings.Contains(err.Error(), v) {
		t.Errorf("Set(%q) error = %q, want it to mention the malformed value", v, err.Error())
	}
}

// The nil *carriedTextFlag case covers String's own nil guard, which the flag
// package hits when it prints defaults for an unset value.
func TestCarriedTextFlag_String(t *testing.T) {
	var empty carriedTextFlag
	if got := empty.String(); got != "" {
		t.Errorf("empty.String() = %q, want %q", got, "")
	}

	f := carriedTextFlag{
		{pass: "", name: "a", path: "/x"},
		{pass: "review", name: "b", path: "/y"},
	}
	want := "a=/x,review:b=/y"
	if got := f.String(); got != want {
		t.Errorf("f.String() = %q, want %q", got, want)
	}

	var nilFlag *carriedTextFlag
	if got := nilFlag.String(); got != "" {
		t.Errorf("nilFlag.String() = %q, want %q", got, "")
	}
}

// --composition-carried must land its named block on the right passes, only
// the named pass with a prefix and every pass without one, and count the block
// in that pass's own Bytes total (issue #3444 slice 3).
func TestRunAssemblePrompt_CompositionCarried(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")
	compositionOutput := filepath.Join(dir, "composition.json")

	everyPassFile := filepath.Join(dir, "every-pass.txt")
	if err := os.WriteFile(everyPassFile, []byte("every pass block"), 0o644); err != nil {
		t.Fatalf("write every-pass carried file: %v", err)
	}
	implementOnlyFile := filepath.Join(dir, "implement-only.txt")
	if err := os.WriteFile(implementOnlyFile, []byte("implement only block"), 0o644); err != nil {
		t.Fatalf("write implement-only carried file: %v", err)
	}

	args := newOrchestratorOnArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	args = append(args,
		"--composition-output", compositionOutput,
		"--composition-carried", "every="+everyPassFile,
		"--composition-carried", "implement:only="+implementOnlyFile,
	)

	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout, io.Discard)
	if rc != 0 {
		t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	compositionBytes, err := os.ReadFile(compositionOutput)
	if err != nil {
		t.Fatalf("read composition output: %v", err)
	}
	var composition promptassembly.Composition
	if err := json.Unmarshal(compositionBytes, &composition); err != nil {
		t.Fatalf("unmarshal composition output: %v\n%s", err, compositionBytes)
	}
	if len(composition.Passes) == 0 {
		t.Fatal("composition.Passes is empty, want at least one pass")
	}

	for _, p := range composition.Passes {
		hasEvery, hasOnly, everyBytes, onlyBytes := false, false, 0, 0
		for _, s := range p.Sources {
			if s.Kind != promptassembly.SourceCarried {
				continue
			}
			switch s.Name {
			case "every":
				hasEvery, everyBytes = true, s.Bytes
			case "only":
				hasOnly, onlyBytes = true, s.Bytes
			}
		}
		if !hasEvery {
			t.Errorf("pass %q sources lack the every-pass carried block, want it on every pass", p.Pass)
		} else if everyBytes != len("every pass block") {
			t.Errorf("pass %q every-pass carried bytes = %d, want %d", p.Pass, everyBytes, len("every pass block"))
		}

		wantOnly := p.Pass == "implement"
		if hasOnly != wantOnly {
			t.Errorf("pass %q has the implement-only carried block = %v, want %v", p.Pass, hasOnly, wantOnly)
		} else if wantOnly && onlyBytes != len("implement only block") {
			t.Errorf("pass %q implement-only carried bytes = %d, want %d", p.Pass, onlyBytes, len("implement only block"))
		}
	}
}

// A --composition-carried value missing "=" must fail loudly instead of
// silently dropping the block (issue #3444 slice 3).
func TestRunAssemblePrompt_CompositionCarriedMalformedValue(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")
	compositionOutput := filepath.Join(dir, "composition.json")

	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	args = append(args, "--composition-output", compositionOutput, "--composition-carried", "no-equals-sign")

	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout, io.Discard)
	if rc == 0 {
		t.Fatal("runAssemblePrompt exit = 0, want non-zero for a malformed --composition-carried value")
	}
	if !strings.Contains(stdout.String(), "composition-carried") {
		t.Errorf("stdout = %q, want it to mention composition-carried", stdout.String())
	}
}

// An unreadable --composition-carried file must fail loudly, naming the file
// on the output writer (issue #3444 slice 3).
func TestRunAssemblePrompt_CompositionCarriedUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")
	compositionOutput := filepath.Join(dir, "composition.json")

	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	args = append(args, "--composition-output", compositionOutput,
		"--composition-carried", "missing="+filepath.Join(dir, "does-not-exist.txt"))

	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout, io.Discard)
	if rc == 0 {
		t.Fatal("runAssemblePrompt exit = 0, want non-zero for an unreadable --composition-carried file")
	}
	if !strings.Contains(stdout.String(), "does-not-exist.txt") {
		t.Errorf("stdout = %q, want it to mention the unreadable carried file", stdout.String())
	}
}

// --fragments-output writes Result.Fragments one name per line so a harness
// can pin it per golden cell (issue #3838); omitted, it writes nothing.
func TestRunAssemblePrompt_FragmentsOutput(t *testing.T) {
	t.Run("with fragments-output", func(t *testing.T) {
		dir := t.TempDir()
		fragmentsOutput := filepath.Join(dir, "fragments.txt")
		args := coveredCellArgs(t, filepath.Join(dir, "prompt.txt"), filepath.Join(dir, "agents.json"), filepath.Join(dir, "handoff.json"))
		args = append(args, "--fragments-output", fragmentsOutput)

		var stdout bytes.Buffer
		if rc := runAssemblePrompt(args, &stdout, io.Discard); rc != 0 {
			t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
		}

		got, err := os.ReadFile(fragmentsOutput)
		if err != nil {
			t.Fatalf("read fragments output: %v", err)
		}
		if len(got) == 0 || got[len(got)-1] != '\n' {
			t.Fatalf("fragments output = %q, want non-empty with trailing newline", got)
		}
		names := strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")
		for i, n := range names {
			if n == "" {
				t.Errorf("fragments output line %d is empty: %q", i, got)
			}
			if i > 0 && names[i-1] >= n {
				t.Errorf("fragments output not sorted and de-duplicated at line %d: %q", i, got)
			}
		}
	})

	t.Run("without fragments-output", func(t *testing.T) {
		dir := t.TempDir()
		args := coveredCellArgs(t, filepath.Join(dir, "prompt.txt"), filepath.Join(dir, "agents.json"), filepath.Join(dir, "handoff.json"))

		var stdout bytes.Buffer
		if rc := runAssemblePrompt(args, &stdout, io.Discard); rc != 0 {
			t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir: %v", err)
		}
		if len(entries) != 3 {
			t.Errorf("dir has %d entries, want exactly prompt/agents/handoff", len(entries))
		}
	})
}

// runStateComposition runs assemble-prompt with --composition-output plus
// extra args and returns the decoded report; the run must exit 0.
func runStateComposition(t *testing.T, extra ...string) promptassembly.Composition {
	t.Helper()
	composition, _ := runStateCompositionEnv(t, nil, extra...)
	return composition
}

// runStateCompositionEnv is runStateComposition with env overrides applied on
// top of the covered cell, returning the run's stderr alongside.
func runStateCompositionEnv(t *testing.T, env map[string]string, extra ...string) (promptassembly.Composition, string) {
	t.Helper()
	dir := t.TempDir()
	compositionOutput := filepath.Join(dir, "composition.json")
	args := coveredCellArgs(t, filepath.Join(dir, "prompt.txt"), filepath.Join(dir, "agents.json"), filepath.Join(dir, "handoff.json"))
	for k, v := range env {
		t.Setenv(k, v)
	}
	args = append(args, "--composition-output", compositionOutput)
	args = append(args, extra...)

	var stdout, stderr bytes.Buffer
	if rc := runAssemblePrompt(args, &stdout, &stderr); rc != 0 {
		t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q stderr=%q)", rc, stdout.String(), stderr.String())
	}
	raw, err := os.ReadFile(compositionOutput)
	if err != nil {
		t.Fatalf("read composition output: %v", err)
	}
	var composition promptassembly.Composition
	if err := json.Unmarshal(raw, &composition); err != nil {
		t.Fatalf("unmarshal composition output: %v\n%s", err, raw)
	}
	return composition, stderr.String()
}

func writeRunState(t *testing.T, state runstate.RunState) string {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal run state: %v", err)
	}
	path := filepath.Join(t.TempDir(), "run-state.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write run state: %v", err)
	}
	return path
}

// carriedBytes returns the Bytes of the named carried source on pass, and
// whether the pass has one.
func carriedBytes(composition promptassembly.Composition, pass, name string) (int, bool) {
	for _, p := range composition.Passes {
		if p.Pass != pass {
			continue
		}
		for _, s := range p.Sources {
			if s.Kind == promptassembly.SourceCarried && s.Name == name {
				return s.Bytes, true
			}
		}
	}
	return 0, false
}

// --run-state derives the handoff block for implement/fix/land, and
// carries nothing for the review passes when the state has no review data.
func TestRunAssemblePrompt_RunStateHandoff(t *testing.T) {
	state := runstate.RunState{
		LastVerdict:     "BLOCK",
		ScoutBriefPath:  filepath.Join(t.TempDir(), "never-written-brief.md"),
		PassSummaryPath: "/tmp/summary.md",
	}
	want := len(seedblock.Handoff(state))
	if want == 0 {
		t.Fatal("test state yields an empty handoff block")
	}
	composition := runStateComposition(t, "--run-state", writeRunState(t, state))

	for _, pass := range []string{"implement", "fix", "land"} {
		got, ok := carriedBytes(composition, pass, runStateHandoffBlock)
		if !ok {
			t.Errorf("pass %q lacks the %s source", pass, runStateHandoffBlock)
		} else if got != want {
			t.Errorf("pass %q %s bytes = %d, want %d", pass, runStateHandoffBlock, got, want)
		}
	}
	for _, pass := range []string{"review", "delta-review"} {
		for _, name := range []string{runStateHandoffBlock, runStateReviewBlock} {
			if _, ok := carriedBytes(composition, pass, name); ok {
				t.Errorf("pass %q carries %q, want none", pass, name)
			}
		}
	}
}

// --run-state derives the review block for the review pass; delta-review
// gets nothing because its seeder needs data RunState does not hold.
func TestRunAssemblePrompt_RunStateReview(t *testing.T) {
	state := runstate.RunState{
		ReviewFindings:       "VERDICT: BLOCK\n- fix it",
		ReviewedCommitAnchor: "0123456789abcdef0123456789abcdef01234567",
	}
	want := len(seedblock.Review(state))
	if want == 0 {
		t.Fatal("test state yields an empty review block")
	}
	composition := runStateComposition(t, "--run-state", writeRunState(t, state))

	got, ok := carriedBytes(composition, "review", runStateReviewBlock)
	if !ok {
		t.Errorf("review pass lacks the %s source", runStateReviewBlock)
	} else if got != want {
		t.Errorf("review %s bytes = %d, want %d", runStateReviewBlock, got, want)
	}
	if _, ok := carriedBytes(composition, "delta-review", runStateReviewBlock); ok {
		t.Errorf("delta-review carries %s, want none", runStateReviewBlock)
	}
}

// Derived run-state blocks precede manual --composition-carried ones.
func TestRunAssemblePrompt_RunStateBeforeManualCarried(t *testing.T) {
	manual := filepath.Join(t.TempDir(), "manual.txt")
	if err := os.WriteFile(manual, []byte("manual"), 0o644); err != nil {
		t.Fatalf("write manual carried file: %v", err)
	}
	state := runstate.RunState{LastVerdict: "BLOCK"}
	composition := runStateComposition(t,
		"--run-state", writeRunState(t, state),
		"--composition-carried", "implement:manual="+manual)

	for _, p := range composition.Passes {
		if p.Pass != "implement" {
			continue
		}
		var names []string
		for _, s := range p.Sources {
			if s.Kind == promptassembly.SourceCarried {
				names = append(names, s.Name)
			}
		}
		if strings.Join(names, ",") != runStateHandoffBlock+",manual" {
			t.Errorf("implement carried sources = %v, want [%s manual]", names, runStateHandoffBlock)
		}
	}
}

// A missing --run-state file reads as the zero state: no run-state sources.
func TestRunAssemblePrompt_RunStateMissingFile(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.json")
	composition, stderr := runStateCompositionEnv(t, nil, "--run-state", absent)
	if want := "--run-state file " + absent + " not found"; !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want a notice containing %q", stderr, want)
	}
	for _, p := range composition.Passes {
		for _, name := range []string{runStateHandoffBlock, runStateReviewBlock} {
			if _, ok := carriedBytes(composition, p.Pass, name); ok {
				t.Errorf("pass %q carries %q from a missing file, want none", p.Pass, name)
			}
		}
	}
}

// The notice must not corrupt a composition report streamed to stdout.
func TestRunAssemblePrompt_RunStateMissingFileKeepsStdoutJSON(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(dir, "absent.json")
	args := coveredCellArgs(t, filepath.Join(dir, "prompt.txt"), filepath.Join(dir, "agents.json"), filepath.Join(dir, "handoff.json"))
	args = append(args, "--composition-output", "-", "--run-state", absent)

	var stdout, stderr bytes.Buffer
	if rc := runAssemblePrompt(args, &stdout, &stderr); rc != 0 {
		t.Fatalf("exit = %d, want 0 (stdout=%q stderr=%q)", rc, stdout.String(), stderr.String())
	}
	var composition promptassembly.Composition
	if err := json.Unmarshal(stdout.Bytes(), &composition); err != nil {
		t.Fatalf("stdout is not a clean composition: %v\n%s", err, stdout.String())
	}
	if want := "--run-state file " + absent + " not found"; !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr = %q, want a notice containing %q", stderr.String(), want)
	}
}

func TestRunAssemblePrompt_RunStateCorruptFile(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, "run-state.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write corrupt run state: %v", err)
	}
	args := coveredCellArgs(t, filepath.Join(dir, "prompt.txt"), filepath.Join(dir, "agents.json"), filepath.Join(dir, "handoff.json"))

	t.Run("with composition-output", func(t *testing.T) {
		var stdout bytes.Buffer
		a := append(append([]string{}, args...), "--composition-output", filepath.Join(dir, "c.json"), "--run-state", corrupt)
		if rc := runAssemblePrompt(a, &stdout, io.Discard); rc != 1 {
			t.Fatalf("exit = %d, want 1", rc)
		}
		if !strings.Contains(stdout.String(), "--run-state") {
			t.Errorf("stdout = %q, want it to mention --run-state", stdout.String())
		}
	})
	t.Run("without composition-output", func(t *testing.T) {
		var stdout bytes.Buffer
		a := append(append([]string{}, args...), "--run-state", corrupt)
		if rc := runAssemblePrompt(a, &stdout, io.Discard); rc != 0 {
			t.Fatalf("exit = %d, want 0 (stdout=%q)", rc, stdout.String())
		}
	})
}

// A warm FIX_PASS>0 cell has no review prompt, so the orchestrator's legacy
// loop seeds its single legacy pass with the handoff block.
func TestRunAssemblePrompt_RunStateLegacyCell(t *testing.T) {
	state := runstate.RunState{LastVerdict: "BLOCK", PassSummaryPath: "/tmp/summary.md"}
	want := len(seedblock.Handoff(state))
	if want == 0 {
		t.Fatal("test state yields an empty handoff block")
	}
	composition, _ := runStateCompositionEnv(t, map[string]string{"FIX_PASS": "1"}, "--run-state", writeRunState(t, state))

	if len(composition.Passes) != 1 || composition.Passes[0].Pass != "legacy" {
		t.Fatalf("passes = %+v, want the single legacy pass", composition.Passes)
	}
	got, ok := carriedBytes(composition, "legacy", runStateHandoffBlock)
	if !ok {
		t.Fatalf("legacy pass lacks the %s source", runStateHandoffBlock)
	}
	if got != want {
		t.Errorf("legacy %s bytes = %d, want %d", runStateHandoffBlock, got, want)
	}
}

// runStateCarried maps every rendered pass to the block the orchestrator seeds
// it with: review gets the review block, delta-review nothing, all others the
// handoff block.
func TestRunStateCarried(t *testing.T) {
	state := runstate.RunState{
		LastVerdict:          "BLOCK",
		ReviewFindings:       "VERDICT: BLOCK\n- fix it",
		ReviewedCommitAnchor: "0123456789abcdef0123456789abcdef01234567",
	}
	handoff, review := seedblock.Handoff(state), seedblock.Review(state)
	if handoff == "" || review == "" {
		t.Fatal("test state must yield both blocks")
	}
	got := runStateCarried(state, []string{"implement", "review", "delta-review", "legacy", "research"})
	want := []promptassembly.CarriedText{
		{Pass: "implement", Name: runStateHandoffBlock, Text: handoff},
		{Pass: "review", Name: runStateReviewBlock, Text: review},
		{Pass: "legacy", Name: runStateHandoffBlock, Text: handoff},
		{Pass: "research", Name: runStateHandoffBlock, Text: handoff},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("runStateCarried = %+v, want %+v", got, want)
	}
}
