package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"spindrift.dev/launcher/internal/forge"
)

// completeIssuesTimeout keeps a slow or offline tracker from wedging the shell
// prompt mid-completion (issue #556).
const completeIssuesTimeout = 2 * time.Second

// printCompletionIssues writes one `<number>\t<title>` line per Dispatchable
// candidate. The completion renderers parse exactly this stdout contract.
func printCompletionIssues(w io.Writer, it forge.IssueTracker, timeout time.Duration) {
	for _, iss := range discoverCompletionIssues(it, timeout) {
		fmt.Fprintf(w, "%s\t%s\n", iss.number, iss.title)
	}
}

// cmdCompleteIssues runs the `__complete-issues` hidden subcommand that the
// bash/zsh/fish completion renderers shell out to (issue #556). It always exits
// 0 and never writes to stderr: a diagnostic has nowhere to surface mid-<TAB>.
func cmdCompleteIssues() int {
	c := loadConfig()
	it := newIssueTracker(c)
	printCompletionIssues(os.Stdout, it, completeIssuesTimeout)
	return 0
}

// discoverCompletionIssues lists the tracker's Dispatchable queue for
// `__complete-issues` (issue #556). it.ListIssues takes no context, so the call
// runs in a goroutine racing a timer: on timeout this returns empty and leaves
// the goroutine and its detached gh/curl child running, which only stays bounded
// because this one-shot command exits right after.
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
			iss := newIssue(f)
			// Completion's stdout contract is number and title only, so
			// reset priority to its zero value rather than let an unused
			// non-Normal priority flow through (issue #2925).
			iss.priority = forge.PriorityNormal
			issues[i] = iss
		}
		return issues
	case <-time.After(timeout):
		return nil
	}
}
