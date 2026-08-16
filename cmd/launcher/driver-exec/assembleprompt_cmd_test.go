package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/promptassembly"
)

// promptsDirForTest is the real templates/default/prompts tree, resolved
// relative to this package directory (cmd/launcher/driver-exec), the same
// convention promptassembly's own assemble_test.go uses for its
// package-relative path.
const promptsDirForTest = "../../../templates/default/prompts"

// registryPathForTest reuses promptassembly's own testdata registry fixture
// rather than duplicating it by hand.
const registryPathForTest = "../internal/promptassembly/testdata/registry.json"

// validateMarkersRegistryPathForTest reuses promptassembly's own testdata
// validateMarkers registry fixture (slice A, issue #2356) rather than
// duplicating it by hand.
const validateMarkersRegistryPathForTest = "../internal/promptassembly/testdata/validate-markers.json"

// coveredCellArgs puts runAssemblePrompt's Env squarely in
// promptassembly.Assemble's covered cell (see promptassembly's
// checkCoveredCell, which as of issue #2540 checks only dispatch kind
// "work"): github tracker, github forge, a read-write box, dispatch kind
// "work", a fresh box (fix-pass 0), the orchestrator off, and every skill
// baked. Since issue #2979, the Box-env-sourced Env fields
// (promptassembly.EnvFromEnviron, boxenv_gen.go) reach runAssemblePrompt via
// t.Setenv rather than a CLI flag -- every field is set explicitly here
// (even to "" for a false/empty default) so a leftover value in the test
// process's own environment can never leak into a covered-cell run. The
// remaining flag set (skill-baked bools, prompts-dir, skills-found, and the
// required registry/output paths) is still a genuine CLI flag and is
// returned for the caller to pass to runAssemblePrompt.
func coveredCellArgs(t *testing.T, promptOutput, agentsJSONOutput, handoffOutput string) []string {
	t.Helper()
	t.Setenv("ORCHESTRATOR_ENABLED", "")
	t.Setenv("AGENTS_JSON_TEMPLATE", "")
	t.Setenv("BOX_FILER_ENABLED", "")
	t.Setenv("BOX_WORKER_PROVISIONED", "")
	t.Setenv("BOX_REVIEW_LOOP_INLINE", "")
	t.Setenv("BOX_REVIEW_LOOP_ORCHESTRATOR", "")
	t.Setenv("ISSUE_TRACKER", "github")
	t.Setenv("BOX_TRACKER_AXIS_READ", "")
	t.Setenv("BOX_TRACKER_AXIS_WRITE", "")
	t.Setenv("BOX_TRACKER_AXIS_FILER", "")
	t.Setenv("BOX_WRITE_ENABLED", "1")
	t.Setenv("LOCAL_ISSUE_REFERENCE", "")
	t.Setenv("CODE_FORGE", "github")
	t.Setenv("BOX_FORGE_BACKEND", "")
	t.Setenv("DISPATCH_KIND", "work")
	t.Setenv("SELF_CONTAINED", "")
	t.Setenv("FIX_PASS", "0")
	t.Setenv("RESUME_AFTER_HOLD", "")
	t.Setenv("AUTO_FORMAT", "")
	t.Setenv("AUTO_LINT", "")
	t.Setenv("CI_FAILURE_SUMMARY", "")
	t.Setenv("ISSUE_NUMBER", "2349")
	t.Setenv("ISSUE_TITLE", "Add assemble-prompt CLI verb")
	t.Setenv("BRANCH", "agent/issue-2349")
	t.Setenv("BASE_BRANCH", "main")
	t.Setenv("IN_PROGRESS_LABEL", "agent-in-progress")
	t.Setenv("COMPLETE_LABEL", "agent-complete")
	t.Setenv("RUN_NONCE", "run-nonce-abc123")
	t.Setenv("RESEARCH_STATUS_ENUM", "")
	return []string{
		"--caveman-skill-baked=true",
		"--tdd-skill-baked=true",
		"--commit-skill-baked=true",
		"--code-review-skill-baked=true",
		"--auto-format-skill-baked=true",
		"--auto-lint-skill-baked=true",
		"--prompts-dir", promptsDirForTest,
		"--skills-found", "caveman, tdd, commit, code-review",
		"--registry", registryPathForTest,
		"--validate-markers-registry", validateMarkersRegistryPathForTest,
		"--prompt-output", promptOutput,
		"--agents-json-output", agentsJSONOutput,
		"--handoff-output", handoffOutput,
	}
}

