package github

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"sort"
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

// ghIssueRow is the raw shape shared by every "gh issue list --json ..." call
// in this file. Body is simply empty where the caller's --json fields don't
// request it.
type ghIssueRow struct {
	Number int       `json:"number"`
	Title  string    `json:"title"`
	Body   string    `json:"body"`
	Labels []ghLabel `json:"labels"`
}

// toIssue converts a raw gh row to a forge.Issue, resolving Priority from the
// row's own labels.
func (r ghIssueRow) toIssue() forge.Issue {
	names := labelNames(r.Labels)
	return forge.Issue{
		Number:   strconv.Itoa(r.Number),
		Title:    r.Title,
		Body:     r.Body,
		Labels:   names,
		Priority: forge.ResolvePriority(names),
	}
}

// byNumberAsc and byNumberDesc are the two number-comparator orders shared by
// ListIssues/ListOpenIssues (ascending) and ListOpenIssuesWithLabels
// (descending).
func byNumberAsc(issues []forge.Issue) func(i, j int) bool {
	return func(i, j int) bool {
		ni, _ := strconv.Atoi(issues[i].Number)
		nj, _ := strconv.Atoi(issues[j].Number)
		return ni < nj
	}
}

func byNumberDesc(issues []forge.Issue) func(i, j int) bool {
	return func(i, j int) bool {
		ni, _ := strconv.Atoi(issues[i].Number)
		nj, _ := strconv.Atoi(issues[j].Number)
		return ni > nj
	}
}

// decodeIssueRows is the shared tail of ListIssues and ListOpenIssues, which
// differ only in the --label filter they hand gh: decode, order and the
// page-limit warning have to stay identical between the two.
func decodeIssueRows(out []byte) ([]forge.Issue, error) {
	var raw []ghIssueRow
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse gh issue list: %w", err)
	}
	issues := make([]forge.Issue, len(raw))
	for i, r := range raw {
		issues[i] = r.toIssue()
	}
	sort.Slice(issues, byNumberAsc(issues))
	forge.WarnPageMayTruncateBacklog("gh issue list", len(issues))
	return issues, nil
}

func (e *execClient) ListIssues(state forge.DispatchState) ([]forge.Issue, error) {
	label := e.labels.Label(state)
	cmd := exec.Command("gh", "issue", "list",
		"--repo", e.repo,
		"--state", "open",
		"--label", label,
		"--limit", strconv.Itoa(forge.ResultPageLimit),
		"--search", "sort:created-asc",
		"--json", "number,title,labels",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, ghCommandErr("gh issue list", err)
	}
	return decodeIssueRows(out)
}

// ListOpenIssues returns every open issue in ascending number order. Unlike
// ListIssues it passes no --label filter, so issues with no dispatch label
// yet are included.
func (e *execClient) ListOpenIssues() ([]forge.Issue, error) {
	cmd := exec.Command("gh", "issue", "list",
		"--repo", e.repo,
		"--state", "open",
		"--limit", strconv.Itoa(forge.ResultPageLimit),
		"--search", "sort:created-asc",
		"--json", "number,title,labels",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, ghCommandErr("gh issue list", err)
	}
	return decodeIssueRows(out)
}

