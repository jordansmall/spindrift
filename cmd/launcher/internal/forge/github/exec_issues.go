package github

import (
	"spindrift.dev/launcher/internal/forge"
)

func (e *execClient) ListIssues(state forge.DispatchState) ([]forge.Issue, error) {
	row, err := routeRow(e.connectors)
	if err != nil {
		return nil, err
	}
	return e.listIssues(row, state)
}

// ListOpenIssues returns every open issue in ascending number order. Unlike
// ListIssues it filters on no label, so issues with no dispatch label
// yet are included.
func (e *execClient) ListOpenIssues() ([]forge.Issue, error) {
	row, err := routeRow(e.connectors)
	if err != nil {
		return nil, err
	}
	return e.listOpenIssues(row)
}

// ListIssuesWithLabels implements forge.LabeledBacklogLister (issues #3873,
// #3609). state scopes the scan to open or closed issues -- a closed finding
// is a durable triage decision the host must not refile. The Connector scans
// one label at a time and merges them (forge.MergeLabeledIssues).
func (e *execClient) ListIssuesWithLabels(state forge.IssueState, labels []string) ([]forge.Issue, error) {
	row, err := routeRow(e.connectors)
	if err != nil {
		return nil, err
	}
	return e.listIssuesWithLabels(row, state, labels)
}

func (e *execClient) Issue(num string) (forge.Issue, error) {
	row, err := routeRow(e.connectors)
	if err != nil {
		return forge.Issue{}, err
	}
	return e.issue(row, num)
}

// Comments returns issue num's comments oldest-first, the order
// forge.IssueText assumes when it windows to the last 10.
func (e *execClient) Comments(num string) ([]forge.Comment, error) {
	row, err := routeRow(e.connectors)
	if err != nil {
		return nil, err
	}
	return e.comments(row, num)
}

var _ forge.CommentLister = (*execClient)(nil)

// StateLabels returns the DispatchLabels e resolves DispatchState values through.
func (e *execClient) StateLabels() forge.DispatchLabels {
	return e.labels
}

// TransitionState swaps the from-state label for the to-state label on issue
// num, also removing forge.TransitionRemoveLabels: a claim strips stale
// terminal and research verdict labels, as the dispatch workflow does
// (#1985), so a re-triggered or recovered issue cannot run still wearing a
// prior run's terminal label; a landing strips a stale Failed (#4651). A
// claim errors with forge.ErrAlreadyClaimed, editing nothing, when InProgress
// is already present (#3887); the check is read-then-edit, not atomic.
func (e *execClient) TransitionState(num string, from, to forge.DispatchState) error {
	row, err := routeRow(e.connectors)
	if err != nil {
		return err
	}
	return e.transition(row, num, from, to)
}

// AddLabels implements forge.IssueLabeler (issue #4074).
func (e *execClient) AddLabels(num string, labels []string) error {
	row, err := routeRow(e.connectors)
	if err != nil {
		return err
	}
	return e.addLabels(row, num, labels)
}

var _ forge.IssueLabeler = (*execClient)(nil)

// CompleteVerdict swaps the InProgress label on issue num for verdict's
// terminal label, resolved from verdictLabels rather than DispatchLabels.
// It first asserts num carries InProgress, so a double-dispatched issue errors
// instead of silently keeping InProgress alongside the verdict label. The check
// is not atomic: another process can flip the label between the read and edit.
func (e *execClient) CompleteVerdict(num string, verdict forge.Verdict) error {
	row, err := routeRow(e.connectors)
	if err != nil {
		return err
	}
	return e.completeVerdict(row, num, verdict)
}

var _ forge.NativeDepsReader = (*execClient)(nil)

// NativeDepsOf reads issue num's blocked_by relationships from GitHub's
// native issue-dependencies API.
func (e *execClient) NativeDepsOf(num string) ([]string, error) {
	row, err := routeRow(e.connectors)
	if err != nil {
		return nil, err
	}
	return e.nativeDeps(row, num)
}

// MatchesRoute reports whether issue num is in the client's Issue route, the
// per-issue read of the route Issue itself leaves unnarrowed (ADR 0075).
func (e *execClient) MatchesRoute(num string) (bool, error) {
	row, err := routeRow(e.connectors)
	if err != nil {
		return false, err
	}
	return e.matchesRoute(row, num)
}

// BlocksOf returns the issues num blocks, read from GitHub's native
// issue-dependencies API. No prose grammar declares a forward "blocks"
// relationship, so unlike DepsOf there is no body-text fallback and a lookup
// failure is returned directly (issue #1744).
func (e *execClient) BlocksOf(num string) ([]forge.Dependency, error) {
	row, err := routeRow(e.connectors)
	if err != nil {
		return nil, err
	}
	return e.blocksOf(row, num)
}

// PriorClaimState reads issue num's timeline for the most recent "unlabeled"
// event naming the Complete or Failed label (issue #2477). A claim's
// TransitionState(_, InProgress) strips that label before recoverByNumber runs,
// so the timeline is the only route back to it.
func (e *execClient) PriorClaimState(num string) (forge.DispatchState, bool, error) {
	row, err := routeRow(e.connectors)
	if err != nil {
		return forge.Untriaged, false, err
	}
	return e.priorClaimState(row, num)
}

// CloseMergedIssue closes issue num as a backstop for a merged agent PR whose
// Closes #<N> keyword GitHub's auto-close missed (issue #1892), and as the
// close for a status=already-resolved outcome (issue #4015), which must never
// read as "not planned". An already-closed issue is a no-op.
func (e *execClient) CloseMergedIssue(num string) error {
	row, err := routeRow(e.connectors)
	if err != nil {
		return err
	}
	return e.closeMergedIssue(row, num)
}

func (e *execClient) Comment(num, body string) error {
	row, err := routeRow(e.connectors)
	if err != nil {
		return err
	}
	return e.comment(row, num, body)
}

// PostIssue files an issue against this adapter's own repo, never a
// caller-supplied one, per the do-not-trust-the-agent-target invariant (issues
// #2028, #1949). It returns the created issue's html_url.
func (e *execClient) PostIssue(title, body string, labels []string) (string, error) {
	row, err := routeRow(e.connectors)
	if err != nil {
		return "", err
	}
	return e.fileIssue(row, title, body, labels)
}

var _ forge.HostPostedCommenter = (*execClient)(nil)
var _ forge.HostPostedIssueFiler = (*execClient)(nil)

// SearchIssues implements forge.IssueSearcher (issue #6895). text comes from an
// untrusted Box; the Connector quotes each term and rejects text that could
// break out of the quoting.
func (e *execClient) SearchIssues(text string, state forge.IssueState, label string, limit int) ([]forge.Issue, error) {
	row, err := routeRow(e.connectors)
	if err != nil {
		return nil, err
	}
	return e.searchIssues(row, text, state, label, limit)
}
