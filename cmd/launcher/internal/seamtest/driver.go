package seamtest

import (
	"fmt"
	"io"
	"os"
)

// DriverConfig is the Driver fake's (registered as "claude") JSON config.
type DriverConfig struct {
	// Record is the file every invocation's argv is appended to.
	Record string `json:"record"`
	// Runs scripts the invocations in order; ones past the end behave as the
	// zero DriverRun (exit 0, no output).
	Runs []DriverRun `json:"runs"`
}

// DriverRun is one scripted Driver invocation.
type DriverRun struct {
	// Stdout is written verbatim, e.g. claude stream-json result lines.
	Stdout string `json:"stdout"`
	Exit   int    `json:"exit"`
}

func driverMain(args []string) int {
	return driverFake(args, os.Stdout, os.Stderr)
}

func driverFake(args []string, stdout, stderr io.Writer) int {
	var cfg DriverConfig
	if err := loadConfig("claude", &cfg); err != nil {
		fmt.Fprintf(stderr, "claude fake: %v\n", err)
		return fakeConfigExit
	}
	prior, err := appendRecord(cfg.Record, args)
	if err != nil {
		fmt.Fprintf(stderr, "claude fake: record: %v\n", err)
		return fakeConfigExit
	}
	var run DriverRun
	if len(prior) < len(cfg.Runs) {
		run = cfg.Runs[len(prior)]
	}
	io.WriteString(stdout, run.Stdout)
	return run.Exit
}