// TestRunAssemblePrompt_CoveredCellWritesOutputs verifies the assemble-prompt
// subcommand's flag parsing reaches promptassembly.Assemble with the right
// Env/Registry and writes all three output files (issue #2349).
func TestRunAssemblePrompt_CoveredCellWritesOutputs(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	var stdout bytes.Buffer
	rc := runAssemblePrompt(coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput), &stdout)
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
		Invoker          string
		ReviewPromptFile string
		ReviewModel      string
	}
	if err := json.Unmarshal(handoffBytes, &handoff); err != nil {
		t.Fatalf("unmarshal handoff output: %v\n%s", err, handoffBytes)
	}
	if handoff.Invoker != "driver-exec" {
		t.Errorf("handoff.Invoker = %q, want driver-exec", handoff.Invoker)
	}
}

// TestRunAssemblePrompt_UnsupportedCellReturnsNonZero verifies an Env
// outside promptassembly.Assemble's covered cell (here, a dispatch kind
// value that is neither "work" nor "research" -- the one axis
// checkCoveredCell still validates as of issue #2540, since it has no
// eval-time or launcher-side guard the way IssueTracker/CodeForge do) is
// reported as a CLI failure, not a panic.
func TestRunAssemblePrompt_UnsupportedCellReturnsNonZero(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	t.Setenv("DISPATCH_KIND", "bogus-kind")

	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout)
	if rc == 0 {
		t.Fatal("runAssemblePrompt exit = 0, want non-zero for an unsupported cell")
	}
	if !strings.Contains(stdout.String(), "bogus-kind") {
		t.Errorf("stdout = %q, want it to mention the rejected dispatch kind bogus-kind", stdout.String())
	}
}

// TestRunAssemblePrompt_MissingRequiredFlagReturnsNonZero verifies a missing
// -handoff-output fails loudly (exit 1) instead of running Assemble against
// a zero-value output path.
func TestRunAssemblePrompt_MissingRequiredFlagReturnsNonZero(t *testing.T) {
	dir := t.TempDir()
	var stdout bytes.Buffer
	rc := runAssemblePrompt([]string{
		"--registry", registryPathForTest,
		"--prompt-output", filepath.Join(dir, "prompt.txt"),
		"--agents-json-output", filepath.Join(dir, "agents.json"),
	}, &stdout)
	if rc == 0 {
		t.Fatal("runAssemblePrompt exit = 0, want non-zero for a missing -handoff-output")
	}
}

// TestRunAssemblePrompt_ValidateMarkersRegistryRequired verifies a missing
// -validate-markers-registry flag fails loudly (exit 1) instead of running
// Assemble/Validate against a zero-value registry path (issue #2356).
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
	rc := runAssemblePrompt(filtered, &stdout)
	if rc == 0 {
		t.Fatal("runAssemblePrompt exit = 0, want non-zero for a missing -validate-markers-registry")
	}
	if !strings.Contains(stdout.String(), "validate-markers-registry") {
		t.Errorf("stdout = %q, want it to mention validate-markers-registry", stdout.String())
	}
}

// researchPromptDirLackingSpindriftComment builds a temp prompts dir whose
// research-prompt.md renders without any SPINDRIFT_COMMENT marker (and
// without a fragments subdir at all -- Assemble swallows a missing fragment
// file as an empty render, see assemble.go's fragment loop), so the
// readOnlyResearch validate row's marker is guaranteed missing.
func researchPromptDirLackingSpindriftComment(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	content := "# TASK\n\nResearch issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}\n\nNo verdict marker here.\n"
	if err := os.WriteFile(filepath.Join(dir, "research-prompt.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write research-prompt.md: %v", err)
	}
	return dir
}

