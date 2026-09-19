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

// carriedTextSpec is one parsed --composition-carried value. Set checks the
// syntax, but nothing opens the file at path unless --composition-output is
// set (issue #3444 slice 3), so an unreadable carried file cannot fail a
// normal, report-free run that only parses the flag.
type carriedTextSpec struct {
	pass, name, path string
}

type carriedTextFlag []carriedTextSpec

func (f *carriedTextFlag) String() string {
	if f == nil {
		return ""
	}
	parts := make([]string, len(*f))
	for i, s := range *f {
		if s.pass == "" {
			parts[i] = s.name + "=" + s.path
		} else {
			parts[i] = s.pass + ":" + s.name + "=" + s.path
		}
	}
	return strings.Join(parts, ",")
}

// Set parses "[<pass>:]<name>=<path>". The pass prefix is whatever precedes
// the first ':' that itself precedes the first '=', so a ':' inside path
// never acts as the pass separator.
func (f *carriedTextFlag) Set(v string) error {
	eq := strings.Index(v, "=")
	if eq < 0 {
		return malformedCarriedText(v)
	}
	head, path := v[:eq], v[eq+1:]
	pass, name := "", head
	if colon := strings.Index(head, ":"); colon >= 0 {
		pass, name = head[:colon], head[colon+1:]
		// An explicit ":" with nothing before it matches no pass kind, so
		// reject it rather than silently falling back to "every pass".
		if pass == "" {
			return malformedCarriedText(v)
		}
	}
	if name == "" {
		return malformedCarriedText(v)
	}
	*f = append(*f, carriedTextSpec{pass: pass, name: name, path: path})
	return nil
}

func malformedCarriedText(v string) error {
	return fmt.Errorf("malformed --composition-carried value %q, want [<pass>:]<name>=<path>", v)
}

// isAssemblePromptInvocation reports whether args (os.Args[1:]) selects the
// assemble-prompt subcommand, which is a verb and not a top-level flag (issue
// #2349).
func isAssemblePromptInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "assemble-prompt"
}

