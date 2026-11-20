package github

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/forge"
)

// ghLabel is the label shape gh issue view/list emit under --json labels.
type ghLabel struct {
	Name string `json:"name"`
}

func labelNames(labels []ghLabel) []string {
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = l.Name
	}
	return names
}

func (e *execClient) ListIssues(state forge.DispatchState) ([]forge.Issue, error) {
	row, err := readRow()
	if err != nil {
		return nil, err
	}
	return e.listIssuesViaConnector(row, state)
}

// ListOpenIssues returns every open issue in ascending number order. Unlike
// ListIssues it filters on no label, so issues with no dispatch label
// yet are included.
func (e *execClient) ListOpenIssues() ([]forge.Issue, error) {
	row, err := readRow()
	if err != nil {
		return nil, err
	}
	return e.listOpenIssuesViaConnector(row)
}

// ListIssuesWithLabels implements forge.LabeledBacklogLister (issue #3873,
// building on the #3609 review). state scopes the scan to open or closed
// issues -- a closed finding is a durable triage decision the host must not
// refile. The Connector scans one label at a time and merges them
// (forge.MergeLabeledIssues).
func (e *execClient) ListIssuesWithLabels(state forge.IssueState, labels []string) ([]forge.Issue, error) {
	row, err := readRow()
	if err != nil {
		return nil, err
	}
	return e.listIssuesWithLabelsViaConnector(row, state, labels)
}

func (e *execClient) Issue(num string) (forge.Issue, error) {
	row, err := readRow()
	if err != nil {
		return forge.Issue{}, err
	}
	return e.issueViaConnector(row, num)
}

