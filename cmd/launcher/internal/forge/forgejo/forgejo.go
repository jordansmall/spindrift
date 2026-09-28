// Package forgejo is the Forgejo REST adapter. It satisfies all three forge
// seams (ADR 0038): IssueTracker on forgejoClient, and CodeForge plus the full
// PRForge optional interface on forgejoCodeForge.
package forgejo

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/rest"
)

// ForgejoConfig configures the Forgejo IssueTracker adapter.
type ForgejoConfig struct {
	BaseURL string // Forgejo instance base URL, e.g. https://codeberg.org
	Repo    string // owner/repo slug
	Token   string

	// Labels drive TransitionState. Forgejo has no native workflow-status
	// concept to prefer over labels.
	Labels forge.DispatchLabels
	// VerdictLabels configures CompleteVerdict, the research dispatch kind's
	// Complete transition.
	VerdictLabels forge.VerdictLabels

	// HTTPClient overrides the client used for Forgejo REST calls. A nil
	// client gets defaultForgejoHTTPTimeout, never the untimed
	// http.DefaultClient, because this default also backs the CodeForge
	// adapter's Probe and Merge calls when the two seams share one
	// *rest.Client (issue #2256).
	HTTPClient *http.Client
}

// ValidateForgejoEnv checks the FORGEJO_* knobs required when
// ISSUE_TRACKER=forgejo and returns an error for the first unmet requirement.
func ValidateForgejoEnv(baseURL, token string) error {
	if baseURL == "" {
		return fmt.Errorf("set FORGEJO_BASE_URL (Forgejo instance base URL) when ISSUE_TRACKER=forgejo")
	}
	if token == "" {
		return fmt.Errorf("set FORGEJO_TOKEN when ISSUE_TRACKER=forgejo")
	}
	return nil
}

const defaultForgejoBaseURL = "https://codeberg.org"

// forgejoClient satisfies IssueTracker only.
type forgejoClient struct {
	cfg  ForgejoConfig
	rest *rest.Client

	// orgLabelsAuthWarnOnce gates ListLabels' missing-scope warning to once
	// per client: ListLabels runs on every poll, and a token lacking
	// read:organization fails the org lookup identically every time.
	orgLabelsAuthWarnOnce sync.Once
}

// NewForgejoClient returns an IssueTracker backed by the Forgejo REST API.
func NewForgejoClient(cfg ForgejoConfig) forge.IssueTracker {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultForgejoBaseURL
	}
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultForgejoHTTPTimeout}
	}
	// The status map's 405/409 to errMergeRefused entries matter only on the
	// merge endpoint, which this IssueTracker never calls. They are carried
	// here so the disambiguation still works when NewForgejoCodeForge reuses
	// this tracker's *rest.Client (issue #2256).
	restClient := rest.New(cfg.BaseURL, rest.TokenAuth{Scheme: "token", Token: cfg.Token}, "forgejo", forgejoStatusMap(), hc)
	return &forgejoClient{cfg: cfg, rest: restClient}
}

func (c *forgejoClient) repoPath() string {
	return "/api/v1/repos/" + c.cfg.Repo
}

// orgLabelsPath is the labels endpoint of cfg.Repo's owner. ForgejoConfig
// has no org field, so the owner is the slug segment before the "/"; a
// slash-less Repo yields the whole slug as owner, so its lookup resolves
// that name as an org (a 404 there is absorbed as "no org labels").
func (c *forgejoClient) orgLabelsPath() string {
	owner, _, _ := strings.Cut(c.cfg.Repo, "/")
	return "/api/v1/orgs/" + owner + "/labels"
}

type forgejoLabel struct {
	Name string `json:"name"`
}

type forgejoIssuePayload struct {
	Number  int            `json:"number"`
	Title   string         `json:"title"`
	Body    string         `json:"body"`
	State   string         `json:"state"`
	Labels  []forgejoLabel `json:"labels"`
	HTMLURL string         `json:"html_url"`
}

func issueState(state string) forge.IssueState {
	if state == "closed" {
		return forge.IssueClosed
	}
	return forge.IssueOpen
}

func labelNames(labels []forgejoLabel) []string {
	names := make([]string, len(labels))
	for i, l := range labels {
		names[i] = l.Name
	}
	return names
}

func toForgeIssue(p forgejoIssuePayload) forge.Issue {
	names := labelNames(p.Labels)
	return forge.Issue{
		Number:   strconv.Itoa(p.Number),
		Title:    p.Title,
		Body:     p.Body,
		State:    issueState(p.State),
		Labels:   names,
		Priority: forge.ResolvePriority(names),
	}
}

