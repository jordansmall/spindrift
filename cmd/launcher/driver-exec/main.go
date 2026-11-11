// Command driver-exec runs one Driver invocation, direct or inside the Target's
// devShell (ADR 0009, ADR 0014, issue #626): it spawns the Driver, tees the
// stream to a log path, filters heartbeats, and returns the Driver's exit code.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"spindrift.dev/launcher/internal/driver"
	_ "spindrift.dev/launcher/internal/driver/bundle" // registers the bundled Drivers
	"spindrift.dev/launcher/internal/driver/driverkit"
	"spindrift.dev/launcher/internal/driver/driverrows"
	"spindrift.dev/launcher/internal/driver/shadow"
	"spindrift.dev/launcher/internal/hostop"
	"spindrift.dev/launcher/internal/promptassembly"
)

// mainRun returns the exit code rather than calling os.Exit so tests can run
// it repeatedly with different argv. The FlagSet is scoped, not the global
// flag package, which panics on re-registering flags across repeated calls in
// one test binary.
func mainRun(argv []string, stdout, stderr io.Writer) int {
	// A verb is argv's first word, never a top-level flag (issue #2349).
	if len(argv) > 0 {
		switch argv[0] {
		case "assemble-prompt":
			return runAssemblePrompt(argv[1:], stdout, stderr)
		case "bind-registry":
			return runBindRegistry(argv[1:], stdout)
		case "probe-registry-socket":
			return runProbeRegistrySocket(argv[1:], stdout)
		case "probe-registry-tcp":
			return runProbeRegistryTCP(argv[1:], stdout)
		case "forward-registry-tcp":
			return runForwardRegistryTCP(argv[1:], stdout)
		case "signal":
			// The only verb handed stdin: a signal's body never travels on argv.
			return runSignal(argv[1:], os.Stdin, stdout)
		}
	}

	fs := flag.NewFlagSet("driver-exec", flag.ContinueOnError)
	fs.SetOutput(stderr)

	handoffFile := fs.String("handoff-file", "", "path to the assemble-prompt-written handoff JSON file (required)")
	promptFile := fs.String("prompt-file", "", "path to the assembled prompt text, falls back to the handoff's own PromptFile when empty")
	sessionFile := fs.String("session-file", "", "path to pre-rendered session pin/resume flags, empty for none")
	logPath := fs.String("log-path", "", "path to tee the raw Driver stream to, for outcome extraction (required)")
	topLevelRole := fs.String("top-level-role", "", "role for this pass's own top-level (no parent_tool_use_id) messages; empty defaults to implementor (issue #2092)")
	if err := fs.Parse(argv); err != nil {
		return 2
	}

	if *handoffFile == "" {
		fmt.Fprintln(stderr, "driver-exec: -handoff-file is required")
		return 1
	}
	handoff, err := promptassembly.LoadHandoffFile(*handoffFile)
	if err != nil {
		fmt.Fprintln(stderr, "driver-exec:", err)
		return 1
	}
	if err := driverrows.UseFile(handoff.DriverRowsFile); err != nil {
		fmt.Fprintln(stderr, "driver-exec:", err)
		return 1
	}

	if *promptFile == "" {
		*promptFile = handoff.PromptFile
	}
	if *promptFile == "" {
		fmt.Fprintln(stderr, "driver-exec: -prompt-file is required")
		return 1
	}
	if handoff.DriverBin == "" {
		fmt.Fprintln(stderr, "driver-exec: handoff is missing DriverBin")
		return 1
	}
	if *logPath == "" {
		fmt.Fprintln(stderr, "driver-exec: -log-path is required")
		return 1
	}

	issue := handoff.Issue
	if issue == "" {
		issue = "0"
	}
	heartbeatLog := handoff.HeartbeatLog
	if heartbeatLog == "" {
		heartbeatLog = "/tmp/heartbeat.log"
	}

	// A reviewer pass overrides only the fields the handoff's ReviewModel and
	// ReviewEffort actually carry, leaving the implementor's own Model and
	// Effort as the fallback.
	model := handoff.Model
	effort := handoff.Effort
	if *topLevelRole == driverkit.ReviewerRole {
		if handoff.ReviewModel != "" {
			model = handoff.ReviewModel
		}
		if handoff.ReviewEffort != "" {
			effort = handoff.ReviewEffort
		}
	}

	d, err := driver.New(handoff.Driver)
	if err != nil {
		fmt.Fprintln(stderr, "driver-exec:", err)
		return 1
	}

	args, err := buildDriverArgs(driverInput{
		shape:       handoff.ArgvShape,
		promptFile:  *promptFile,
		model:       model,
		effort:      effort,
		agentsFile:  handoff.AgentsFile,
		sessionFile: *sessionFile,
		driverFlags: handoff.DriverFlags,
	})
	if err != nil {
		fmt.Fprintln(stderr, "driver-exec:", err)
		return 1
	}

	// Stdout only: run.go treats an empty log as a Driver that never started.
	if model != "" {
		fmt.Fprint(stdout, hostop.EncodeSpindriftOp(hostop.SpindriftOp{Op: hostop.OpPassModel, Model: model, Effort: effort}))
	}

	rc, err := run(execConfig{
		driver:       handoff.Driver,
		driverBin:    handoff.DriverBin,
		args:         args,
		devshell:     handoff.Devshell,
		devshellName: handoff.DevshellName,
		logPath:      *logPath,
		heartbeatLog: heartbeatLog,
		issue:        issue,
		topLevelRole: *topLevelRole,
	}, stdout)
	if err != nil {
		fmt.Fprintln(stderr, "driver-exec:", err)
		return 1
	}

	exit := resolveExit(d, rc, *logPath)
	runShadow(handoff.Driver, *logPath, rc, stdout)
	return exit
}

