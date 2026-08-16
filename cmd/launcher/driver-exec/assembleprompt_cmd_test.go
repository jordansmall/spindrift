package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

// coveredCellArgs returns the flag set that puts runAssemblePrompt's Env
// squarely in promptassembly.Assemble's covered cell (see promptassembly's
// checkCoveredCell, which as of issue #2540 checks only dispatch kind
// "work"): github tracker, github forge, a read-write box, dispatch kind
// "work", a fresh box (fix-pass 0), the orchestrator off, and every skill
// baked.
func coveredCellArgs(t *testing.T, promptOutput, agentsJSONOutput, handoffOutput string) []string {
	t.Helper()
	return []string{
		"--caveman-skill-baked=true",
		"--tdd-skill-baked=true",
		"--commit-skill-baked=true",
		"--code-review-skill-baked=true",
		"--auto-format-skill-baked=true",
		"--auto-lint-skill-baked=true",
		"--orchestrator-enabled=false",
		"--issue-tracker", "github",
		"--box-write-enabled=true",
		"--code-forge", "github",
		"--dispatch-kind", "work",
		"--fix-pass", "0",
		"--prompts-dir", promptsDirForTest,
		"--skills-found", "caveman, tdd, commit, code-review",
		"--issue-number", "2349",
		"--issue-title", "Add assemble-prompt CLI verb",
		"--branch", "agent/issue-2349",
		"--base-branch", "main",
		"--in-progress-label", "agent-in-progress",
		"--complete-label", "agent-complete",
		"--run-nonce", "run-nonce-abc123",
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
	for i, a := range args {
		if a == "work" && args[i-1] == "--dispatch-kind" {
			args[i] = "bogus-kind"
		}
	}

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
	args = replaceArg(args, "--dispatch-kind", "research")
	args = replaceArg(args, "--box-write-enabled", "false")
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
	args = replaceArg(args, "--box-write-enabled", "false")
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
	// --agents-json-template configured (as here) Assemble's own AgentsJSON
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

// TestRunAssemblePrompt_TrackerAxisFlagsReachGates verifies the
// --tracker-axis-read/--tracker-axis-write/--tracker-axis-filer flags reach
// promptassembly.Env's TrackerAxisRead/TrackerAxisWrite/TrackerAxisFiler
// fields (issue #2533 slice 2): setting --tracker-axis-read/write/filer=
// FORGEJO fires the ISSUE_TRACKER_FORGEJO/ISSUE_TRACKER_FORGEJO_READWRITE
// gates (gates_tracker.go reads the axis fields directly, no longer
// re-deriving them from --issue-tracker) -- even though --issue-tracker
// itself is left at "github", since checkCoveredCell no longer re-validates
// IssueTracker (issue #2540) and the axis fields are the sole gate input.
// Issue #2547 re-points issue-read-github.md/issue-read-forgejo.md at the
// same frozen /issue-snapshot.md, so they no longer distinguish trackers;
// this asserts that convergence directly, then proves the FORGEJO axis
// still reaches a gate that does distinguish trackers --
// ISSUE_TRACKER_FORGEJO_READWRITE's issue-blocked-comment-forgejo.md
// ("fj issue comment"), unaffected by issue #2547, instead of
// issue-blocked-comment-github.md's "gh issue comment".
func TestRunAssemblePrompt_TrackerAxisFlagsReachGates(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	args = replaceArg(args, "--tracker-axis-read", "FORGEJO")
	args = replaceArg(args, "--tracker-axis-write", "FORGEJO")
	args = replaceArg(args, "--tracker-axis-filer", "FORGEJO")

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

// TestRunAssemblePrompt_ForgeBackendFlagReachesGates verifies the
// --forge-backend flag reaches promptassembly.Env.ForgeBackend
// (gates_access_forge.go reads it directly, no longer re-deriving it from
// --code-forge, issue #2533 slice 2): setting --forge-backend=FORGEJO fires
// FIX_CI_READ_FORGEJO instead of FIX_CI_READ_GH, even with --code-forge
// left at "github".
func TestRunAssemblePrompt_ForgeBackendFlagReachesGates(t *testing.T) {
	dir := t.TempDir()
	promptOutput := filepath.Join(dir, "prompt.txt")
	agentsJSONOutput := filepath.Join(dir, "agents.json")
	handoffOutput := filepath.Join(dir, "handoff.json")

	args := coveredCellArgs(t, promptOutput, agentsJSONOutput, handoffOutput)
	args = replaceArg(args, "--fix-pass", "1")
	args = replaceArg(args, "--forge-backend", "FORGEJO")

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

// TestRunAssemblePrompt_FilerEnabledFlagReachesPrompt verifies --filer-enabled
// reaches promptassembly.Env.FilerEnabled and, through it, the FILER_ENABLED
// gate: on, the rendered prompt carries the filer's FILE ISSUES heading; off
// (coveredCellArgs' default -- the flag defaults to false and is never set
// by coveredCellArgs itself), it does not (issue #2533 slice 2).
func TestRunAssemblePrompt_FilerEnabledFlagReachesPrompt(t *testing.T) {
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
			args = replaceArg(args, "--filer-enabled", boolFlagValue(enabled))

			prompt := assemblePromptForTest(t, dir, args)
			got := strings.Contains(prompt, filerEnabledMarker)
			if got != enabled {
				t.Errorf("prompt contains %q = %v, want %v (--filer-enabled=%v)", filerEnabledMarker, got, enabled, enabled)
			}
		})
	}
}

// TestRunAssemblePrompt_WorkerProvisionedFlagReachesPrompt verifies
// --worker-provisioned reaches promptassembly.Env.WorkerProvisioned and, through
// it, the WORKER_PROVISIONED gate: on, the rendered prompt carries
// coordinator.md's marker text; off, it does not (issue #2533 slice 2).
func TestRunAssemblePrompt_WorkerProvisionedFlagReachesPrompt(t *testing.T) {
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
			args = replaceArg(args, "--worker-provisioned", boolFlagValue(provisioned))

			prompt := assemblePromptForTest(t, dir, args)
			got := strings.Contains(prompt, workerProvisionedMarker)
			if got != provisioned {
				t.Errorf("prompt contains %q = %v, want %v (--worker-provisioned=%v)", workerProvisionedMarker, got, provisioned, provisioned)
			}
		})
	}
}

