package seamtest

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// OrchestratorConfig is the orchestrator fake's JSON config.
type OrchestratorConfig struct {
	// Record is the file the fake's own argv is appended to.
	Record string `json:"record"`
}

func orchestratorMain(args []string) int {
	return orchestratorFake(args, os.Stdout, os.Stderr, execClaude)
}

// driverRunner runs one Driver invocation with argv, writing its stdout to
// stdout, and returns its exit code.
type driverRunner func(argv []string, stdout, stderr io.Writer) int

// execClaude runs the `claude` fake from PATH.
func execClaude(argv []string, stdout, stderr io.Writer) int {
	cmd := exec.Command("claude", argv...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	}
	fmt.Fprintf(stderr, "orchestrator fake: claude: %v\n", err)
	return fakeConfigExit
}

// orchestratorFake stands in for the in-box orchestrator on one resume pass.
// It records its argv, reads only --prompt-file, --session-file and
// --log-path (the rest, such as --handoff-file and --manifest-path, stay in
// the record), and runs the Driver as `<fields of the session file> -p
// <prompt file content>`: the shape the claude Driver's session flags and
// prompt reach the CLI in. The Driver's stdout is teed to --log-path and the
// fake's own stdout; the Driver's exit code is the fake's.
func orchestratorFake(args []string, stdout, stderr io.Writer, drive driverRunner) int {
	var cfg OrchestratorConfig
	if err := loadConfig("orchestrator", &cfg); err != nil {
		fmt.Fprintf(stderr, "orchestrator fake: %v\n", err)
		return fakeConfigExit
	}
	if _, err := appendRecord(cfg.Record, args); err != nil {
		fmt.Fprintf(stderr, "orchestrator fake: record: %v\n", err)
		return fakeConfigExit
	}

	flags := map[string]string{}
	for i := 0; i < len(args); i++ {
		name, val, hasVal := strings.Cut(args[i], "=")
		switch name {
		case "--prompt-file", "--session-file", "--log-path":
			if !hasVal && i+1 < len(args) {
				i++
				val = args[i]
			}
			flags[name] = val
		}
	}
	for _, name := range []string{"--prompt-file", "--session-file", "--log-path"} {
		if flags[name] == "" {
			fmt.Fprintf(stderr, "orchestrator fake: missing %s\n", name)
			return 2
		}
	}

	prompt, err := os.ReadFile(flags["--prompt-file"])
	if err != nil {
		fmt.Fprintf(stderr, "orchestrator fake: %v\n", err)
		return 2
	}
	session, err := os.ReadFile(flags["--session-file"])
	if err != nil {
		fmt.Fprintf(stderr, "orchestrator fake: %v\n", err)
		return 2
	}
	log, err := os.OpenFile(flags["--log-path"], os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "orchestrator fake: %v\n", err)
		return 2
	}
	defer log.Close()

	argv := append(strings.Fields(string(session)), "-p", string(prompt))
	return drive(argv, io.MultiWriter(log, stdout), stderr)
}
