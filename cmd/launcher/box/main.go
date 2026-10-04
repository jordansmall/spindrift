// Command box is the Box's in-box driver: it assembles the prompt, runs the
// first Driver run through the orchestrator, then the settle sequence that
// follows it: required-marker nudges, the synthetic outcome backstop, the
// already-resolved demotion, the lockfile scan, and bundle-out. entrypoint.sh
// execs it with the shell-local values assembly needs and it exits with the
// run's exit code (ADR 0058). It replaces the assemble-prompt call and the
// marker-gate, outcome-backstop, bundle-out and advise-only driver-exec verbs
// bash chained. Under podman box runs as PID 1 and splits itself into an init
// parent that only reaps orphans and forwards signals and a worker child that
// does the work (see init.go).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/bundleout"
	"spindrift.dev/launcher/internal/outcomebackstop"
	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/signalclient"
)

// parseFlags requires every flag, so a contract drift in entrypoint.sh fails
// loudly instead of defaulting a flag to its zero value. Emptiness is validated
// where a value is used. The assembly flags carry the shell-local values
// entrypoint.sh cannot export without changing the Driver's environment.
func parseFlags(args []string, stderr io.Writer) (inputs, error) {
	var in inputs
	var tokensRaw, usdRaw, argvOrder string
	a := &in.Assembly
	p := &a.Passthrough
	fs := flag.NewFlagSet("box", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&in.WorkDir, "work-dir", "", "the repository working directory")
	fs.StringVar(&in.OutboxDir, "outbox-dir", "", "the outbox directory")
	fs.StringVar(&a.RegistryFile, "registry", "", "path to the fragment registry JSON file")
	fs.StringVar(&a.ValidateMarkersFile, "validate-markers-registry", "", "path to the prompt-contract validateMarkers registry JSON file")
	fs.StringVar(&a.SkillsDir, "driver-skills-dir", "", "DRIVER_SKILLS_DIR, probed for baked skills")
	fs.StringVar(&a.PromptsDir, "prompts-dir", "", "PROMPTS_DIR")
	fs.StringVar(&a.AgentsPromptFiles, "agents-prompt-files", "", "nix-baked agent-name -> promptFile JSON map")
	fs.StringVar(&a.DriverAgentFilesDir, "driver-agent-files-dir", "", "opencode-style baked agent files dir, empty for claude")
	fs.StringVar(&a.CommsContractFile, "comms-contract-file", "", "COMMS_CONTRACT_FILE")
	fs.StringVar(&a.CheckContractFile, "check-contract-file", "", "CHECK_CONTRACT_FILE")
	fs.StringVar(&a.OutcomeContractFile, "outcome-contract-file", "", "OUTCOME_CONTRACT_FILE")
	fs.StringVar(&a.ResearchOutcomeContractFile, "research-outcome-contract-file", "", "RESEARCH_OUTCOME_CONTRACT_FILE")
	fs.StringVar(&p.ArgvShape.PromptStyle, "argv-prompt-style", "", "Handoff.ArgvShape.PromptStyle")
	fs.StringVar(&p.ArgvShape.PromptFlag, "argv-prompt-flag", "", "Handoff.ArgvShape.PromptFlag")
	fs.StringVar(&p.ArgvShape.ModelFlag, "argv-model-flag", "", "Handoff.ArgvShape.ModelFlag")
	fs.BoolVar(&p.ArgvShape.ModelOmitEmpty, "argv-model-omit-empty", false, "Handoff.ArgvShape.ModelOmitEmpty")
	fs.StringVar(&p.ArgvShape.AgentsFlag, "argv-agents-flag", "", "Handoff.ArgvShape.AgentsFlag")
	fs.StringVar(&p.ArgvShape.EffortFlag, "argv-effort-flag", "", "Handoff.ArgvShape.EffortFlag")
	fs.StringVar(&argvOrder, "argv-order", "", "space-separated Handoff.ArgvShape.Order")
	fs.StringVar(&p.Model, "model", "", "Handoff.Model")
	fs.StringVar(&p.Effort, "effort", "", "Handoff.Effort")
	fs.StringVar(&p.Driver, "driver", "", "Handoff.Driver")
	fs.StringVar(&p.DriverBin, "driver-bin", "", "Handoff.DriverBin")
	fs.StringVar(&p.DriverFlags, "driver-flags", "", "Handoff.DriverFlags")
	fs.StringVar(&p.HeartbeatLog, "heartbeat-log", "", "Handoff.HeartbeatLog")
	// Strings, not Int/Float64: a malformed value degrades to 0 below instead
	// of failing the run (issues #2975, #2694).
	fs.StringVar(&tokensRaw, "max-budget-tokens", "", "Handoff.Caps.MaxBudgetTokens")
	fs.StringVar(&usdRaw, "max-budget-usd", "", "Handoff.Caps.MaxBudgetUSD")
	fs.BoolVar(&p.Devshell, "devshell", false, "Handoff.Devshell")
	fs.StringVar(&p.DevshellName, "devshell-name", "", "Handoff.DevshellName")
	if err := fs.Parse(args); err != nil {
		return inputs{}, err
	}
	if fs.NArg() > 0 {
		return inputs{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	seen := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { seen[f.Name] = true })
	var missing []string
	fs.VisitAll(func(f *flag.Flag) {
		if !seen[f.Name] {
			missing = append(missing, "--"+f.Name)
		}
	})
	if len(missing) > 0 {
		return inputs{}, fmt.Errorf("missing required flag(s): %s", strings.Join(missing, ", "))
	}
	p.ArgvShape.Order = strings.Fields(argvOrder)
	// The entrypoint never passed the review-round and slice caps, so the
	// defaults are the only values they have ever had.
	p.Caps = promptassembly.Caps{MaxSlices: promptassembly.DefaultMaxSlices, MaxReviewRounds: promptassembly.DefaultMaxReviewRounds}
	p.Caps.MaxBudgetTokens, _ = promptassembly.ParseNonnegBudgetTokens(tokensRaw)
	p.Caps.MaxBudgetUSD, _ = promptassembly.ParseNonnegBudgetUSD(usdRaw)
	return in, nil
}

func mainRun(args []string, env promptassembly.Env, d deps) int {
	in, err := parseFlags(args, d.Stderr)
	if err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(d.Stderr, "box:", err)
		}
		return 2
	}
	rc, err := run(in, env, d)
	if err != nil {
		// A marker rejection is operator-facing prose: it prints bare, on the
		// stdout stream the assemble-prompt verb used.
		var rejected *promptassembly.ValidateError
		if errors.As(err, &rejected) {
			fmt.Fprintln(d.Stdout, rejected)
		} else {
			fmt.Fprintln(d.Stderr, "box:", err)
		}
		return 1
	}
	return rc
}