// issuePromptDirLackingSpindriftPRIntent builds a temp prompts dir whose
// issue-prompt.md renders without any SPINDRIFT_PR_INTENT marker (and
// without a fragments subdir at all), so the boxAccessReadOnly validate
// row's marker is guaranteed missing.
func issuePromptDirLackingSpindriftPRIntent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	content := "# TASK\n\nWork issue #${ISSUE_NUMBER}: ${ISSUE_TITLE}\n\nNo PR-intent marker here.\n"
	if err := os.WriteFile(filepath.Join(dir, "issue-prompt.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write issue-prompt.md: %v", err)
	}
	return dir
}

// replaceArg overwrites, or appends, flag's value in args, returning the new
// slice -- a small test helper so each Validate-wiring test can start from
// coveredCellArgs and move only the axes it needs off the covered cell. It
// drops any existing occurrence of flag, in either the two-token
// ("--flag", "value") or single-token ("--flag=value") form
// coveredCellArgs mixes (string flags use the former, bool flags the
// latter -- Go's flag package requires "=" for an explicit bool value,
// since a bare "--flag next-token" reads next-token as a positional
// argument and halts flag parsing entirely), and always re-adds it as a
// single "--flag=value" token, which both flag kinds accept.
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

// TestRunAssemblePrompt_ValidatorRejectBlocksOutputs verifies a reject-severity
// validate row whose gate is active and marker missing (readOnlyResearch: a
// research, read-only cell whose rendered prompt lacks SPINDRIFT_COMMENT)
// makes runAssemblePrompt return non-zero and write none of the three
// output files -- the Driver must never run against an unmet contract
// (issue #2356).
func TestRunAssemblePrompt_ValidatorRejectBlocksOutputs(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	t.Setenv("DISPATCH_KIND", "research")
	t.Setenv("BOX_WRITE_ENABLED", "")
	args = replaceArg(args, "--prompts-dir", researchPromptDirLackingSpindriftComment(t))

	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout)
	if rc == 0 {
		t.Fatalf("runAssemblePrompt exit = 0, want non-zero for a reject-gate missing marker (stdout=%q)", stdout.String())
	}
	if !strings.Contains(stdout.String(), "SPINDRIFT_COMMENT") {
		t.Errorf("stdout = %q, want it to mention SPINDRIFT_COMMENT", stdout.String())
	}

	for _, p := range []string{promptOutput, agentsJSONOutput, handoffOutput} {
		if info, err := os.Stat(p); err == nil {
			t.Errorf("output file %s exists (size %d), want it never written on reject", p, info.Size())
		} else if !os.IsNotExist(err) {
			t.Errorf("stat %s: %v", p, err)
		}
	}
}

// TestRunAssemblePrompt_ValidatorWarnStillWritesOutputs verifies a
// warn-severity validate row whose gate is active and marker missing
// (boxAccessReadOnly: a read-only, non-research work cell whose rendered
// prompt lacks SPINDRIFT_PR_INTENT) still lets runAssemblePrompt succeed and
// write all three output files -- a warn is advisory, never blocks the
// Driver (issue #2356).
func TestRunAssemblePrompt_ValidatorWarnStillWritesOutputs(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	t.Setenv("BOX_WRITE_ENABLED", "")
	args = replaceArg(args, "--prompts-dir", issuePromptDirLackingSpindriftPRIntent(t))

	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout)
	if rc != 0 {
		t.Fatalf("runAssemblePrompt exit = %d, want 0 for a warn-gate missing marker (stdout=%q)", rc, stdout.String())
	}
	if !strings.Contains(stdout.String(), "SPINDRIFT_PR_INTENT") {
		t.Errorf("stdout = %q, want it to mention SPINDRIFT_PR_INTENT", stdout.String())
	}

	// agentsJSONOutput is only Stat-checked, not size-checked: with no
	// AGENTS_JSON_TEMPLATE configured (as here) Assemble's own AgentsJSON
	// legitimately renders empty -- TestRunAssemblePrompt_CoveredCellWritesOutputs
	// makes the same distinction. promptOutput/handoffOutput are always
	// non-empty in the covered cell.
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
}

