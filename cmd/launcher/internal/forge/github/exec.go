// Package github is the gh-exec adapter: it satisfies the parent forge
// package's IssueTracker, CodeForge, and PRForge interfaces using the gh
// CLI. GH_TOKEN is read from the ambient environment; the repo slug and
// dispatch label mapping are fixed at construction time.
package github

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// ghCommandErrStderrCap bounds how much of a failed gh invocation's captured
// stderr ghCommandErr folds into the returned error's message, so a
// pathological gh failure can't dump unbounded output into an error message
// (and, transitively, whatever logs it).
const ghCommandErrStderrCap = 4096

// ghCommandErr turns a failed gh invocation's error into one that also
// surfaces gh's own stderr diagnostic, when available. description names the
// operation (e.g. "gh issue list"); err is whatever `cmd.Output()` returned.
//
// exec.Cmd.Output populates (*exec.ExitError).Stderr automatically whenever
// the command's Stderr field was left nil, so this needs no caller-wired
// stderr buffer — it recovers the diagnostic from err itself via errors.As.
// When err isn't an *exec.ExitError (e.g. *exec.Error when the gh binary is
// missing from PATH) or its Stderr is empty/whitespace-only, this degrades
// to the plain "description: err" form — never a dangling ": " separator
// with nothing after it.
func ghCommandErr(description string, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if msg := strings.TrimSpace(string(exitErr.Stderr)); msg != "" {
			if len(msg) > ghCommandErrStderrCap {
				msg = msg[:ghCommandErrStderrCap] + "...(truncated)"
			}
			return fmt.Errorf("%s: %w: %s", description, err, msg)
		}
	}
	return fmt.Errorf("%s: %w", description, err)
}

// execClient is the gh-exec adapter.
type execClient struct {
	repo          string // owner/repo slug
	labels        forge.DispatchLabels
	verdictLabels forge.VerdictLabels
	branchPrefix  string
	mergeMethod   string // "", "merge", "squash", or "rebase"; "" behaves as "rebase" (mergeMethodFlag)
	syncMethod    string // "", "rebase", or "merge"; "" behaves as "rebase"
}

// ExecOption configures an optional, construction-site-specific field on
// execClient beyond the three required positional arguments every call site
// shares. Both NewExecClient and NewReadOnlyCodeForge accept a variadic list
// of these, applied in order.
type ExecOption func(*execClient)

// WithVerdictLabels configures CompleteVerdict (the research dispatch kind's
// Complete transition); omitted for work-kind construction sites, matching
// NewFake's variadic convention for an optional, test/kind-specific config
// value.
func WithVerdictLabels(vl forge.VerdictLabels) ExecOption {
	return func(e *execClient) { e.verdictLabels = vl }
}

// WithMergeMethod sets the native `gh pr merge` method ("merge", "squash",
// or "rebase") used by both Merge and EnqueueAutoMerge. Omitted (or an empty
// string), it preserves today's --rebase default byte-for-byte
// (mergeMethodFlag).
func WithMergeMethod(method string) ExecOption {
	return func(e *execClient) { e.mergeMethod = method }
}

// WithSyncMethod sets the git verb ("rebase" or "merge") Rebase uses to
// bring a PR branch up to date with its base. Omitted (or an empty
// string), it preserves today's rebase-only behavior byte-for-byte.
func WithSyncMethod(method string) ExecOption {
	return func(e *execClient) { e.syncMethod = method }
}

// NewExecClient returns the gh-exec adapter for the given repo slug, backed
// by the gh CLI. It implements IssueTracker, CodeForge, and PRForge, so
// callers assign it to whichever seam(s) they need — the same concrete
// instance may be constructed twice (once per seam) or once and used for
// both. labels maps canonical DispatchState values to GitHub label names.
// branchPrefix is baked into AgentBranch's output. opts configures optional,
// construction-site-specific fields (WithVerdictLabels, WithMergeMethod);
// most call sites pass none.
func NewExecClient(repo string, labels forge.DispatchLabels, branchPrefix string, opts ...ExecOption) *execClient {
	e := &execClient{repo: repo, labels: labels, branchPrefix: branchPrefix}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// AgentBranch returns branchPrefix + num.
func (e *execClient) AgentBranch(num string) string {
	return e.branchPrefix + num
}

// IsGithubTracker implements the optional forge.GithubTracker marker (issue
// #2341), letting settle's ensureClosesReference discover that this
// specific adapter — and not e.g. forgejo, whose issue numbers are a
// foreign namespace from GitHub's — owns the GitHub Closes-keyword
// convention.
func (e *execClient) IsGithubTracker() bool { return true }