// nativeIssue reads issue num through gh. Only CloseMergedIssue's native write
// path calls it, to learn whether the issue is already closed; every read goes
// through readRow.
func (e *execClient) nativeIssue(num string) (forge.Issue, error) {
	cmd := exec.Command("gh", "issue", "view", num,
		"--repo", e.repo,
		"--json", "number,title,body,state,labels",
	)
	out, err := cmd.Output()
	if err != nil {
		return forge.Issue{}, ghCommandErr(fmt.Sprintf("gh issue view %s", num), err)
	}
	var raw struct {
		Number int       `json:"number"`
		Title  string    `json:"title"`
		Body   string    `json:"body"`
		State  string    `json:"state"`
		Labels []ghLabel `json:"labels"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return forge.Issue{}, fmt.Errorf("parse issue %s: %w", num, err)
	}
	names := labelNames(raw.Labels)
	return forge.Issue{
		Number:   strconv.Itoa(raw.Number),
		Title:    raw.Title,
		Body:     raw.Body,
		State:    forge.IssueState(raw.State),
		Labels:   names,
		Priority: forge.ResolvePriority(names),
	}, nil
}

// Comments returns issue num's comments oldest-first, the order
// forge.IssueText assumes when it windows to the last 10.
func (e *execClient) Comments(num string) ([]forge.Comment, error) {
	row, err := readRow()
	if err != nil {
		return nil, err
	}
	return e.commentsViaConnector(row, num)
}

var _ forge.CommentLister = (*execClient)(nil)

// StateLabels returns the DispatchLabels e resolves DispatchState values through.
func (e *execClient) StateLabels() forge.DispatchLabels {
	return e.labels
}

// TransitionState swaps the from-state label for the to-state label on issue
// num, removing the labels forge.TransitionRemoveLabels names. A claim
// (to == InProgress) also strips the stale terminal labels, matching the
// dispatch workflow's claim-remove-labels set (#1985), so a re-triggered or
// recovered issue cannot run while still wearing a prior run's terminal label;
// a landing (to == Complete) strips a stale Failed (#4651). A claim also
// strips any research verdict label.
//
// A claim first reads num's labels and errors on forge.ErrAlreadyClaimed
// without editing anything when InProgress is already present (#3887). The
// check is read-then-edit, not atomic: another claimer can land between the
// read and the edit.
func (e *execClient) TransitionState(num string, from, to forge.DispatchState) error {
	if row, ok := routedRow(); ok {
		return e.transitionViaConnector(row, num, from, to)
	}
	if to == forge.InProgress {
		labels, err := e.issueLabels(num)
		if err != nil {
			return err
		}
		if e.labels.AlreadyClaimed(from, to, labels) {
			return fmt.Errorf("gh issue edit %s: %w (%q)", num, forge.ErrAlreadyClaimed, e.labels.Label(to))
		}
	}
	add := e.labels.Label(to)
	args := []string{"issue", "edit", num, "--repo", e.repo, "--add-label", add}
	for _, remove := range forge.TransitionRemoveLabels(e.labels, e.verdictLabels, from, to) {
		args = append(args, "--remove-label", remove)
	}
	cmd := exec.Command("gh", args...)
	if _, err := cmd.Output(); err != nil {
		return ghCommandErr(fmt.Sprintf("gh issue edit %s", num), err)
	}
	return nil
}

// AddLabels implements forge.IssueLabeler (issue #4074): one gh issue edit
// call carrying a --add-label flag per label, mirroring TransitionState's
// exec and error-handling style.
func (e *execClient) AddLabels(num string, labels []string) error {
	if row, ok := routedRow(); ok {
		return e.addLabelsViaConnector(row, num, labels)
	}
	args := []string{"issue", "edit", num, "--repo", e.repo}
	for _, l := range labels {
		args = append(args, "--add-label", l)
	}
	cmd := exec.Command("gh", args...)
	if _, err := cmd.Output(); err != nil {
		return ghCommandErr(fmt.Sprintf("gh issue edit %s", num), err)
	}
	return nil
}

var _ forge.IssueLabeler = (*execClient)(nil)

// issueLabels skips the title/body/state fields Issue fetches; CompleteVerdict's
// InProgress precondition check needs nothing but the labels. Under a routed
// row the Connector's Issue read answers it.
func (e *execClient) issueLabels(num string) ([]string, error) {
	if row, ok := routedRow(); ok {
		iss, err := e.issueViaConnector(row, num)
		return iss.Labels, err
	}
	cmd := exec.Command("gh", "issue", "view", num,
		"--repo", e.repo,
		"--json", "labels",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, ghCommandErr(fmt.Sprintf("gh issue view %s", num), err)
	}
	var raw struct {
		Labels []ghLabel `json:"labels"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse issue %s: %w", num, err)
	}
	return labelNames(raw.Labels), nil
}

// CompleteVerdict swaps the InProgress label on issue num for verdict's
// terminal label, resolved from verdictLabels rather than DispatchLabels.
// It first asserts num carries InProgress, so a double-dispatched issue errors
// instead of silently keeping InProgress alongside the verdict label. The check
// is not atomic: another process can flip the label between the read and edit.
func (e *execClient) CompleteVerdict(num string, verdict forge.Verdict) error {
	if row, ok := routedRow(); ok {
		return e.completeVerdictViaConnector(row, num, verdict)
	}
	add := e.verdictLabels.Label(verdict)
	if add == "" {
		return fmt.Errorf("gh issue edit %s: no label configured for verdict %v", num, verdict)
	}

	remove := e.labels.Label(forge.InProgress)
	if remove != "" {
		labels, err := e.issueLabels(num)
		if err != nil {
			return err
		}
		if !slices.Contains(labels, remove) {
			return fmt.Errorf("gh issue edit %s: expected %q label, issue has [%s]", num, remove, strings.Join(labels, ", "))
		}
	}

	args := []string{"issue", "edit", num, "--repo", e.repo, "--add-label", add}
	if remove != "" {
		args = append(args, "--remove-label", remove)
	}
	cmd := exec.Command("gh", args...)
	if _, err := cmd.Output(); err != nil {
		return ghCommandErr(fmt.Sprintf("gh issue edit %s", num), err)
	}
	return nil
}

var _ forge.NativeDepsReader = (*execClient)(nil)