// TestRunAssemblePrompt_TrackerAxisEnvVarsReachGates verifies the
// BOX_TRACKER_AXIS_READ/BOX_TRACKER_AXIS_WRITE/BOX_TRACKER_AXIS_FILER env
// vars reach promptassembly.Env's
// TrackerAxisRead/TrackerAxisWrite/TrackerAxisFiler fields (issue #2533
// slice 2): setting BOX_TRACKER_AXIS_READ/WRITE/FILER=FORGEJO fires the
// ISSUE_TRACKER_FORGEJO/ISSUE_TRACKER_FORGEJO_READWRITE gates
// (gates_tracker.go reads the axis fields directly, no longer re-deriving
// them from ISSUE_TRACKER) -- even though ISSUE_TRACKER itself is left at
// "github", since checkCoveredCell no longer re-validates IssueTracker
// (issue #2540) and the axis fields are the sole gate input.
// Issue #2547 re-points issue-read-github.md/issue-read-forgejo.md at the
// same frozen /issue-snapshot.md, so they no longer distinguish trackers;
// this asserts that convergence directly, then proves the FORGEJO axis
// still reaches a gate that does distinguish trackers --
// ISSUE_TRACKER_FORGEJO_READWRITE's issue-blocked-comment-forgejo.md
// ("fj issue comment"), unaffected by issue #2547, instead of
// issue-blocked-comment-github.md's "gh issue comment".
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
	rc := runAssemblePrompt(args, &stdout)
	if rc != 0 {
		t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}

	promptBytes, err := os.ReadFile(promptOutput)
	if err != nil {
		t.Fatalf("read prompt output: %v", err)
	}
	prompt := string(promptBytes)
	if !strings.Contains(prompt, "cat /issue-snapshot.md") {
		t.Errorf("prompt does not contain %q (issue-read-forgejo.md's frozen snapshot read), want it rendered when --tracker-axis-read=FORGEJO", "cat /issue-snapshot.md")
	}
	if strings.Contains(prompt, "gh issue view") || strings.Contains(prompt, "fj issue view") {
		t.Errorf("prompt contains a live gh/fj issue view call, want only the frozen snapshot read when --tracker-axis-read=FORGEJO")
	}
	if !strings.Contains(prompt, "fj issue comment") {
		t.Errorf("prompt does not contain %q (forgejo blocked-comment fragment), want it rendered when --tracker-axis-write=FORGEJO", "fj issue comment")
	}
	if strings.Contains(prompt, "gh issue comment") {
		t.Errorf("prompt contains %q (github blocked-comment fragment), want it absent when --tracker-axis-write=FORGEJO", "gh issue comment")
	}
}

// TestRunAssemblePrompt_ForgeBackendEnvVarReachesGates verifies the
// BOX_FORGE_BACKEND env var reaches promptassembly.Env.ForgeBackend
// (gates_access_forge.go reads it directly, no longer re-deriving it from
// CODE_FORGE, issue #2533 slice 2): setting BOX_FORGE_BACKEND=FORGEJO fires
// FIX_CI_READ_FORGEJO instead of FIX_CI_READ_GH, even with CODE_FORGE
// left at "github".
func TestRunAssemblePrompt_ForgeBackendEnvVarReachesGates(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	t.Setenv("FIX_PASS", "1")
	t.Setenv("BOX_FORGE_BACKEND", "FORGEJO")

	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout)
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

// Distinctive, present-in-source literal strings used to prove each of the
// four roster/review-loop flags reaches its own fragment and no other --
// picked by reading the actual fragment files under
// templates/default/prompts/fragments/ rather than guessed, and confirmed
// (via a grep sweep across templates/default/prompts/) to appear nowhere
// else in the prompt template tree, so a Contains/! Contains check on the
// rendered prompt is unambiguous:
//   - filerEnabledMarker is file-issues-direct.md's/file-issues-relay.md's
//     shared heading (both render only when FILER_ENABLED gates a direct or
//     relay write mechanism in gates_tracker.go -- either fork requires
//     e.FilerEnabled, so the heading's presence pins FilerEnabled true
//     regardless of which fork the covered cell's BOX_WRITE_ENABLED/
//     ORCHESTRATOR combination picks).
//   - workerProvisionedMarker is coordinator.md's (WORKER_PROVISIONED gate).
//   - reviewLoopInlineMarker is review-loop-inline.md's (REVIEW_LOOP_INLINE
//     gate).
//   - reviewLoopOrchestratorMarker is review-loop-orchestrator.md's
//     (REVIEW_LOOP_ORCHESTRATOR gate).
const (
	filerEnabledMarker           = "# FILE ISSUES"
	workerProvisionedMarker      = "rather than editing the source yourself"
	reviewLoopInlineMarker       = "Do NOT review inline"
	reviewLoopOrchestratorMarker = "Review is handled by the orchestrator as a separate"
)