// ListOpenIssuesWithLabels implements forge.LabeledBacklogLister (issue #3609
// review). "gh issue list --label A --label B" ANDs the labels together, so
// finding every issue carrying *either* one takes one query per label,
// merged here and de-duplicated by number -- an issue carrying both labels
// would otherwise come back twice.
//
// A per-label failure degrades rather than aborting the whole scan: the
// target repo may simply lack one of the finding labels (spindrift doctor
// treats agent-research-finding as advisory, never required), or a single
// label's call can hit a transient 403/secondary rate limit. Either way,
// returning nil,err here would hand backlogDedupIndex an empty index and
// refile every open finding as a duplicate -- worse than the labels that did
// succeed still being honored. Only a failure on every label propagates, so
// a total outage still reaches backlogDedupIndex's own fallback.
func (e *execClient) ListOpenIssuesWithLabels(labels []string) ([]forge.Issue, error) {
	seen := make(map[string]bool)
	var issues []forge.Issue
	failures := 0
	var lastErr error
	for _, label := range labels {
		cmd := exec.Command("gh", "issue", "list",
			"--repo", e.repo,
			"--state", "open",
			"--label", label,
			"--limit", strconv.Itoa(forge.DedupScanLimit),
			"--search", "sort:created-desc",
			"--json", "number,title,body,labels",
		)
		out, err := cmd.Output()
		if err != nil {
			lastErr = ghCommandErr("gh issue list", err)
			fmt.Fprintf(os.Stderr, "WARNING: gh issue list --label %s failed: %v\n", label, lastErr)
			failures++
			continue
		}
		var raw []ghIssueRow
		if err := json.Unmarshal(out, &raw); err != nil {
			lastErr = fmt.Errorf("parse gh issue list: %w", err)
			fmt.Fprintf(os.Stderr, "WARNING: gh issue list --label %s failed: %v\n", label, lastErr)
			failures++
			continue
		}
		forge.WarnDedupScanMayTruncate("gh issue list", label, len(raw))
		for _, r := range raw {
			num := strconv.Itoa(r.Number)
			if seen[num] {
				continue
			}
			seen[num] = true
			issues = append(issues, r.toIssue())
		}
	}
	if len(labels) > 0 && failures == len(labels) {
		return nil, fmt.Errorf("gh issue list: all %d label(s) failed: %w", len(labels), lastErr)
	}
	// Each per-label page arrives newest-first, but a merge of two of them
	// does not, and the doc promises one order.
	sort.Slice(issues, byNumberDesc(issues))
	return issues, nil
}