// Issue returns the Forgejo issue's title, body, state, and labels.
func (c *forgejoClient) Issue(num string) (forge.Issue, error) {
	var payload forgejoIssuePayload
	if err := c.rest.Do(http.MethodGet, c.repoPath()+"/issues/"+num, nil, &payload); err != nil {
		return forge.Issue{}, err
	}
	return toForgeIssue(payload), nil
}

// ListIssues returns open issues carrying state's configured label, ascending
// by issue number. A state with no configured label applies no label filter.
// A configured label the repo doesn't define matches nothing and sends no
// query (issue #3952), for the same reason ListIssuesWithLabels skips one.
func (c *forgejoClient) ListIssues(state forge.DispatchState) ([]forge.Issue, error) {
	label := c.cfg.Labels.Label(state)
	if label != "" && len(c.definedLabels([]string{label})) == 0 {
		return nil, nil
	}
	return c.listIssues("open", label)
}

// ListOpenIssues returns every open issue, ascending by issue number. It
// applies no label filter, so untriaged issues are included.
func (c *forgejoClient) ListOpenIssues() ([]forge.Issue, error) {
	return c.listIssues("open", "")
}

// listIssues walks every page of the issue listing in restState, "open" or
// "closed" (issue #2265, #3978), via walkPages. It sorts the merged pages by
// numeric issue number because Forgejo guarantees no order of its own, and
// merging pages preserves none either.
func (c *forgejoClient) listIssues(restState, label string) ([]forge.Issue, error) {
	q := url.Values{
		"state": {restState},
		"type":  {"issues"},
	}
	if label != "" {
		q.Set("labels", label)
	}
	payload, err := walkPages(c.rest, c.repoPath()+"/issues", q, func(p forgejoIssuePayload) int { return p.Number })
	if err != nil {
		return nil, err
	}
	var issues []forge.Issue
	for _, p := range payload {
		iss := toForgeIssue(p)
		// Lossless backstop for label != "": Forgejo silently drops an
		// unresolved labels= filter and returns everything (issue #3952),
		// so re-check client-side (definedLabels documents the exact,
		// case-sensitive match semantics).
		if label != "" && !slices.Contains(iss.Labels, label) {
			continue
		}
		issues = append(issues, iss)
	}
	sort.Slice(issues, func(i, j int) bool {
		ni, _ := strconv.Atoi(issues[i].Number)
		nj, _ := strconv.Atoi(issues[j].Number)
		return ni < nj
	})
	return issues, nil
}

// issueListSource tags ListIssues and ListIssuesWithLabels' errors and
// warnings.
const issueListSource = "forgejo: issue list"

// definedLabels filters labels down to the ones ListLabels reports as defined
// on the repo or its owning org. The match is exact (case-sensitive) even
// though Forgejo's DB name lookup can be case-insensitive on MySQL, so a
// configured label differing only by case is treated as undefined. A
// ListLabels error is a transient outage, not evidence the labels are
// absent, so it falls back to treating every requested label as defined
// rather than returning none.
func (c *forgejoClient) definedLabels(labels []string) []string {
	defined, err := c.ListLabels()
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: %s: ListLabels failed, querying all %d requested label(s) without the existence pre-check: %v\n", issueListSource, len(labels), err)
		return labels
	}
	var present []string
	for _, l := range labels {
		if slices.Contains(defined, l) {
			present = append(present, l)
		}
	}
	return present
}

// ListIssuesWithLabels implements forge.LabeledBacklogLister (issue #3873):
// state scopes the scan to open or closed issues, since a closed finding is a
// durable triage decision the host must not refile. Forgejo's "labels" query
// param is comma-separated, but its match semantics are not reliably "any
// of" across Forgejo versions, so this queries once per label via listIssues
// (already fully paginated, so the merge is never truncated); the merge,
// dedup-by-number, and partial-failure handling live in
// forge.MergeLabeledIssues. A label the repo doesn't define is skipped before
// querying (issue #3944): Forgejo's ListIssues handler resolves "labels=" names
// to IDs and drops the filter entirely when none resolve, so querying an
// absent label would scan every issue in state instead of finding nothing.
func (c *forgejoClient) ListIssuesWithLabels(state forge.IssueState, labels []string) ([]forge.Issue, error) {
	var restState string
	switch state {
	case forge.IssueOpen, forge.IssueClosed:
		restState = strings.ToLower(string(state))
	default:
		return nil, fmt.Errorf("forgejo: unsupported issue state %q", state)
	}
	if len(labels) == 0 {
		// Only spares a /labels round-trip: MergeLabeledIssues also returns
		// nil, nil for empty input.
		return nil, nil
	}

	present := c.definedLabels(labels)

	return forge.MergeLabeledIssues(issueListSource, present, func(label string) ([]forge.Issue, error) {
		return c.listIssues(restState, label)
	})
}

