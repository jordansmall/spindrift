// Command box is the Box's in-box driver: it first checks the required env
// (the dispatch key, keying and git identity, plus the forge token and repo
// unless fully local or self-contained with no reachable tracker), prints the
// writable-store notice when NIX_STORE_WRITABLE=true, clones the Target repo
// (issue #4302) right after the kind read, recovers the agent
// branch and runs the pre-work rebase (issue #4301) once it is cloned,
// ahead of the Forgejo CLI and the guards, wires FORGEJO_TOKEN into
// fj and installs the read-only guards (issue #4299), binds the registry proxy
// (the Forwarder, the home configs and the in-tree rewrite, reverted on exit;
// issue #4298), decides the toolchain (devShell probe, prefetch hook and
// toolchain hint; issue #4297), lays out the Driver skills dir and the home
// agent files from HARNESS_SKILLS_DIR, OPERATOR_SKILLS_DIR and
// HARNESS_HOME_AGENT_DIR, which it defaults itself (issue #4296), then runs
// the pre-work conflict-resolve pass when the rebase stopped on conflicts,
// assembles the prompt, runs the first Driver run through the orchestrator,
// then the settle sequence that follows it: required-marker nudges, the
// synthetic outcome backstop, the already-resolved demotion, the lockfile
// scan, and bundle-out. entrypoint.sh is the generated shim that only execs it
// with the preamble values the run needs, and it exits with the run's exit
// code (ADR 0058). It replaces
// the assemble-prompt call, the branch-recovery and prework-rebase phases, the
// toolchain-nudge, devShell-probe, prefetch and
// bind-registry phases, and the conflict-resolve phase, marker-gate,
// outcome-backstop, bundle-out and advise-only driver-exec verbs bash
// chained. Under podman box runs as PID 1 and splits itself into an init
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
	"strconv"
	"strings"
	"syscall"

	"spindrift.dev/launcher/internal/bindregistry"
	"spindrift.dev/launcher/internal/boxclone"
	"spindrift.dev/launcher/internal/branchrecovery"
	"spindrift.dev/launcher/internal/bundleout"
	"spindrift.dev/launcher/internal/outcomebackstop"
	"spindrift.dev/launcher/internal/promptassembly"
	"spindrift.dev/launcher/internal/signalclient"
	"spindrift.dev/launcher/internal/toolchain"
)

// parseFlags requires every flag, so a contract drift in entrypoint.sh fails
// loudly instead of defaulting a flag to its zero value. Emptiness is validated
// where a value is used. The assembly flags carry the shell-local values
// entrypoint.sh cannot export without changing the Driver's environment.
func parseFlags(args []string, stderr io.Writer) (inputs, error) {
	var in inputs
	var tokensRaw, usdRaw, argvOrder, omitEmptyRaw string
	a := &in.Assembly
	p := &a.Passthrough
	fs := flag.NewFlagSet("box", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&in.WorkDir, "work-dir", "", "the repository working directory")
	fs.StringVar(&in.OutboxDir, "outbox-dir", "", "the outbox directory")
	fs.StringVar(&in.BranchPrefix, "branch-prefix", "", "BRANCH_PREFIX; the agent branch is this plus the dispatch key")
	fs.StringVar(&in.DriverBashTimeoutMS, "driver-bash-timeout-ms", "", "DRIVER_BASH_TIMEOUT_MS, empty when unset")
	fs.StringVar(&in.DriverBashTimeoutEnv, "driver-bash-timeout-env", "", "DRIVER_BASH_TIMEOUT_ENV, the env var names the timeout is exported under")
	fs.StringVar(&in.DevShellName, "dev-shell-name", "", "DEV_SHELL_NAME, exported for the devShell probe")
	fs.StringVar(&in.DevShellProbeTimeout, "dev-shell-probe-timeout", "", "DEV_SHELL_PROBE_TIMEOUT, exported for the devShell probe")
	fs.StringVar(&in.RunStateFile, "run-state-file", "", "path to the run-state file the backstop reads the reviewer's last verdict from")
	fs.StringVar(&in.ForbiddenMarkersFile, "forbidden-markers-registry", "", "path to the prompt-contract forbiddenMarkers registry JSON file, read by the read-only guards")
	fs.StringVar(&a.RegistryFile, "registry", "", "path to the fragment registry JSON file")
	fs.StringVar(&a.ValidateMarkersFile, "validate-markers-registry", "", "path to the prompt-contract validateMarkers registry JSON file")
	fs.StringVar(&a.SkillsDir, "driver-skills-dir", "", "DRIVER_SKILLS_DIR, probed for baked skills")
	fs.StringVar(&in.DriverSessionCacheDir, "driver-session-cache-dir", "", "DRIVER_SESSION_CACHE_DIR, empty when the Driver has none")
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
	fs.StringVar(&omitEmptyRaw, "argv-model-omit-empty", "", "DRIVER_ARGV_MODEL_OMIT_EMPTY as rendered: empty is false, else a Go bool (1, 0, true)")
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
	if omitEmptyRaw != "" {
		omit, err := strconv.ParseBool(omitEmptyRaw)
		if err != nil {
			return inputs{}, fmt.Errorf("--argv-model-omit-empty: %w", err)
		}
		p.ArgvShape.ModelOmitEmpty = omit
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
	in = withDirDefaults(in, d.Getenv)
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
		Clone:         boxclone.Clone,
		Chdir:         os.Chdir,
		Setenv:        os.Setenv,
		Recover:       branchrecovery.Recover,
		PublishBranch: branchrecovery.Publish,
		WarnLockfiles: bindregistry.WarnStaleLockfiles,
		Nix:           toolchain.RunNix(os.Stdout),
		RunCmd:        func(cmd *exec.Cmd) error { return cmd.Run() },
		LookPath:      exec.LookPath,
		Git:           gitRun,
		GitOutput:     gitOutput,
		AbortRebase:   abortRebase,
		Registry:      realRegistryDeps(),
		Guards:        realGuardsDeps(),
		Getenv:        os.Getenv,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
	}
	os.Exit(mainRun(os.Args[1:], promptassembly.EnvFromEnviron(), d))
}
