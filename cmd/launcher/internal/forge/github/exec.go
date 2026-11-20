// Package github implements forge's IssueTracker, CodeForge, and PRForge
// interfaces. Every call goes through the routed github Connector row; with no
// routed row it fails with ErrNoRoute.
package github

import (
	"spindrift.dev/launcher/internal/forge"
)

type execClient struct {
	repo          string // owner/repo slug
	labels        forge.DispatchLabels
	verdictLabels forge.VerdictLabels
	mergeMethod   string // "" or "rebase"; the Connector merges by rebase only (checkRoutedMergeMethod)
	syncMethod    string // "", "rebase", or "merge"; "" behaves as "rebase"
	userName      string // committer identity Rebase sets on its clone; empty keeps ambient git config
	userEmail     string
	baseBranch    string // core's BASE_BRANCH; the base a routed row's Rebase syncs onto
}

// ExecOption sets one optional execClient field. NewExecClient applies
// these in order.
type ExecOption func(*execClient)

// WithVerdictLabels configures CompleteVerdict, the research dispatch kind's
// Complete transition. Work-kind call sites omit it.
func WithVerdictLabels(vl forge.VerdictLabels) ExecOption {
	return func(e *execClient) { e.verdictLabels = vl }
}

// WithMergeMethod sets the MERGE_METHOD the Connector must honour: only
// "rebase" or unset, which checkRoutedMergeMethod enforces.
func WithMergeMethod(method string) ExecOption {
	return func(e *execClient) { e.mergeMethod = method }
}

// WithSyncMethod sets the git verb ("rebase" or "merge") Rebase uses to bring
// a PR branch up to date with its base. Omitted, it stays on rebase.
func WithSyncMethod(method string) ExecOption {
	return func(e *execClient) { e.syncMethod = method }
}

// WithBaseBranch sets the branch a routed row's Rebase syncs a PR onto: the
// Connector's PR read carries no base, so core's own BASE_BRANCH stands in.
func WithBaseBranch(branch string) ExecOption {
	return func(e *execClient) { e.baseBranch = branch }
}

// WithCommitIdentity sets the committer identity Rebase configures on its
// throwaway clone, so a sync that writes a commit does not depend on ambient
// git config, which a bare CI runner lacks. An empty name or email leaves that
// key unset.
func WithCommitIdentity(name, email string) ExecOption {
	return func(e *execClient) { e.userName, e.userEmail = name, email }
}

// NewExecClient returns the Connector-backed adapter for the given repo slug. One
// instance implements IssueTracker, CodeForge, and PRForge, so a caller may
// use it for every seam it needs or construct one per seam. labels maps
// canonical DispatchState values to GitHub label names.
func NewExecClient(repo string, labels forge.DispatchLabels, opts ...ExecOption) *execClient {
	e := &execClient{repo: repo, labels: labels}
	for _, opt := range opts {
		opt(e)
	}
	return e
}
