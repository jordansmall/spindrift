package seamtest

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PodmanConfig is the podman fake's JSON config.
type PodmanConfig struct {
	// Record is the file every invocation's argv is appended to.
	Record string `json:"record"`
	// ImagePresent answers `image exists` / `image inspect`.
	ImagePresent bool `json:"image_present"`
	// Runs scripts the `run` invocations in order; runs past the end behave
	// as the zero PodmanRun (exit 0, no output).
	Runs []PodmanRun `json:"runs"`
}

// PodmanRun is one scripted `podman run`.
type PodmanRun struct {
	Exit int `json:"exit"`
	// Outcome is written to stdout, e.g.
	// "SPINDRIFT_OUTCOME issue=7 status=done\n". Like a well-behaved Box it
	// gains the run's own " nonce=<RUN_NONCE>" (from the run argv's
	// -e RUN_NONCE=...) unless it already carries one; the launcher rejects
	// a line without it as spoofed.
	Outcome string `json:"outcome"`
	// Intents are raw issue-intent JSON payloads (a butler finding), each
	// written ahead of Outcome as a "SPINDRIFT_ISSUE_INTENT <RUN_NONCE>
	// <base64>" line, the log-carrier grammar. Without RUN_NONCE in the run
	// argv they are omitted: the launcher would reject them as unverified.
	Intents []string `json:"intents"`
}

// podmanMain serves both podman and docker; the invoked name picks the config.
func podmanMain(args []string) int {
	return podmanFake(filepath.Base(os.Args[0]), args, os.Stdout, os.Stderr)
}

// podmanFake mirrors tests/fakes/runtime: every subcommand succeeds silently
// except the probes whose failure the launcher branches on.
func podmanFake(tool string, args []string, stdout, stderr io.Writer) int {
	var cfg PodmanConfig
	if err := loadConfig(tool, &cfg); err != nil {
		fmt.Fprintf(stderr, "%s fake: %v\n", tool, err)
		return fakeConfigExit
	}
	prior, err := appendRecord(cfg.Record, args)
	if err != nil {
		fmt.Fprintf(stderr, "%s fake: record: %v\n", tool, err)
		return fakeConfigExit
	}
	sub := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	switch sub(0) {
	case "image":
		if sub(1) == "inspect" || sub(1) == "exists" {
			if cfg.ImagePresent {
				return 0
			}
			return 1
		}
	case "inspect":
		// Container liveness probe: no container exists.
		return 1
	case "run":
		n := 0
		for _, p := range prior {
			if len(p) > 0 && p[0] == "run" {
				n++
			}
		}
		var run PodmanRun
		if n < len(cfg.Runs) {
			run = cfg.Runs[n]
		}
		io.WriteString(stdout, boxOutput(run, args))
		return run.Exit
	}
	return 0
}

// boxOutput is what a scripted Box run prints: its intents, then its outcome.
func boxOutput(run PodmanRun, args []string) string {
	var b strings.Builder
	if nonce, ok := runNonce(args); ok {
		for _, raw := range run.Intents {
			fmt.Fprintf(&b, "SPINDRIFT_ISSUE_INTENT %s %s\n", nonce, base64.StdEncoding.EncodeToString([]byte(raw)))
		}
	}
	b.WriteString(withNonce(run.Outcome, args))
	return b.String()
}

func withNonce(outcome string, args []string) string {
	if outcome == "" || strings.Contains(outcome, " nonce=") {
		return outcome
	}
	nonce, ok := runNonce(args)
	if !ok {
		return outcome
	}
	line := strings.TrimSuffix(outcome, "\n")
	return line + " nonce=" + nonce + outcome[len(line):]
}

// runNonce finds RUN_NONCE in a run argv: `-e RUN_NONCE=<v>` for the OCI
// runtimes, `--setenv RUN_NONCE <v>` for bwrap.
func runNonce(args []string) (string, bool) {
	for i, a := range args {
		if v, ok := strings.CutPrefix(a, "RUN_NONCE="); ok && i > 0 && args[i-1] == "-e" {
			return v, true
		}
		if a == "RUN_NONCE" && i > 0 && args[i-1] == "--setenv" && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}
