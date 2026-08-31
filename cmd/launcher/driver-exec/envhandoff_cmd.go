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

// isEnvHandoffInvocation reports whether args (os.Args[1:]) selects the
// env-handoff subcommand: a distinct verb, mirroring
// isAssemblePromptInvocation/isBundleOutInvocation.
func isEnvHandoffInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "env-handoff"
}

// runEnvHandoff is the `env-handoff` subcommand's thin CLI wrapper (ADR
// 0007's thin-exec-glue tier, issue #2975 slice 2): it builds a minimal
// promptassembly.Handoff straight from env-derived flags and writes it as
// JSON to --handoff-output. It exists solely for the one Driver pass that
// runs before phase_prompt_assembly ever executes and so has no
// assemble-prompt-written handoff file yet: phase_conflict_resolve's
// pre-work rebase-fixup pass (entrypoint.sh's _write_env_handoff, which a
// later slice retargets to exec this binary). Unlike runAssemblePrompt, this
// verb never touches the fragment registry or promptassembly.Assemble --
// it is pure passthrough, no gate computation, by design (issue #2975
// blocked-review finding #6): the CONFLICT_RESOLVE_PR_URL early-exit path
// must stay reachable even when PROMPTASSEMBLY_REGISTRY_FILE points nowhere
// (tests/entrypoint-branch-recovery.bats).
//
// Only the fields a driver-exec-direct (or orchestrator) invocation actually
// consults for that one pass are flag-driven here; PromptFile, AgentsFile,
// ReviewPromptFile, ReviewModel, ReviewEffort, and SessionMode/Invoker have
// no flag at all and so unmarshal to their zero values on load -- mirroring
// _write_env_handoff's own doc comment on why those fields are left off.
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

	// Defaults mirror assembleprompt_cmd.go's own argv-* flags exactly (issue
	// #2975 review finding #2): both verbs must produce the same working
	// ArgvShape when a caller omits every argv-* flag, so a diverging default
	// here doesn't hand buildDriverArgs a broken shape (e.g. promptStyle "").
	argvPromptStyle := fs.String("argv-prompt-style", "flag", "Handoff.ArgvShape.PromptStyle")
	argvPromptFlag := fs.String("argv-prompt-flag", "", "Handoff.ArgvShape.PromptFlag")
	argvModelFlag := fs.String("argv-model-flag", "--model", "Handoff.ArgvShape.ModelFlag")
	argvModelOmitEmpty := fs.Bool("argv-model-omit-empty", false, "Handoff.ArgvShape.ModelOmitEmpty")
	argvAgentsFlag := fs.String("argv-agents-flag", "", "Handoff.ArgvShape.AgentsFlag")
	argvEffortFlag := fs.String("argv-effort-flag", "--effort", "Handoff.ArgvShape.EffortFlag")
	argvOrder := fs.String("argv-order", "prompt model agents session driverFlags effort", "space-separated Handoff.ArgvShape.Order")

	// String, not Int/Float64: a malformed forwarded value must degrade to 0
	// via promptassembly.ParseNonnegBudgetTokens/ParseNonnegBudgetUSD after
	// fs.Parse succeeds, never make fs.Parse itself fail and return non-zero
	// -- entrypoint.sh runs under set -euo pipefail, mirroring
	// assembleprompt_cmd.go's own maxBudgetTokensRaw/maxBudgetUSDRaw
	// rationale (issue #2975 review finding #1, issue #2694's original
	// rationale).
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

	// Degrades a malformed/negative --max-budget-tokens/--max-budget-usd to 0
	// rather than failing the run; ok is discarded since this passthrough CLI
	// wrapper has no operator-facing diagnostics channel for it today.
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
		// DefaultMaxSlices/DefaultMaxReviewRounds: entrypoint.sh's bash caller
		// has never overridden these (no flag for either here), so this
		// mirrors assemble-prompt's own default so a conflict-resolve pass
		// run under $ORCHESTRATOR gets a working loop bound rather than 0.
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