// assemblePromptForTest runs runAssemblePrompt against args (built from
// coveredCellArgs and mutated via replaceArg by callers) and returns the
// rendered prompt output's content as a string, failing the test on a
// non-zero exit or an unreadable output file.
func assemblePromptForTest(t *testing.T, dir string, args []string) string {
	t.Helper()
	var stdout bytes.Buffer
	rc := runAssemblePrompt(args, &stdout)
	if rc != 0 {
		t.Fatalf("runAssemblePrompt exit = %d, want 0 (stdout=%q)", rc, stdout.String())
	}
	promptBytes, err := os.ReadFile(filepath.Join(dir, "prompt.txt"))
	if err != nil {
		t.Fatalf("read prompt output: %v", err)
	}
	return string(promptBytes)
}

// TestRunAssemblePrompt_FilerEnabledEnvVarReachesPrompt verifies
// BOX_FILER_ENABLED reaches promptassembly.Env.FilerEnabled and, through it,
// the FILER_ENABLED gate: on, the rendered prompt carries the filer's FILE
// ISSUES heading; off (coveredCellArgs' default -- unset there), it does not
// (issue #2533 slice 2).
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

// TestRunAssemblePrompt_WorkerProvisionedEnvVarReachesPrompt verifies
// BOX_WORKER_PROVISIONED reaches promptassembly.Env.WorkerProvisioned and,
// through it, the WORKER_PROVISIONED gate: on, the rendered prompt carries
// coordinator.md's marker text; off, it does not (issue #2533 slice 2).
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

// TestRunAssemblePrompt_ReviewLoopEnvVarsReachPrompt verifies
// BOX_REVIEW_LOOP_INLINE/BOX_REVIEW_LOOP_ORCHESTRATOR reach
// promptassembly.Env.ReviewLoopInline/Env.ReviewLoopOrchestrator and, through
// them, the REVIEW_LOOP_INLINE/REVIEW_LOOP_ORCHESTRATOR gates: each
// combination renders exactly its own fragment's marker and never the
// other's, proving the two env vars aren't swapped or aliased (issue #2533
// slice 2).
func TestRunAssemblePrompt_ReviewLoopEnvVarsReachPrompt(t *testing.T) {
	cases := []struct {
		name         string
		inline       bool
		orchestrator bool
	}{
		{"inline on, orchestrator off", true, false},
		{"inline off, orchestrator on", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			promptOutput := filepath.Join(dir, "prompt.txt")
			agentsJSONOutput := filepath.Join(dir, "agents.json")
			handoffOutput := filepath.Join(dir, "handoff.json")

			args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
			t.Setenv("BOX_REVIEW_LOOP_INLINE", presenceEnvValue(c.inline))
			t.Setenv("BOX_REVIEW_LOOP_ORCHESTRATOR", presenceEnvValue(c.orchestrator))

			prompt := assemblePromptForTest(t, dir, args)
			if gotInline := strings.Contains(prompt, reviewLoopInlineMarker); gotInline != c.inline {
				t.Errorf("prompt contains %q = %v, want %v (BOX_REVIEW_LOOP_INLINE=%v)", reviewLoopInlineMarker, gotInline, c.inline, c.inline)
			}
			if gotOrchestrator := strings.Contains(prompt, reviewLoopOrchestratorMarker); gotOrchestrator != c.orchestrator {
				t.Errorf("prompt contains %q = %v, want %v (BOX_REVIEW_LOOP_ORCHESTRATOR=%v)", reviewLoopOrchestratorMarker, gotOrchestrator, c.orchestrator, c.orchestrator)
			}
		})
	}
}