// runAssemblePrompt is the `assemble-prompt` subcommand's CLI wrapper and
// returns the process exit code. Keep it thin: the real work belongs in
// promptassembly (ADR 0007's thin-exec-glue tier, issue #2349).
func runAssemblePrompt(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("assemble-prompt", flag.ContinueOnError)
	fs.SetOutput(stdout)

	// BEGIN GENERATED SKILL-BAKED FLAGS -- nix run .#regen -- DO NOT EDIT
	cavemanSkillBaked := fs.Bool("caveman-skill-baked", false, "true when DRIVER_SKILLS_DIR/caveman/SKILL.md was baked")
	tddSkillBaked := fs.Bool("tdd-skill-baked", false, "true when DRIVER_SKILLS_DIR/tdd/SKILL.md was baked")
	commitSkillBaked := fs.Bool("commit-skill-baked", false, "true when DRIVER_SKILLS_DIR/commit/SKILL.md was baked")
	codeReviewSkillBaked := fs.Bool("code-review-skill-baked", false, "true when DRIVER_SKILLS_DIR/code-review/SKILL.md was baked")
	autoFormatSkillBaked := fs.Bool("auto-format-skill-baked", false, "true when DRIVER_SKILLS_DIR/auto-format/SKILL.md was baked")
	autoLintSkillBaked := fs.Bool("auto-lint-skill-baked", false, "true when DRIVER_SKILLS_DIR/auto-lint/SKILL.md was baked")
	checkHygieneSkillBaked := fs.Bool("check-hygiene-skill-baked", false, "true when DRIVER_SKILLS_DIR/check-hygiene/SKILL.md was baked")
	codeCommentsSkillBaked := fs.Bool("code-comments-skill-baked", false, "true when DRIVER_SKILLS_DIR/code-comments/SKILL.md was baked")
	nixChecksSkillBaked := fs.Bool("nix-checks-skill-baked", false, "true when DRIVER_SKILLS_DIR/nix-checks/SKILL.md was baked")
	principleFixRootCausesSkillBaked := fs.Bool("principle-fix-root-causes-skill-baked", false, "true when DRIVER_SKILLS_DIR/principle-fix-root-causes/SKILL.md was baked")
	principleLazinessProtocolSkillBaked := fs.Bool("principle-laziness-protocol-skill-baked", false, "true when DRIVER_SKILLS_DIR/principle-laziness-protocol/SKILL.md was baked")
	principleRedesignFromFirstPrinciplesSkillBaked := fs.Bool("principle-redesign-from-first-principles-skill-baked", false, "true when DRIVER_SKILLS_DIR/principle-redesign-from-first-principles/SKILL.md was baked")
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
	compositionOutput := fs.String("composition-output", "", "path to write promptassembly.Compose's report as JSON, or '-' for stdout; empty (default) skips composition reporting entirely")
	var compositionCarried carriedTextFlag
	fs.Var(&compositionCarried, "composition-carried", "[<pass>:]<name>=<path> carried-text block fed to Compose (repeatable); only meaningful with --composition-output")

	// Assemble never reads the flags below; they pass straight through into
	// result.Handoff after it returns (issue #2975).
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
	// String, not Int/Float64: a malformed value degrades to 0 below, after
	// fs.Parse succeeds. If fs.Parse failed instead, the non-zero exit would
	// kill the whole box run under entrypoint.sh's set -euo pipefail, over a
	// value that was never fatal before this cap existed (issue #2975 review
	// finding #1, issue #2694).
	maxBudgetTokensRaw := fs.String("max-budget-tokens", "0", "Handoff.Caps.MaxBudgetTokens")
	maxBudgetUSDRaw := fs.String("max-budget-usd", "0", "Handoff.Caps.MaxBudgetUSD")

	heartbeatLog := fs.String("heartbeat-log", "", "Handoff.HeartbeatLog")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	// A malformed or negative value degrades to 0 rather than failing the run.
	// This wrapper discards the ok result because it has no diagnostics channel
	// to report the degradation on.
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
	env.NixChecksSkillBaked = *nixChecksSkillBaked
	env.PrincipleFixRootCausesSkillBaked = *principleFixRootCausesSkillBaked
	env.PrincipleLazinessProtocolSkillBaked = *principleLazinessProtocolSkillBaked
	env.PrincipleRedesignFromFirstPrinciplesSkillBaked = *principleRedesignFromFirstPrinciplesSkillBaked
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

	// Passthrough only (issue #2975): Assemble never touches these fields, so
	// this command layers its own flags on once Assemble and Validate both
	// succeed. Issue comes from env.IssueNumber because EnvFromEnviron reads
	// ISSUE_NUMBER rather than a flag (issue #2979).
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

	// Composition runs last, after every other output succeeds, so a failure
	// here cannot cost the real prompt, agents, handoff, or review-prompt
	// files (issue #3444 slice 3).
	if *compositionOutput != "" {
		var carried []promptassembly.CarriedText
		for _, spec := range compositionCarried {
			text, err := os.ReadFile(spec.path)
			if err != nil {
				fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt: read --composition-carried file:", err)
				return 1
			}
			carried = append(carried, promptassembly.CarriedText{Pass: spec.pass, Name: spec.name, Text: string(text)})
		}

		composition, err := promptassembly.Compose(env, registry, carried)
		if err != nil {
			fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt:", err)
			return 1
		}
		compositionJSON, err := json.MarshalIndent(composition, "", "  ")
		if err != nil {
			fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt: marshal composition:", err)
			return 1
		}
		compositionJSON = append(compositionJSON, '\n')

		if *compositionOutput == "-" {
			if _, err := stdout.Write(compositionJSON); err != nil {
				fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt: write composition output:", err)
				return 1
			}
		} else if err := os.WriteFile(*compositionOutput, compositionJSON, 0o644); err != nil {
			fmt.Fprintln(fs.Output(), "driver-exec assemble-prompt: write composition output:", err)
			return 1
		}
	}

	return 0
}