// setLabels replaces the full label set on issue num. Forgejo's
// replace-all-labels endpoint takes names, so there is no label-ID bookkeeping.
func (c *forgejoClient) setLabels(num string, names []string) error {
	if names == nil {
		names = []string{}
	}
	return c.rest.Do(http.MethodPut, c.repoPath()+"/issues/"+num+"/labels",
		map[string]any{"labels": names}, nil)
}

// TransitionState replaces num's label set, dropping the from label and adding
// the to label. A claim to InProgress also drops any stale Complete or Failed
// terminal label.
//
// A claim (to == InProgress) errors on forge.ErrAlreadyClaimed without
// touching labels when num already carries InProgress (#3887). The check is
// read-then-write, not atomic, so another claimer can still land between the
// two.
func (c *forgejoClient) TransitionState(num string, from, to forge.DispatchState) error {
	iss, err := c.Issue(num)
	if err != nil {
		return err
	}
	if c.cfg.Labels.AlreadyClaimed(from, to, iss.Labels) {
		return fmt.Errorf("forgejo: issue %s: %w (%q)", num, forge.ErrAlreadyClaimed, c.cfg.Labels.Label(to))
	}
	remove := c.cfg.Labels.ClaimRemoveLabels(from, to)
	newLabels := make([]string, 0, len(iss.Labels))
	for _, l := range iss.Labels {
		if !slices.Contains(remove, l) {
			newLabels = append(newLabels, l)
		}
	}
	if add := c.cfg.Labels.Label(to); add != "" && !slices.Contains(newLabels, add) {
		newLabels = append(newLabels, add)
	}
	return c.setLabels(num, newLabels)
}

// CompleteVerdict swaps num's InProgress label for verdict's terminal label,
// the research dispatch kind's Complete transition (ADR 0022). It errors
// without touching labels when num lacks the InProgress label, the #701
// double-dispatch guard. The check is not atomic, so a TOCTOU window remains.
func (c *forgejoClient) CompleteVerdict(num string, verdict forge.Verdict) error {
	add := c.cfg.VerdictLabels.Label(verdict)
	if add == "" {
		return fmt.Errorf("forgejo: no label configured for verdict %v", verdict)
	}

	iss, err := c.Issue(num)
	if err != nil {
		return err
	}

	inProgress := c.cfg.Labels.Label(forge.InProgress)
	if inProgress != "" && !slices.Contains(iss.Labels, inProgress) {
		return fmt.Errorf("forgejo: issue %s: expected %q label, issue has [%s]", num, inProgress, strings.Join(iss.Labels, ", "))
	}

	newLabels := make([]string, 0, len(iss.Labels))
	for _, l := range iss.Labels {
		if l != inProgress {
			newLabels = append(newLabels, l)
		}
	}
	if !slices.Contains(newLabels, add) {
		newLabels = append(newLabels, add)
	}
	return c.setLabels(num, newLabels)
}

type forgejoDependencyPayload struct {
	Number int `json:"number"`
}

