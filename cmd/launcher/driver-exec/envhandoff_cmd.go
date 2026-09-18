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

func isEnvHandoffInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "env-handoff"
}

// runEnvHandoff writes a minimal handoff JSON for the one Driver pass that runs
// before phase_prompt_assembly: phase_conflict_resolve's pre-work rebase fixup
// (ADR 0007, issue #2975). It never touches the fragment registry (issue #2975
// finding #6), so the CONFLICT_RESOLVE_PR_URL early exit stays reachable when
// PROMPTASSEMBLY_REGISTRY_FILE points nowhere.
func runEnvHandoff(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("env-handoff", flag.ContinueOnError)
	fs.SetOutput(stdout)

	driverName := fs.String("driver", "", "Handoff.Driver")
	driverBin := fs.String("driver-bin", "", "Handoff.DriverBin")
	driverFlags := fs.String("driver-flags", "", "Handoff.DriverFlags")
	model := fs.String("model", "", "Handoff.Model")
	effort := fs.String("effort", "", "Handoff.Effort")
	devshell := fs.Bool("devshell", false, "Handoff.Devshell")
	devshellName := fs.String("devshell-name", "", "Handoff.DevshellName")
	issue := fs.String("issue", "", "Handoff.Issue")
	heartbeatLog := fs.String("heartbeat-log", "", "Handoff.HeartbeatLog")

	// Defaults must match assembleprompt_cmd.go's argv-* flags exactly (issue
	// #2975 finding #2): a caller that omits every argv-* flag must still get a
	// working ArgvShape, so a diverging default here would hand buildDriverArgs
	// a broken one, such as an empty PromptStyle.
	argvPromptStyle := fs.String("argv-prompt-style", "flag", "Handoff.ArgvShape.PromptStyle")
	argvPromptFlag := fs.String("argv-prompt-flag", "", "Handoff.ArgvShape.PromptFlag")
	argvModelFlag := fs.String("argv-model-flag", "--model", "Handoff.ArgvShape.ModelFlag")
	argvModelOmitEmpty := fs.Bool("argv-model-omit-empty", false, "Handoff.ArgvShape.ModelOmitEmpty")
	argvAgentsFlag := fs.String("argv-agents-flag", "", "Handoff.ArgvShape.AgentsFlag")
	argvEffortFlag := fs.String("argv-effort-flag", "--effort", "Handoff.ArgvShape.EffortFlag")
	argvOrder := fs.String("argv-order", "prompt model agents session driverFlags effort", "space-separated Handoff.ArgvShape.Order")

	// String, not Int/Float64: a malformed forwarded value must degrade to 0
	// below rather than fail fs.Parse and return non-zero, because entrypoint.sh
	// runs under set -euo pipefail (issues #2694, #2975 finding #1).
	maxBudgetTokensRaw := fs.String("max-budget-tokens", "0", "Handoff.Caps.MaxBudgetTokens")
	maxBudgetUSDRaw := fs.String("max-budget-usd", "0", "Handoff.Caps.MaxBudgetUSD")

	handoffOutput := fs.String("handoff-output", "", "path to write the driver hand-off facts as JSON to (required)")

	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *handoffOutput == "" {
		fmt.Fprintln(fs.Output(), "driver-exec env-handoff: -handoff-output is required")
		return 1
	}

	// A malformed or negative value degrades to 0; the ok result is discarded
	// because this wrapper has no diagnostics channel to report it on.
	maxBudgetTokens, _ := promptassembly.ParseNonnegBudgetTokens(*maxBudgetTokensRaw)
	maxBudgetUSD, _ := promptassembly.ParseNonnegBudgetUSD(*maxBudgetUSDRaw)

	handoff := promptassembly.Handoff{
		Model:        *model,
		Effort:       *effort,
		Driver:       *driverName,
		DriverBin:    *driverBin,
		DriverFlags:  *driverFlags,
		Devshell:     *devshell,
		DevshellName: *devshellName,
		Issue:        *issue,
		HeartbeatLog: *heartbeatLog,
		ArgvShape: promptassembly.ArgvShape{
			PromptStyle:    *argvPromptStyle,
			PromptFlag:     *argvPromptFlag,
			ModelFlag:      *argvModelFlag,
			ModelOmitEmpty: *argvModelOmitEmpty,
			AgentsFlag:     *argvAgentsFlag,
			EffortFlag:     *argvEffortFlag,
			Order:          strings.Fields(*argvOrder),
		},
		// No flag overrides the slice and review-round bounds, so they take
		// assemble-prompt's defaults: a conflict-resolve pass under
		// $ORCHESTRATOR needs a working loop bound rather than 0.
		Caps: promptassembly.Caps{
			MaxSlices:       promptassembly.DefaultMaxSlices,
			MaxReviewRounds: promptassembly.DefaultMaxReviewRounds,
			MaxBudgetTokens: maxBudgetTokens,
			MaxBudgetUSD:    maxBudgetUSD,
		},
	}

	handoffJSON, err := json.Marshal(handoff)
	if err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec env-handoff: marshal handoff:", err)
		return 1
	}
	if err := os.WriteFile(*handoffOutput, handoffJSON, 0o644); err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec env-handoff: write handoff output:", err)
		return 1
	}

	return 0
}
