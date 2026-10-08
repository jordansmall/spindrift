package seamtest

import (
	"fmt"
	"io"
	"os"
	"slices"
)

// BwrapConfig is the bwrap fake's JSON config. bwrap has no subcommands, so
// every invocation is a Box run and Runs scripts them in order, except the
// launcher's overlay probe (runner.ValidateOverlay), which exits 0 silently
// and takes no scripted run.
type BwrapConfig struct {
	// Record is the file every invocation's argv is appended to.
	Record string `json:"record"`
	// Runs scripts the invocations; ones past the end behave as the zero
	// PodmanRun. Outcome gains its nonce from `--setenv RUN_NONCE <v>`.
	Runs []PodmanRun `json:"runs"`
}

// IsBwrapOverlayProbe reports whether argv is the launcher's writable-overlay
// probe (`... --overlay-src <dir> ... -- true`) rather than a Box run.
func IsBwrapOverlayProbe(args []string) bool {
	n := len(args)
	return n >= 2 && args[n-2] == "--" && args[n-1] == "true" && slices.Contains(args, "--overlay-src")
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
	if IsBwrapOverlayProbe(args) {
		return 0
	}
	boxRuns := 0
	for _, p := range prior {
		if !IsBwrapOverlayProbe(p) {
			boxRuns++
		}
	}
	var run PodmanRun
	if boxRuns < len(cfg.Runs) {
		run = cfg.Runs[boxRuns]
	}
	io.WriteString(stdout, boxOutput(run, args))
	return run.Exit
}
