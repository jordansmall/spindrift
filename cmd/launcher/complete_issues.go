package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

// completeIssuesTimeout bounds the discovery query `__complete-issues` runs
// so a slow or offline tracker degrades within a shell's tab-completion
// patience instead of wedging the prompt (issue #556).
const completeIssuesTimeout = 2 * time.Second

// printCompletionIssues writes one `<number>\t<title>` line per candidate in
// its current Dispatchable queue, bounded by timeout. This is the exact
// stdout contract the bash/zsh/fish completion renderers parse.
func printCompletionIssues(w io.Writer, it forge.IssueTracker, timeout time.Duration) {
	for _, iss := range discoverCompletionIssues(it, timeout) {
		fmt.Fprintf(w, "%s\t%s\n", iss.number, iss.title)
	}
}

// cmdCompleteIssues is the `__complete-issues` hidden subcommand: the
// dynamic-candidate seam the bash/zsh/fish completion renderers shell out to
// when completing the positional issue argument on dispatch/preview/recover
// (issue #556). Not a documented subcommand (absent from printHelp and the
// renderers' own subcommand lists) — it exists solely for the completion
// scripts to invoke, so it always exits 0 and never writes to stderr: a
// diagnostic has nowhere to surface mid-<TAB>.
func cmdCompleteIssues() int {
	c := loadConfig()
	it := newIssueTracker(c)
	printCompletionIssues(os.Stdout, it, completeIssuesTimeout)
	return 0
}

// discoverCompletionIssues lists the tracker's current Dispatchable queue —
// the same candidate set discoverIssues' label-query branch uses — for the
// `__complete-issues` hidden subcommand (issue #556). it.ListIssues carries
// no context (production adapters shell out to gh/curl with no deadline of
// their own), so the call runs in a goroutine and races a timer instead of
// being cancelled outright: on timeout this returns empty immediately and
// abandons the goroutine. The underlying gh/curl child process isn't killed
// either — it keeps running detached until it exits on its own — but since
// `__complete-issues` is a one-shot CLI invocation that exits right after,
// that's a single bounded stray process per timed-out completion, not a
// leak that accumulates across a long-lived one.
func discoverCompletionIssues(it forge.IssueTracker, timeout time.Duration) []issue {
	result := make(chan []forge.Issue, 1)
	go func() {
		fi, err := it.ListIssues(forge.Dispatchable)
		if err != nil {
			result <- nil
			return
		}
		result <- fi
	}()

	select {
	case fi := <-result:
		issues := make([]issue, len(fi))
		for i, f := range fi {
			issues[i] = issue{number: f.Number, title: f.Title}
		}
		return issues
	case <-time.After(timeout):
		return nil
	}
}
