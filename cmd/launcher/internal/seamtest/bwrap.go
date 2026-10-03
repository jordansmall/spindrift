package seamtest

import (
	"fmt"
	"io"
	"os"
)

// BwrapConfig is the bwrap fake's JSON config. bwrap has no subcommands, so
// every invocation is a Box run and Runs scripts them in order.
type BwrapConfig struct {
	// Record is the file every invocation's argv is appended to.
	Record string `json:"record"`
	// Runs scripts the invocations; ones past the end behave as the zero
	// PodmanRun. Outcome gains its nonce from `--setenv RUN_NONCE <v>`.
	Runs []PodmanRun `json:"runs"`
}

func bwrapMain(args []string) int {
	return bwrapFake(args, os.Stdout, os.Stderr)
}

func bwrapFake(args []string, stdout, stderr io.Writer) int {
	var cfg BwrapConfig
	if err := loadConfig("bwrap", &cfg); err != nil {
		fmt.Fprintf(stderr, "bwrap fake: %v\n", err)
		return fakeConfigExit
	}
	prior, err := appendRecord(cfg.Record, args)
	if err != nil {
		fmt.Fprintf(stderr, "bwrap fake: record: %v\n", err)
		return fakeConfigExit
	}
	var run PodmanRun
	if len(prior) < len(cfg.Runs) {
		run = cfg.Runs[len(prior)]
	}
	io.WriteString(stdout, boxOutput(run, args))
	return run.Exit
}
