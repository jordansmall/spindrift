package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

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
	cavemanSkillBaked := fs.Bool("caveman-skill-baked", false, "true when DRIVER_SKILLS_DIR/caveman/SKILL.md was baked")
	tddSkillBaked := fs.Bool("tdd-skill-baked", false, "true when DRIVER_SKILLS_DIR/tdd/SKILL.md was baked")
	commitSkillBaked := fs.Bool("commit-skill-baked", false, "true when DRIVER_SKILLS_DIR/commit/SKILL.md was baked")
	codeReviewSkillBaked := fs.Bool("code-review-skill-baked", false, "true when DRIVER_SKILLS_DIR/code-review/SKILL.md was baked")

	orchestratorEnabled := fs.Bool("orchestrator-enabled", false, "true when ORCHESTRATOR_ENABLED is set")
	agentsJSONTemplate := fs.String("agents-json-template", "", "the nix-baked --agents JSON template, empty when no subagent model is configured")
	issueTracker := fs.String("issue-tracker", "", "ISSUE_TRACKER value, defaults to github when empty")
	boxWriteEnabled := fs.Bool("box-write-enabled", false, "true when BOX_WRITE_ENABLED is set")
	localIssueReference := fs.Bool("local-issue-reference", false, "true when LOCAL_ISSUE_REFERENCE is set")
	codeForge := fs.String("code-forge", "", "CODE_FORGE value, defaults to github when empty")
	dispatchKind := fs.String("dispatch-kind", "", "DISPATCH_KIND value, defaults to work when empty")
	selfContained := fs.Bool("self-contained", false, "true when SELF_CONTAINED == 1")
	fixPass := fs.Int("fix-pass", 0, "FIX_PASS number; >0 selects fix-prompt.md")
	resumeAfterHold := fs.Bool("resume-after-hold", false, "true when RESUME_AFTER_HOLD is set")
	autoFormat := fs.Bool("auto-format", false, "true when AUTO_FORMAT is set")
	autoLint := fs.Bool("auto-lint", false, "true when AUTO_LINT is set")
	ciFailureSummary := fs.String("ci-failure-summary", "", "CI_FAILURE_SUMMARY value, launcher-forwarded on a fix pass (issue #426)")

	promptsDir := fs.String("prompts-dir", "", "PROMPTS_DIR, default /agent/prompts")
	agentsPromptFiles := fs.String("agents-prompt-files", "", "nix-baked agent-name -> promptFile JSON map")
	driverAgentFilesDir := fs.String("driver-agent-files-dir", "", "opencode-style baked agent files dir, empty for claude")

	commsContractFile := fs.String("comms-contract-file", "", "COMMS_CONTRACT_FILE")
	checkContractFile := fs.String("check-contract-file", "", "CHECK_CONTRACT_FILE")
	outcomeContractFile := fs.String("outcome-contract-file", "", "OUTCOME_CONTRACT_FILE")
	researchOutcomeContractFile := fs.String("research-outcome-contract-file", "", "RESEARCH_OUTCOME_CONTRACT_FILE")

	skillsFound := fs.String("skills-found", "", "comma-separated list of skill directory basenames found under DRIVER_SKILLS_DIR")

	issueNumber := fs.String("issue-number", "", "ISSUE_NUMBER")
	issueTitle := fs.String("issue-title", "", "ISSUE_TITLE")
	branch := fs.String("branch", "", "BRANCH")
	baseBranch := fs.String("base-branch", "", "BASE_BRANCH")
	inProgressLabel := fs.String("in-progress-label", "", "IN_PROGRESS_LABEL")
	completeLabel := fs.String("complete-label", "", "COMPLETE_LABEL")
	runNonce := fs.String("run-nonce", "", "RUN_NONCE")

	registryPath := fs.String("registry", "", "path to the fragment registry JSON file (required)")
	validateMarkersRegistryPath := fs.String("validate-markers-registry", "", "path to the prompt-contract validateMarkers registry JSON file (required)")
	forbiddenMarkersRegistryPath := fs.String("forbidden-markers-registry", "", "path to the prompt-contract forbiddenMarkers registry JSON file (required)")
	promptOutput := fs.String("prompt-output", "", "path to write the assembled prompt text to (required)")
	agentsJSONOutput := fs.String("agents-json-output", "", "path to write the (possibly empty) --agents JSON to (required)")
	handoffOutput := fs.String("handoff-output", "", "path to write the driver hand-off facts as JSON to (required)")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *registryPath == "" || *validateMarkersRegistryPath == "" || *forbiddenMarkersRegistryPath == "" || *promptOutput == "" || *agentsJSONOutput == "" || *handoffOutput == "" {
		fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt: -registry, -validate-markers-registry, -forbidden-markers-registry, -prompt-output, -agents-json-output, and -handoff-output are all required")
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

	forbiddenMarkerRows, err := promptassembly.LoadForbiddenMarkersFile(*forbiddenMarkersRegistryPath)
	if err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt:", err)
		return 1
	}

	env := promptassembly.Env{
		CavemanSkillBaked:    *cavemanSkillBaked,
		TDDSkillBaked:        *tddSkillBaked,
		CommitSkillBaked:     *commitSkillBaked,
		CodeReviewSkillBaked: *codeReviewSkillBaked,

		OrchestratorEnabled: *orchestratorEnabled,

		AgentsJSONTemplate: *agentsJSONTemplate,

		IssueTracker: *issueTracker,

		BoxWriteEnabled: *boxWriteEnabled,

		LocalIssueReference: *localIssueReference,

		CodeForge: *codeForge,

		DispatchKind:    *dispatchKind,
		SelfContained:   *selfContained,
		FixPass:         *fixPass,
		ResumeAfterHold: *resumeAfterHold,

		AutoFormat:       *autoFormat,
		AutoLint:         *autoLint,
		CIFailureSummary: *ciFailureSummary,

		PromptsDir:          *promptsDir,
		AgentsPromptFiles:   *agentsPromptFiles,
		DriverAgentFilesDir: *driverAgentFilesDir,

		CommsContractFile:           *commsContractFile,
		CheckContractFile:           *checkContractFile,
		OutcomeContractFile:         *outcomeContractFile,
		ResearchOutcomeContractFile: *researchOutcomeContractFile,

		SkillsFound: *skillsFound,

		IssueNumber:     *issueNumber,
		IssueTitle:      *issueTitle,
		Branch:          *branch,
		BaseBranch:      *baseBranch,
		InProgressLabel: *inProgressLabel,
		CompleteLabel:   *completeLabel,
		RunNonce:        *runNonce,
	}

	result, err := promptassembly.Assemble(env, registry)
	if err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt:", err)
		return 1
	}

	warnings, err := promptassembly.Validate(env, result, validateMarkerRows, forbiddenMarkerRows)
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