// execOrchestrator runs the orchestrator with the Box's stdio and returns its
// exit code the way a shell reports it: 127 when it is not on PATH, 126 when it
// cannot start, 128+signal when it is killed.
func execOrchestrator(argv []string, stdout, stderr io.Writer, stdin io.Reader) int {
	cmd := exec.Command("orchestrator", argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return exitErr.ExitCode()
	}
	fmt.Fprintln(stderr, "box: orchestrator:", err)
	if errors.Is(err, exec.ErrNotFound) {
		return 127
	}
	return 126
}

func main() {
	if rc, ok := runAsInit(os.Getpid(), os.Executable, os.Args[1:], os.Stderr); ok {
		os.Exit(rc)
	}
	d := deps{
		Assemble: assemblePrompt,
		Orchestrate: func(argv []string) int {
			return execOrchestrator(argv, os.Stdout, os.Stderr, os.Stdin)
		},
		SignalStatus:  signalclient.Status,
		Backstop:      outcomebackstop.Run,
		Demote:        outcomebackstop.DemoteAlreadyResolved,
		BundleOut:     bundleout.Run,
		WarnLockfiles: bindregistry.WarnStaleLockfiles,
		Getenv:        os.Getenv,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
	}
	os.Exit(mainRun(os.Args[1:], promptassembly.EnvFromEnviron(), d))
}