// TestRunAssemblePrompt_ReviewLoopFlagsReachPrompt verifies
// --review-loop-inline/--review-loop-orchestrator reach
// promptassembly.Env.ReviewLoopInline/Env.ReviewLoopOrchestrator and, through
// them, the REVIEW_LOOP_INLINE/REVIEW_LOOP_ORCHESTRATOR gates: each flag
// combination renders exactly its own fragment's marker and never the
// other's, proving the two flags aren't swapped or aliased (issue #2533
// slice 2).
func TestRunAssemblePrompt_ReviewLoopFlagsReachPrompt(t *testing.T) {
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
			args = replaceArg(args, "--review-loop-inline", boolFlagValue(c.inline))
			args = replaceArg(args, "--review-loop-orchestrator", boolFlagValue(c.orchestrator))

			prompt := assemblePromptForTest(t, dir, args)
			if gotInline := strings.Contains(prompt, reviewLoopInlineMarker); gotInline != c.inline {
				t.Errorf("prompt contains %q = %v, want %v (--review-loop-inline=%v)", reviewLoopInlineMarker, gotInline, c.inline, c.inline)
			}
			if gotOrchestrator := strings.Contains(prompt, reviewLoopOrchestratorMarker); gotOrchestrator != c.orchestrator {
				t.Errorf("prompt contains %q = %v, want %v (--review-loop-orchestrator=%v)", reviewLoopOrchestratorMarker, gotOrchestrator, c.orchestrator, c.orchestrator)
			}
		})
	}
}

// TestRunAssemblePrompt_FilerAndWorkerFlagsNotCrossWired proves
// --filer-enabled and --worker-provisioned reach their own, distinct Env
// fields rather than being crossed (e.g. --filer-enabled accidentally
// wired to Env.WorkerProvisioned, or vice versa): with exactly one of the
// two flags on, only that flag's own marker appears in the rendered
// prompt, never the other's -- a wiring bug that swapped the two flags'
// destinations would show both markers together or neither, failing this
// test (issue #2533 slice 2, the review finding this pins closed).
func TestRunAssemblePrompt_FilerAndWorkerFlagsNotCrossWired(t *testing.T) {
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
			args = replaceArg(args, "--filer-enabled", boolFlagValue(c.filer))
			args = replaceArg(args, "--worker-provisioned", boolFlagValue(c.worker))

			prompt := assemblePromptForTest(t, dir, args)
			if gotFiler := strings.Contains(prompt, filerEnabledMarker); gotFiler != c.wantFiler {
				t.Errorf("prompt contains %q = %v, want %v (--filer-enabled=%v, --worker-provisioned=%v)", filerEnabledMarker, gotFiler, c.wantFiler, c.filer, c.worker)
			}
			if gotWorker := strings.Contains(prompt, workerProvisionedMarker); gotWorker != c.wantWorker {
				t.Errorf("prompt contains %q = %v, want %v (--filer-enabled=%v, --worker-provisioned=%v)", workerProvisionedMarker, gotWorker, c.wantWorker, c.filer, c.worker)
			}
		})
	}
}

// boolFlagValue renders b the way replaceArg/the flag package expect a bool
// flag's value token ("true"/"false").
func boolFlagValue(b bool) string {
	if b {
		return "true"
	}
	return "false"
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
