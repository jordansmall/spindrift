package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"spindrift.dev/launcher/internal/promptassembly"
)

// isAssemblePromptInvocation reports whether args (os.Args[1:]) selects the
// assemble-prompt subcommand: a distinct verb, not a top-level flag (issue
// #2349), mirroring isBundleOutInvocation/isOutcomeBackstopInvocation.
func isAssemblePromptInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "assemble-prompt"
}

// runAssemblePrompt is the `assemble-prompt` subcommand's thin CLI wrapper
// (ADR 0007's thin-exec-glue tier, issue #2349): it parses args into a
// promptassembly.Env, loads the fragment registry, delegates to
// promptassembly.Assemble, and writes the three resulting output files.
// Returns the process exit code.
func runAssemblePrompt(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("assemble-prompt", flag.ContinueOnError)
	fs.SetOutput(stdout)

	// Skill-baking presence flags.
	// BEGIN GENERATED SKILL-BAKED FLAGS -- nix run .#regen -- DO NOT EDIT
	cavemanSkillBaked := fs.Bool("caveman-skill-baked", false, "true when DRIVER_SKILLS_DIR/caveman/SKILL.md was baked")
	tddSkillBaked := fs.Bool("tdd-skill-baked", false, "true when DRIVER_SKILLS_DIR/tdd/SKILL.md was baked")
	commitSkillBaked := fs.Bool("commit-skill-baked", false, "true when DRIVER_SKILLS_DIR/commit/SKILL.md was baked")
	codeReviewSkillBaked := fs.Bool("code-review-skill-baked", false, "true when DRIVER_SKILLS_DIR/code-review/SKILL.md was baked")
	autoFormatSkillBaked := fs.Bool("auto-format-skill-baked", false, "true when DRIVER_SKILLS_DIR/auto-format/SKILL.md was baked")
	autoLintSkillBaked := fs.Bool("auto-lint-skill-baked", false, "true when DRIVER_SKILLS_DIR/auto-lint/SKILL.md was baked")
	checkHygieneSkillBaked := fs.Bool("check-hygiene-skill-baked", false, "true when DRIVER_SKILLS_DIR/check-hygiene/SKILL.md was baked")
	codeCommentsSkillBaked := fs.Bool("code-comments-skill-baked", false, "true when DRIVER_SKILLS_DIR/code-comments/SKILL.md was baked")
	// END GENERATED SKILL-BAKED FLAGS

	promptsDir := fs.String("prompts-dir", "", "PROMPTS_DIR, default /agent/prompts")
	agentsPromptFiles := fs.String("agents-prompt-files", "", "nix-baked agent-name -> promptFile JSON map")
	driverAgentFilesDir := fs.String("driver-agent-files-dir", "", "opencode-style baked agent files dir, empty for claude")

	commsContractFile := fs.String("comms-contract-file", "", "COMMS_CONTRACT_FILE")
	checkContractFile := fs.String("check-contract-file", "", "CHECK_CONTRACT_FILE")
	outcomeContractFile := fs.String("outcome-contract-file", "", "OUTCOME_CONTRACT_FILE")
	researchOutcomeContractFile := fs.String("research-outcome-contract-file", "", "RESEARCH_OUTCOME_CONTRACT_FILE")

	skillsFound := fs.String("skills-found", "", "comma-separated list of skill directory basenames found under DRIVER_SKILLS_DIR")

	registryPath := fs.String("registry", "", "path to the fragment registry JSON file (required)")
	validateMarkersRegistryPath := fs.String("validate-markers-registry", "", "path to the prompt-contract validateMarkers registry JSON file (required)")
	promptOutput := fs.String("prompt-output", "", "path to write the assembled prompt text to (required)")
	agentsJSONOutput := fs.String("agents-json-output", "", "path to write the (possibly empty) --agents JSON to (required)")
	handoffOutput := fs.String("handoff-output", "", "path to write the driver hand-off facts as JSON to (required)")
	reviewPromptOutput := fs.String("review-prompt-output", "", "path to write the rendered review-prompt text to, only when the cell actually renders one")

	// The following flags are pure passthrough into result.Handoff after
	// Assemble returns -- Assemble itself never reads them (issue #2975).
	argvPromptStyle := fs.String("argv-prompt-style", "flag", "Handoff.ArgvShape.PromptStyle")
	argvPromptFlag := fs.String("argv-prompt-flag", "", "Handoff.ArgvShape.PromptFlag")
	argvModelFlag := fs.String("argv-model-flag", "--model", "Handoff.ArgvShape.ModelFlag")
	argvModelOmitEmpty := fs.Bool("argv-model-omit-empty", false, "Handoff.ArgvShape.ModelOmitEmpty")
	argvAgentsFlag := fs.String("argv-agents-flag", "", "Handoff.ArgvShape.AgentsFlag")
	argvEffortFlag := fs.String("argv-effort-flag", "--effort", "Handoff.ArgvShape.EffortFlag")
	argvOrder := fs.String("argv-order", "prompt model agents session driverFlags effort", "space-separated Handoff.ArgvShape.Order")

	model := fs.String("model", "", "Handoff.Model")
	effort := fs.String("effort", "", "Handoff.Effort")
	driverName := fs.String("driver", "claude", "Handoff.Driver")
	driverBin := fs.String("driver-bin", "", "Handoff.DriverBin")
	driverFlags := fs.String("driver-flags", "", "Handoff.DriverFlags")
	devshell := fs.Bool("devshell", false, "Handoff.Devshell")
	devshellName := fs.String("devshell-name", "default", "Handoff.DevshellName")

	maxReviewRounds := fs.Int("max-review-rounds", promptassembly.DefaultMaxReviewRounds, "Handoff.Caps.MaxReviewRounds")
	maxSlices := fs.Int("max-slices", promptassembly.DefaultMaxSlices, "Handoff.Caps.MaxSlices")
	// String, not Int/Float64: a malformed forwarded value must degrade to 0
	// via promptassembly.ParseNonnegBudgetTokens/ParseNonnegBudgetUSD after
	// fs.Parse succeeds, never make fs.Parse itself fail and return non-zero
	// -- entrypoint.sh runs under set -euo pipefail, so a fatal exit here
	// kills the whole box run over a value that was never fatal before this
	// cap existed (issue #2975 review finding #1, issue #2694's original
	// rationale).
	maxBudgetTokensRaw := fs.String("max-budget-tokens", "0", "Handoff.Caps.MaxBudgetTokens")
	maxBudgetUSDRaw := fs.String("max-budget-usd", "0", "Handoff.Caps.MaxBudgetUSD")

	heartbeatLog := fs.String("heartbeat-log", "", "Handoff.HeartbeatLog")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	// Degrades a malformed/negative --max-budget-tokens/--max-budget-usd to 0
	// rather than failing the run; ok is discarded since this passthrough CLI
	// wrapper has no operator-facing diagnostics channel for it today.
	maxBudgetTokens, _ := promptassembly.ParseNonnegBudgetTokens(*maxBudgetTokensRaw)
	maxBudgetUSD, _ := promptassembly.ParseNonnegBudgetUSD(*maxBudgetUSDRaw)

	if *registryPath == "" || *validateMarkersRegistryPath == "" || *promptOutput == "" || *agentsJSONOutput == "" || *handoffOutput == "" {
		fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt: -registry, -validate-markers-registry, -prompt-output, -agents-json-output, and -handoff-output are all required")
		return 1
	}

	registry, err := promptassembly.LoadRegistryFile(*registryPath)
	if err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt:", err)
		return 1
	}

	validateMarkerRows, err := promptassembly.LoadValidateMarkersFile(*validateMarkersRegistryPath)
	if err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt:", err)
		return 1
	}

	env := promptassembly.EnvFromEnviron()
	// BEGIN GENERATED SKILL-BAKED ENV -- nix run .#regen -- DO NOT EDIT
	env.CavemanSkillBaked = *cavemanSkillBaked
	env.TDDSkillBaked = *tddSkillBaked
	env.CommitSkillBaked = *commitSkillBaked
	env.CodeReviewSkillBaked = *codeReviewSkillBaked
	env.AutoFormatSkillBaked = *autoFormatSkillBaked
	env.AutoLintSkillBaked = *autoLintSkillBaked
	env.CheckHygieneSkillBaked = *checkHygieneSkillBaked
	env.CodeCommentsSkillBaked = *codeCommentsSkillBaked
	// END GENERATED SKILL-BAKED ENV
	env.PromptsDir = *promptsDir
	env.AgentsPromptFiles = *agentsPromptFiles
	env.DriverAgentFilesDir = *driverAgentFilesDir
	env.CommsContractFile = *commsContractFile
	env.CheckContractFile = *checkContractFile
	env.OutcomeContractFile = *outcomeContractFile
	env.ResearchOutcomeContractFile = *researchOutcomeContractFile
	env.SkillsFound = *skillsFound

	result, err := promptassembly.Assemble(env, registry)
	if err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt:", err)
		return 1
	}

	warnings, err := promptassembly.Validate(env, result, validateMarkerRows)
	for _, w := range warnings {
		fmt.Fprintln(fs.Output(), w)
	}
	if err != nil {
		fmt.Fprintln(fs.Output(), err)
		return 1
	}

	if err := os.WriteFile(*promptOutput, []byte(result.Prompt), 0o644); err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt: write prompt output:", err)
		return 1
	}
	if err := os.WriteFile(*agentsJSONOutput, []byte(result.AgentsJSON), 0o644); err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt: write agents json output:", err)
		return 1
	}

	// Every field set below is pure passthrough (issue #2975): Assemble never
	// touches them, so they're layered onto result.Handoff here, after
	// Assemble/Validate both succeed, straight from this command's own flags
	// (Issue from env.IssueNumber, issue #2979 -- ISSUE_NUMBER is now read via
	// EnvFromEnviron rather than its own flag).
	result.Handoff.PromptFile = *promptOutput
	if result.AgentsJSON != "" {
		result.Handoff.AgentsFile = *agentsJSONOutput
	}
	result.Handoff.Model = *model
	result.Handoff.Effort = *effort
	result.Handoff.Driver = *driverName
	result.Handoff.DriverBin = *driverBin
	result.Handoff.DriverFlags = *driverFlags
	result.Handoff.Devshell = *devshell
	result.Handoff.DevshellName = *devshellName
	result.Handoff.Issue = env.IssueNumber
	result.Handoff.HeartbeatLog = *heartbeatLog
	result.Handoff.ArgvShape = promptassembly.ArgvShape{
		PromptStyle:    *argvPromptStyle,
		PromptFlag:     *argvPromptFlag,
		ModelFlag:      *argvModelFlag,
		ModelOmitEmpty: *argvModelOmitEmpty,
		AgentsFlag:     *argvAgentsFlag,
		EffortFlag:     *argvEffortFlag,
		Order:          strings.Fields(*argvOrder),
	}
	result.Handoff.Caps = promptassembly.Caps{
		MaxSlices:       *maxSlices,
		MaxReviewRounds: *maxReviewRounds,
		MaxBudgetTokens: maxBudgetTokens,
		MaxBudgetUSD:    maxBudgetUSD,
	}
	if result.ReviewPromptText != "" && *reviewPromptOutput != "" {
		if err := os.WriteFile(*reviewPromptOutput, []byte(result.ReviewPromptText), 0o644); err != nil {
			fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt: write review prompt output:", err)
			return 1
		}
		result.Handoff.ReviewPromptFile = *reviewPromptOutput
	}

	handoffJSON, err := json.Marshal(result.Handoff)
	if err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt: marshal handoff:", err)
		return 1
	}
	if err := os.WriteFile(*handoffOutput, handoffJSON, 0o644); err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt: write handoff output:", err)
		return 1
	}

	return 0
}
