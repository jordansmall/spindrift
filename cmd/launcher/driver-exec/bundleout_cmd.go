package main

import (
	"flag"
	"fmt"
	"io"

	"spindrift.dev/launcher/internal/bundleout"
)

// isBundleOutInvocation reports whether args (os.Args[1:]) selects the
// bundle-out subcommand: a distinct verb, not a top-level flag (issue
// #1808). The existing invocation always starts with "--" flags, so a bare
// first arg can only ever be this subcommand's name.
func isBundleOutInvocation(args []string) bool {
	return len(args) > 0 && args[0] == "bundle-out"
}

// runBundleOut is the `bundle-out` subcommand's thin CLI wrapper (ADR 0007's
// thin-exec-glue tier, issue #1808): it parses args into a bundleout.Config
// and delegates to bundleout.Run, the same producer the localloop composed
// test calls directly. Returns the process exit code.
func runBundleOut(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("bundle-out", flag.ContinueOnError)
	repo := fs.String("repo", "", "path to the git repository holding base and branch (required)")
	base := fs.String("base", "", "base ref, e.g. origin/main (required)")
	branch := fs.String("branch", "", "agent branch name (required)")
	outbox := fs.String("outbox", "", "outbox directory to write the bundle into (required)")
	issue := fs.String("issue", "", "issue number, carried into a corrective outcome line")
	priorOutcomeLine := fs.String("prior-outcome-line", "", "the Agent's own SPINDRIFT_OUTCOME line, verbatim, or empty")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	if *repo == "" || *base == "" || *branch == "" || *outbox == "" {
		fmt.Fprintln(fs.Output(), "driver-exec bundle-out: -repo, -base, -branch, and -outbox are all required")
		return 1
	}

	if err := bundleout.Run(bundleout.Config{
		Repo:             *repo,
		Base:             *base,
		Branch:           *branch,
		OutboxDir:        *outbox,
		Issue:            *issue,
		PriorOutcomeLine: *priorOutcomeLine,
	}, stdout); err != nil {
		fmt.Fprintln(fs.Output(), "driver-exec bundle-out:", err)
		return 1
	}
	return 0
}