// TestRunAssemblePrompt_FilerAndWorkerEnvVarsNotCrossWired proves
// BOX_FILER_ENABLED and BOX_WORKER_PROVISIONED reach their own, distinct Env
// fields rather than being crossed (e.g. BOX_FILER_ENABLED accidentally
// wired to Env.WorkerProvisioned, or vice versa): with exactly one of the
// two env vars on, only that one's own marker appears in the rendered
// prompt, never the other's -- a wiring bug that swapped the two
// destinations would show both markers together or neither, failing this
// test (issue #2533 slice 2, the review finding this pins closed).
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

// presenceEnvValue renders b the way a presence-kind Env field
// (os.Getenv(k) != "") expects: "1" when set, "" (unset) when not -- the
// env-var analog of the old bool flag's "true"/"false" token (issue #2979).
func presenceEnvValue(b bool) string {
	if b {
		return "1"
	}
	return ""
}

// TestIsAssemblePromptInvocation verifies the assemble-prompt subcommand's
// dispatch guard: a bare "assemble-prompt" first arg selects it, while every
// other invocation shape falls through to the default Driver-invocation path
// (or, for the other subcommands, to those).
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
		{"outcome-backstop", []string{"outcome-backstop"}, false},
	}
	for _, c := range cases {
		if got := isAssemblePromptInvocation(c.args); got != c.want {
			t.Errorf("%s: isAssemblePromptInvocation(%v) = %v, want %v", c.name, c.args, got, c.want)
		}
	}
}

// TestRunAssemblePrompt_PopulatesPassthroughHandoffFields verifies the pure
// passthrough flags (--model, --effort, --driver*, --devshell*, --argv-*,
// --max-*, --heartbeat-log) reach result.Handoff untouched by Assemble
// itself, and that PromptFile/AgentsFile/Issue are populated from the
// existing --prompt-output/--agents-json-output/--issue-number flags this
// command already parsed (issue #2975).
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
	rc := runAssemblePrompt(args, &stdout)
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

// reflectStringSlicesEqual compares two string slices element-by-element --
// a small helper so TestRunAssemblePrompt_PopulatesPassthroughHandoffFields
// doesn't need to pull in reflect.DeepEqual or the slices package just for
// this one comparison.
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

// TestRunAssemblePrompt_MalformedBudgetCapsDegradeToZero pins the
// graceful-degrade contract for --max-budget-tokens/--max-budget-usd end to
// end (issue #2975 review finding #1, restoring coverage dropped when
// TestMainRunToleratesMalformedOrNegativeBudgetCaps was deleted with no
// replacement): entrypoint.sh forwards MAX_BUDGET_TOKENS/MAX_BUDGET_USD
// verbatim, so an operator typo or stray negative value must degrade to 0
// here -- the layer that now actually parses the strings -- rather than
// making fs.Parse fail and killing the whole box run under entrypoint.sh's
// set -euo pipefail (issue #2694's original rationale, still binding).
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
			rc := runAssemblePrompt(args, &stdout)
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

// TestRunAssemblePrompt_ReviewPromptOutput verifies --review-prompt-output
// writes Result.ReviewPromptText to disk and sets Handoff.ReviewPromptFile
// to that path on an orchestrator-on, default-work, FixPass==0 cell (the one
// cell that renders a review prompt at all, mirroring
// TestAssembleOrchestratorReviewerDrop's Env setup) -- and that omitting the
// flag entirely on that same cell leaves Handoff.ReviewPromptFile empty and
// still exits 0, since a rendered-but-unrequested review prompt is not an
// error (issue #2975).
func TestRunAssemblePrompt_ReviewPromptOutput(t *testing.T) {
	orchestratorOnArgs := func(t *testing.T, promptOutput, agentsJSONOutput, handoffOutput string) []string {
		args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
		t.Setenv("ORCHESTRATOR_ENABLED", "1")
		t.Setenv("BOX_REVIEW_LOOP_INLINE", "")
		t.Setenv("BOX_REVIEW_LOOP_ORCHESTRATOR", "1")
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
		rc := runAssemblePrompt(args, &stdout)
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
		rc := runAssemblePrompt(args, &stdout)
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
