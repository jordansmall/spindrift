// Command box is the Box's in-box driver: it runs the first Driver run through
// the orchestrator, then the settle sequence that follows it: required-marker
// nudges, the synthetic outcome backstop, the already-resolved demotion, the
// lockfile scan, and bundle-out. entrypoint.sh execs it after prompt assembly
// and it exits with the run's exit code (ADR 0058). It replaces the
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
// where a value is used.
func parseFlags(args []string, stderr io.Writer) (inputs, error) {
	var in inputs
	fs := flag.NewFlagSet("box", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&in.HandoffFile, "handoff-file", "", "the shared assemble-prompt handoff JSON")
	fs.StringVar(&in.WorkDir, "work-dir", "", "the repository working directory")
	fs.StringVar(&in.OutboxDir, "outbox-dir", "", "the outbox directory")
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
		fmt.Fprintln(d.Stderr, "box:", err)
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