func (e *execClient) Issue(num string) (forge.Issue, error) {
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

// ghComment is the comment shape gh issue view --json comments emits.
type ghComment struct {
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
	CreatedAt string `json:"createdAt"`
	Body      string `json:"body"`
}

// Comments returns issue num's comments oldest-first, the order gh emits them
// in and the order forge.IssueText assumes when it windows to the last 10.
func (e *execClient) Comments(num string) ([]forge.Comment, error) {
	cmd := exec.Command("gh", "issue", "view", num,
		"--repo", e.repo,
		"--json", "comments",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, ghCommandErr(fmt.Sprintf("gh issue view %s", num), err)
	}
	var raw struct {
		Comments []ghComment `json:"comments"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse issue %s: %w", num, err)
	}
	comments := make([]forge.Comment, len(raw.Comments))
	for i, c := range raw.Comments {
		comments[i] = forge.Comment{
			Author:    c.Author.Login,
			CreatedAt: c.CreatedAt,
			Body:      c.Body,
		}
	}
	return comments, nil
}

var _ forge.CommentLister = (*execClient)(nil)

// StateLabels returns the DispatchLabels e resolves DispatchState values through.
func (e *execClient) StateLabels() forge.DispatchLabels {
	return e.labels
}

// TransitionState swaps the from-state label for the to-state label on issue
// num. A claim (to == InProgress) also strips any stale terminal label
// (Complete, Failed) left by a prior run, matching the dispatch workflow's
// claim-remove-labels set (#1985), so a re-triggered or recovered issue cannot
// run while still labeled agent-failed or agent-complete.
func (e *execClient) TransitionState(num string, from, to forge.DispatchState) error {
	add := e.labels.Label(to)
	args := []string{"issue", "edit", num, "--repo", e.repo, "--add-label", add}
	for _, remove := range e.labels.ClaimRemoveLabels(from, to) {
		args = append(args, "--remove-label", remove)
	}
	cmd := exec.Command("gh", args...)
	if _, err := cmd.Output(); err != nil {
		return ghCommandErr(fmt.Sprintf("gh issue edit %s", num), err)
	}
	return nil
}

// issueLabels skips the title/body/state fields Issue fetches; CompleteVerdict's
// InProgress precondition check needs nothing but the labels.
func (e *execClient) issueLabels(num string) ([]string, error) {
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

// DepsOf returns issue num's dependencies, preferring GitHub's native
// issue-dependencies API and falling back to body-text parsing when that
// lookup errors or returns no relationships.
func (e *execClient) DepsOf(num string) ([]forge.Dependency, error) {
	deps, err := e.nativeDepsOf(num)
	if err == nil && len(deps) > 0 {
		return forge.WithSource(deps, forge.DepSourceNative), nil
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: native dependency lookup for issue %s failed (%v); falling back to body parsing\n", num, err)
	}
	iss, err := e.Issue(num)
	if err != nil {
		return nil, err
	}
	return forge.WithSource(forge.ParseBlockerRefs(iss.Body), forge.DepSourceBody), nil
}

func (e *execClient) nativeDepsOf(num string) ([]string, error) {
	return e.nativeDependencyIDs(num, "blocked_by")
}

// BlocksOf returns the issues num blocks, read from GitHub's native
// issue-dependencies API. No prose grammar declares a forward "blocks"
// relationship, so unlike DepsOf there is no body-text fallback and a lookup
// failure is returned directly (issue #1744).
func (e *execClient) BlocksOf(num string) ([]forge.Dependency, error) {
	ids, err := e.nativeDependencyIDs(num, "blocking")
	if err != nil {
		return nil, err
	}
	return forge.WithSource(ids, forge.DepSourceNative), nil
}

// nativeDependencyIDs reads num's relationships in the given direction,
// either "blocked_by" or "blocking", deduplicating in API response order.
func (e *execClient) nativeDependencyIDs(num, direction string) ([]string, error) {
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/issues/%s/dependencies/%s", e.repo, num, direction),
		"--jq", ".[].number",
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, ghCommandErr(fmt.Sprintf("gh api dependencies/%s %s", direction, num), err)
	}
	var deps []string
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !seen[line] {
			seen[line] = true
			deps = append(deps, line)
		}
	}
	return deps, nil
}

// PriorClaimState reads issue num's timeline for the most recent "unlabeled"
// event naming the Complete or Failed label (issue #2477). A claim's
// TransitionState(_, InProgress) strips that label before recoverByNumber runs,
// so the timeline is the only route back to it. Events arrive oldest first,
// across --paginate pages too, so the last match scanned is the most recent.
func (e *execClient) PriorClaimState(num string) (forge.DispatchState, bool, error) {
	cmd := exec.Command("gh", "api",
		fmt.Sprintf("repos/%s/issues/%s/timeline", e.repo, num),
		"--paginate",
		"--jq", `.[] | select(.event == "unlabeled") | .label.name`,
	)
	out, err := cmd.Output()
	if err != nil {
		return forge.Untriaged, false, ghCommandErr(fmt.Sprintf("gh api timeline %s", num), err)
	}
	var prior forge.DispatchState
	found := false
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		switch strings.TrimSpace(scanner.Text()) {
		case e.labels.Complete:
			prior, found = forge.Complete, true
		case e.labels.Failed:
			prior, found = forge.Failed, true
		}
	}
	return prior, found, nil
}

// TouchesOf parses the declared touch-set from issue num's body; this adapter
// has no native touch-set concept to prefer over the shared body grammar.
func (e *execClient) TouchesOf(num string) ([]string, error) {
	iss, err := e.Issue(num)
	if err != nil {
		return nil, err
	}
	return forge.ParseTouchPaths(iss.Body), nil
}

// CloseMergedIssue closes issue num as a backstop for a merged agent PR whose
// Closes #<N> keyword GitHub's auto-close missed (issue #1892). It checks state
// first so the common already-closed case is a true no-op instead of leaning on
// gh's exit code for a redundant close.
func (e *execClient) CloseMergedIssue(num string) error {
	iss, err := e.Issue(num)
	if err != nil {
		return err
	}
	if iss.State == forge.IssueClosed {
		return nil
	}
	cmd := exec.Command("gh", "issue", "close", num, "--repo", e.repo)
	if _, err := cmd.Output(); err != nil {
		return ghCommandErr(fmt.Sprintf("gh issue close %s", num), err)
	}
	return nil
}

func (e *execClient) Comment(num, body string) error {
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
// #2028, #1949). It returns the created issue's URL from gh's stdout.
func (e *execClient) PostIssue(title, body string, labels []string) (string, error) {
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