// dependencyIDs deduplicates the payload's issue numbers, keeping API order.
func dependencyIDs(payload []forgejoDependencyPayload) []string {
	var ids []string
	seen := map[string]bool{}
	for _, p := range payload {
		id := strconv.Itoa(p.Number)
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

func (c *forgejoClient) nativeDepsOf(num string) ([]string, error) {
	var payload []forgejoDependencyPayload
	if err := c.rest.Do(http.MethodGet, c.repoPath()+"/issues/"+num+"/dependencies", nil, &payload); err != nil {
		return nil, err
	}
	return dependencyIDs(payload), nil
}

// DepsOf returns issue num's dependencies, preferring Forgejo's native
// dependencies API and falling back to body-text parsing when that lookup
// errors or finds nothing.
func (c *forgejoClient) DepsOf(num string) ([]forge.Dependency, error) {
	deps, err := c.nativeDepsOf(num)
	if err == nil && len(deps) > 0 {
		return forge.WithSource(deps, forge.DepSourceNative), nil
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: native dependency lookup for issue %s failed (%v); falling back to body parsing\n", num, err)
	}
	iss, err := c.Issue(num)
	if err != nil {
		return nil, err
	}
	return forge.WithSource(forge.ParseBlockerRefs(iss.Body), forge.DepSourceBody), nil
}

// BlocksOf returns the issues num blocks, from Forgejo's native blocks
// endpoint. It has no body-text fallback, unlike DepsOf, because no prose
// grammar declares a forward blocks relationship, so it returns a lookup
// failure directly.
func (c *forgejoClient) BlocksOf(num string) ([]forge.Dependency, error) {
	var payload []forgejoDependencyPayload
	if err := c.rest.Do(http.MethodGet, c.repoPath()+"/issues/"+num+"/blocks", nil, &payload); err != nil {
		return nil, err
	}
	return forge.WithSource(dependencyIDs(payload), forge.DepSourceNative), nil
}

// TouchesOf returns the touch-set parsed from issue num's body. Forgejo has no
// native touch-set concept to prefer over the shared body grammar.
func (c *forgejoClient) TouchesOf(num string) ([]string, error) {
	iss, err := c.Issue(num)
	if err != nil {
		return nil, err
	}
	return forge.ParseTouchPaths(iss.Body), nil
}

// CloseMergedIssue implements forge.MergeCloser (issue #2259), a backstop for
// a merged agent PR whose Closes #<N> keyword Forgejo's auto-close missed. It
// reads state before PATCHing so the common already-closed case is a real
// no-op instead of a redundant PATCH.
func (c *forgejoClient) CloseMergedIssue(num string) error {
	iss, err := c.Issue(num)
	if err != nil {
		return err
	}
	if iss.State == forge.IssueClosed {
		return nil
	}
	return c.rest.Do(http.MethodPatch, c.repoPath()+"/issues/"+num,
		map[string]string{"state": "closed"}, nil)
}

// Comment posts a comment on the Forgejo issue.
func (c *forgejoClient) Comment(num, body string) error {
	return c.rest.Do(http.MethodPost, c.repoPath()+"/issues/"+num+"/comments",
		map[string]string{"body": body}, nil)
}

type forgejoCommentPayload struct {
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt string `json:"created_at"`
	Body      string `json:"body"`
}

// Comments implements forge.CommentLister, returning num's comments
// oldest-first in Forgejo's own creation order, which is what forge.IssueText
// assumes when it windows to the last 10. The endpoint is unpaginated — it
// ignores page/limit and returns every comment on every request (issue
// #3978) — so Comments makes a single request rather than walking pages.
func (c *forgejoClient) Comments(num string) ([]forge.Comment, error) {
	var payload []forgejoCommentPayload
	if err := c.rest.Do(http.MethodGet, c.repoPath()+"/issues/"+num+"/comments", nil, &payload); err != nil {
		return nil, err
	}
	var comments []forge.Comment
	for _, p := range payload {
		comments = append(comments, forge.Comment{
			Author:    p.User.Login,
			CreatedAt: p.CreatedAt,
			Body:      p.Body,
		})
	}
	return comments, nil
}

var _ forge.CommentLister = (*forgejoClient)(nil)

// PostIssue implements forge.HostPostedIssueFiler (issue #1964), filing an
// issue against this adapter's repo and returning its html_url. The
// issue-creation endpoint wants label IDs, so labels go in a second call to
// setLabels, which takes names.
func (c *forgejoClient) PostIssue(title, body string, labels []string) (string, error) {
	var payload forgejoIssuePayload
	if err := c.rest.Do(http.MethodPost, c.repoPath()+"/issues",
		map[string]any{"title": title, "body": body}, &payload); err != nil {
		return "", err
	}
	if len(labels) > 0 {
		if err := c.setLabels(strconv.Itoa(payload.Number), labels); err != nil {
			return "", err
		}
	}
	return payload.HTMLURL, nil
}

var _ forge.HostPostedCommenter = (*forgejoClient)(nil)
var _ forge.HostPostedIssueFiler = (*forgejoClient)(nil)

// StateLabels implements forge.LabeledTracker.
func (c *forgejoClient) StateLabels() forge.DispatchLabels {
	return c.cfg.Labels
}

// WalksAllPages implements forge.FullyPaginated. listIssues walks every page
// until one comes back empty (#2265, #3978), so results are never truncated
// at the server's own page cap or at forge.ResultPageLimit, and a caller's
// page-limit fail-safe can treat a full-looking result as complete.
func (c *forgejoClient) WalksAllPages() bool {
	return true
}

// walkPages fetches every page of a Forgejo list endpoint at path, merging
// pages until one comes back empty: Forgejo caps the page size at [api]
// MAX_RESPONSE_ITEMS (default 50) whatever limit is requested (issue #2265,
// #3953, #3978), so a page shorter than forge.ResultPageLimit is never proof
// it's the last one. A page whose first item's key repeats the previous
// page's first key also ends the walk: rest.Client.Paginate has no page
// bound of its own, so a server or proxy that ignores ?page and re-serves
// the same page would otherwise loop forever. q is the caller's base query;
// walkPages clones it before adding "limit" and "page" so the caller's
// url.Values is never mutated; a nil q is treated as empty.
func walkPages[T any, K comparable](rc *rest.Client, path string, q url.Values, key func(T) K) ([]T, error) {
	q = maps.Clone(q)
	if q == nil {
		q = url.Values{}
	}
	var items []T
	var prevFirst K
	q.Set("limit", strconv.Itoa(forge.ResultPageLimit))
	err := rc.Paginate(func(page int) (bool, error) {
		q.Set("page", strconv.Itoa(page))
		var payload []T
		if err := rc.Do(http.MethodGet, path+"?"+q.Encode(), nil, &payload); err != nil {
			return false, err
		}
		if len(payload) == 0 {
			return true, nil
		}
		first := key(payload[0])
		if page > 1 && first == prevFirst {
			return true, nil
		}
		prevFirst = first
		items = append(items, payload...)
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// ListLabels returns the names of every label defined on the repo or its
// owning org, walking every page of both endpoints via walkPages (issue
// #2265, #3953, #3997), repo names first, then any org-only names, with
// duplicates dropped. Forgejo's issue list and label-replace endpoints
// resolve both label sets, so a pre-check that only saw repo labels would
// treat an org-only label as undefined.
func (c *forgejoClient) ListLabels() ([]string, error) {
	repoPayload, err := walkPages(c.rest, c.repoPath()+"/labels", url.Values{}, func(l forgejoLabel) string { return l.Name })
	if err != nil {
		return nil, err
	}
	names := labelNames(repoPayload)

	orgNames, err := c.orgLabels()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		seen[n] = true
	}
	for _, n := range orgNames {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	return names, nil
}

// orgLabels returns the owning org's label names. forge.ErrNotFound means a
// user-owned repo, so there are none. forge.ErrAuthFailure means a token
// without read:organization; the repo fetch already succeeded with it, and
// failing ListLabels would newly fail doctor (any ListLabels error is
// ErrConnectivity there) for deployments that work today, so it degrades to
// repo labels only with a warning. The cost: for that token an org-only
// label still reads as undefined, so ListIssues on it returns nothing (the
// #3997 gap, now warned once per client). Any other error propagates.
func (c *forgejoClient) orgLabels() ([]string, error) {
	payload, err := walkPages(c.rest, c.orgLabelsPath(), url.Values{}, func(l forgejoLabel) string { return l.Name })
	if err != nil {
		if errors.Is(err, forge.ErrNotFound) {
			return nil, nil
		}
		if errors.Is(err, forge.ErrAuthFailure) {
			c.orgLabelsAuthWarnOnce.Do(func() {
				fmt.Fprintf(os.Stderr, "WARNING: forgejo: org label lookup failed (%v); the token likely lacks the read:organization scope, so org-only labels are not seen by label pre-checks\n", err)
			})
			return nil, nil
		}
		return nil, err
	}
	return labelNames(payload), nil
}

// CreateLabel creates a repository label. The color argument is a bare hex
// value, and this call adds the leading # that Forgejo's endpoint requires.
func (c *forgejoClient) CreateLabel(name, description, color string) error {
	return c.rest.Do(http.MethodPost, c.repoPath()+"/labels",
		map[string]any{"name": name, "description": description, "color": "#" + color}, nil)
}

type forgejoRepoPayload struct {
	FullName string `json:"full_name"`
}

// Probe checks Forgejo connectivity and auth, returning the repository's
// owner/repo full name.
func (c *forgejoClient) Probe() (string, error) {
	var payload forgejoRepoPayload
	if err := c.rest.Do(http.MethodGet, c.repoPath(), nil, &payload); err != nil {
		if errors.Is(err, forge.ErrAuthFailure) {
			return "", err
		}
		return "", fmt.Errorf("%w: %w", forge.ErrRepoNotFound, err)
	}
	return payload.FullName, nil
}