// NativeDepsOf reads issue num's blocked_by relationships from GitHub's
// native issue-dependencies API.
func (e *execClient) NativeDepsOf(num string) ([]string, error) {
	row, err := readRow()
	if err != nil {
		return nil, err
	}
	return e.nativeDepsViaConnector(row, num)
}

// BlocksOf returns the issues num blocks, read from GitHub's native
// issue-dependencies API. No prose grammar declares a forward "blocks"
// relationship, so unlike DepsOf there is no body-text fallback and a lookup
// failure is returned directly (issue #1744).
func (e *execClient) BlocksOf(num string) ([]forge.Dependency, error) {
	row, err := readRow()
	if err != nil {
		return nil, err
	}
	return e.blocksOfViaConnector(row, num)
}

// PriorClaimState reads issue num's timeline for the most recent "unlabeled"
// event naming the Complete or Failed label (issue #2477). A claim's
// TransitionState(_, InProgress) strips that label before recoverByNumber runs,
// so the timeline is the only route back to it. Events arrive oldest first,
// across --paginate pages too, so the last match scanned is the most recent.
func (e *execClient) PriorClaimState(num string) (forge.DispatchState, bool, error) {
	row, err := readRow()
	if err != nil {
		return forge.Untriaged, false, err
	}
	return e.priorClaimStateViaConnector(row, num)
}

// CloseMergedIssue closes issue num as a backstop for a merged agent PR whose
// Closes #<N> keyword GitHub's auto-close missed (issue #1892), and as the
// close for a status=already-resolved outcome (issue #4015), which must never
// read as "not planned". It checks state first so the common already-closed
// case is a true no-op instead of leaning on gh's exit code for a redundant
// close.
func (e *execClient) CloseMergedIssue(num string) error {
	if row, ok := routedRow(); ok {
		return e.closeMergedIssueViaConnector(row, num)
	}
	iss, err := e.nativeIssue(num)
	if err != nil {
		return err
	}
	if iss.State == forge.IssueClosed {
		return nil
	}
	cmd := exec.Command("gh", "issue", "close", num, "--repo", e.repo, "--reason", "completed")
	if _, err := cmd.Output(); err != nil {
		return ghCommandErr(fmt.Sprintf("gh issue close %s", num), err)
	}
	return nil
}

func (e *execClient) Comment(num, body string) error {
	if row, ok := routedRow(); ok {
		return e.commentViaConnector(row, num, body)
	}
	cmd := exec.Command("gh", "issue", "comment", num,
		"--repo", e.repo,
		"--body", body,
	)
	if _, err := cmd.Output(); err != nil {
		return ghCommandErr(fmt.Sprintf("gh issue comment %s", num), err)
	}
	return nil
}

// PostIssue files an issue against this adapter's own repo, never a
// caller-supplied one, per the do-not-trust-the-agent-target invariant (issues
// #2028, #1949). It returns the created issue's URL: the Connector's html_url
// under a routed row, else gh's stdout.
func (e *execClient) PostIssue(title, body string, labels []string) (string, error) {
	if row, ok := routedRow(); ok {
		return e.fileIssueViaConnector(row, title, body, labels)
	}
	args := []string{"issue", "create",
		"--repo", e.repo,
		"--title", title,
		"--body", body,
	}
	for _, label := range labels {
		args = append(args, "--label", label)
	}
	cmd := exec.Command("gh", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", ghCommandErr("gh issue create", err)
	}
	return strings.TrimSpace(string(out)), nil
}

var _ forge.HostPostedCommenter = (*execClient)(nil)
var _ forge.HostPostedIssueFiler = (*execClient)(nil)

// SearchIssues implements forge.IssueSearcher (issue #6895). text comes from an
// untrusted Box; the Connector quotes each term and rejects text that could
// break out of the quoting.
func (e *execClient) SearchIssues(text string, state forge.IssueState, label string, limit int) ([]forge.Issue, error) {
	row, err := readRow()
	if err != nil {
		return nil, err
	}
	return e.searchIssuesViaConnector(row, text, state, label, limit)
}
