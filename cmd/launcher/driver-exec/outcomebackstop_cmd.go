package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"spindrift.dev/launcher/internal/outcomebackstop"
	"spindrift.dev/launcher/internal/retry"
)

// Issue #2157 made outcome-backstop a distinct verb rather than a top-level
// flag.
func isOutcomeBackstopInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "outcome-backstop"
}

type outcomeBackstopFlags struct {
	repo               *string
	issue              *string
	branch             *string
	base               *string
	kind               *string
	hostMediatedRemote *string
	outboxRelayCapable *string
	boxWrite           *string
	recovery           *string
	maxAttempts        *int
	backoffSecs        *int
	jitterSecs         *int
	runStateFile       *string
}

// Registering the flags without parsing them lets a test read a flag's default
// value without running outcomebackstop.Run or touching a real file on disk.
func newOutcomeBackstopFlagSet() (*flag.FlagSet, *outcomeBackstopFlags) {
	fs := flag.NewFlagSet("outcome-backstop", flag.ContinueOnError)
	flags := &outcomeBackstopFlags{
		repo:               fs.String("repo", "", "path to the git repository holding branch and base (required)"),
		issue:              fs.String("issue", "", "issue number, carried into the emitted outcome line"),
		branch:             fs.String("branch", "", "agent branch name, e.g. agent/issue-42 (required)"),
		base:               fs.String("base", "", "full base ref, e.g. origin/main (required)"),
		kind:               fs.String("dispatch-kind", "work", "dispatch kind: work | research | ..."),
		hostMediatedRemote: fs.String("host-mediated-remote", "", "non-empty when the active CODE_FORGE has no writable remote at all (presence flag)"),
		outboxRelayCapable: fs.String("outbox-relay-capable", "", "non-empty when the active CODE_FORGE backend gets outbox-relay treatment under read-only (presence flag)"),
		boxWrite:           fs.String("box-write-enabled", "", "non-empty when BOX_WRITE_ENABLED was set (presence flag)"),
		recovery:           fs.String("recovery-attempted", "", "non-empty when a resume pass already ran and also produced no outcome (presence flag)"),
		maxAttempts:        fs.Int("max-attempts", 1, "bounds the push retry loop"),
		backoffSecs:        fs.Int("backoff-secs", 0, "linear backoff unit, in seconds, between push retries"),
		jitterSecs:         fs.Int("jitter-secs", 0, "linear backoff jitter, in seconds, added to each push retry wait"),
		runStateFile:       fs.String("run-state-file", "/tmp/run-state.json", "path to the run-state handoff artifact recording the reviewer's last verdict (issue #2459); empty or unreadable degrades to no-verdict-known"),
	}
	return fs, flags
}

// ADR 0007 keeps this wrapper as thin CLI glue (issue #2157): parse args and
// delegate to outcomebackstop.Run.
func runOutcomeBackstop(args []string, stdout io.Writer) int {
	fs, flags := newOutcomeBackstopFlagSet()
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *flags.repo == "" || *flags.branch == "" || *flags.base == "" {
		fmt.Fprintln(fs.Output(), "driver-exec outcome-backstop: -repo, -branch, and -base are all required")
		return 1
	}

	err := outcomebackstop.Run(outcomebackstop.Config{
		Repo:               *flags.repo,
		Issue:              *flags.issue,
		Branch:             *flags.branch,
		Base:               *flags.base,
		Kind:               *flags.kind,
		HostMediatedRemote: *flags.hostMediatedRemote != "",
		OutboxRelayCapable: *flags.outboxRelayCapable != "",
		WriteEnabled:       *flags.boxWrite != "",
		RecoveryAttempted:  *flags.recovery != "",
		MaxAttempts:        *flags.maxAttempts,
		Backoff:            time.Duration(*flags.backoffSecs) * time.Second,
		Jitter:             time.Duration(*flags.jitterSecs) * time.Second,
		Clock:              retry.RealClock(),
		RunStateFilePath:   *flags.runStateFile,
	}, stdout)
	if err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec outcome-backstop:", err)
		return 1
	}
	return 0
}
