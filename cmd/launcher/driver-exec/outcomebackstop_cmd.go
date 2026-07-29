package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"spindrift.dev/launcher/internal/outcomebackstop"
	"spindrift.dev/launcher/internal/retry"
)

// isOutcomeBackstopInvocation reports whether args (os.Args[1:]) selects the
// outcome-backstop subcommand: a distinct verb, not a top-level flag (issue
// #2157), mirroring isBundleOutInvocation.
func isOutcomeBackstopInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "outcome-backstop"
}

// runOutcomeBackstop is the `outcome-backstop` subcommand's thin CLI wrapper
// (ADR 0007's thin-exec-glue tier, issue #2157): it parses args into an
// outcomebackstop.Config and delegates to outcomebackstop.Run, the same
// producer entrypoint.sh's retired emit_outcome_backstop shell function used
// to implement in bash. Returns the process exit code.
func runOutcomeBackstop(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("outcome-backstop", flag.ContinueOnError)
	repo := fs.String("repo", "", "path to the git repository holding branch and base (required)")
	issue := fs.String("issue", "", "issue number, carried into the emitted outcome line")
	branch := fs.String("branch", "", "agent branch name, e.g. agent/issue-42 (required)")
	base := fs.String("base", "", "full base ref, e.g. origin/main (required)")
	kind := fs.String("dispatch-kind", "work", "dispatch kind: work | research | ...")
	forge := fs.String("code-forge", "github", "code forge: github | git | local")
	boxWrite := fs.String("box-write-enabled", "", "non-empty when BOX_WRITE_ENABLED was set (presence flag)")
	nonce := fs.String("nonce", "", "this run's own control nonce (RUN_NONCE), appended to the emitted line")
	recovery := fs.String("recovery-attempted", "", "non-empty when a resume pass already ran and also produced no outcome (presence flag)")
	maxAttempts := fs.Int("max-attempts", 1, "bounds the push retry loop")
	backoffSecs := fs.Int("backoff-secs", 0, "linear backoff unit, in seconds, between push retries")
	jitterSecs := fs.Int("jitter-secs", 0, "linear backoff jitter, in seconds, added to each push retry wait")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *repo == "" || *branch == "" || *base == "" {
		fmt.Fprintln(fs.Output(), "driver-exec outcome-backstop: -repo, -branch, and -base are all required")
		return 1
	}

	err := outcomebackstop.Run(outcomebackstop.Config{
		Repo:              *repo,
		Issue:             *issue,
		Branch:            *branch,
		Base:              *base,
		Kind:              *kind,
		CodeForge:         *forge,
		WriteEnabled:      *boxWrite != "",
		RecoveryAttempted: *recovery != "",
		Nonce:             *nonce,
		MaxAttempts:       *maxAttempts,
		Backoff:           time.Duration(*backoffSecs) * time.Second,
		Jitter:            time.Duration(*jitterSecs) * time.Second,
		Clock:             retry.RealClock(),
	}, stdout)
	if err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec outcome-backstop:", err)
		return 1
	}
	return 0
}