// shadowReplay is a seam for tests that stub the wasm replay to observe what
// runShadow hands it.
var shadowReplay = shadow.Run

// shadowBudget bounds what the shadow replay adds to every Pass, which the
// orchestrator waits on before it sees the exit code. A replay that runs out is
// a summary error, never a clean run.
const shadowBudget = 2 * time.Minute

// runShadow replays the Pass log through the Connector shadowing the named
// native Driver, if a Driver row declared one, writing the comparison onto
// stdout as spindrift ops. It never alters the run: the native result stays
// authoritative, and a Connector that cannot be read, or a shadow row that was
// skipped as malformed, is a summary, not an error. rawExit is the CLI's own
// code, not the resolved one: the comparator resolves it once per side.
func runShadow(name, logPath string, rawExit int, stdout io.Writer) {
	emit := func(op hostop.SpindriftOp) { fmt.Fprint(stdout, hostop.EncodeSpindriftOp(op)) }
	resolved := driver.ResolveName(name)
	if reason, ok := driver.ShadowSkipped(name); ok {
		emit(hostop.SpindriftOp{Op: hostop.OpShadowSummary, ShadowSummary: &hostop.ShadowSummary{Driver: resolved, Error: reason}})
		return
	}
	module, ok := driver.ShadowModule(name)
	if !ok {
		return
	}
	native, ok := driver.NativeStrategy(name)
	if !ok {
		return
	}
	wasm, err := os.ReadFile(module)
	if err != nil {
		emit(hostop.SpindriftOp{Op: hostop.OpShadowSummary, ShadowSummary: &hostop.ShadowSummary{Driver: resolved, Error: err.Error()}})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), shadowBudget)
	defer cancel()
	shadowReplay(ctx, native, wasm, logPath, rawExit, emit)
}

func main() {
	os.Exit(mainRun(os.Args[1:], os.Stdout, os.Stderr))
}

// resolveExit asks the Driver for the final exit code (issue #2263); each
// Driver applies its own exit-code correction itself,
// so this call site carries no per-Driver knowledge. A ResolveExit
// error degrades to rc rather than masking a possibly successful run behind a
// resolution failure.
func resolveExit(d driver.Driver, rc int, logPath string) int {
	resolved, err := d.ResolveExit(logPath, rc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "driver-exec: resolve exit code:", err)
		return rc
	}
	return resolved
}
