// Package forgejo is the Forgejo REST adapter. It satisfies all three of the
// parent forge package's seams (ADR 0038): the IssueTracker interface
// (forgejoClient), the CodeForge interface, and the full PRForge optional
// interface (both on forgejoCodeForge) — the second full-parity backend
// beside github, so the whole dispatch loop (claim, work, PR, CI watch,
// merge) runs against a Codeberg or self-hosted Forgejo instance exactly as
// it does on GitHub.
package forgejo

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/rest"
)

// ForgejoConfig configures the Forgejo IssueTracker adapter.
type ForgejoConfig struct {
	BaseURL string // Forgejo instance base URL, e.g. https://codeberg.org
	Repo    string // owner/repo slug
	Token   string

	// Labels are the labels TransitionState swaps to move an issue through
	// the dispatch lifecycle — Forgejo has no native workflow-status concept
	// to prefer over labels, unlike jira's StatusMapping.
	Labels forge.DispatchLabels
	// VerdictLabels configures CompleteVerdict (the research dispatch kind's
	// Complete transition).
	VerdictLabels forge.VerdictLabels

	// HTTPClient overrides the HTTP client used for Forgejo REST calls; nil
	// uses a client with a default 30s timeout (defaultForgejoHTTPTimeout) —
	// never the untimed http.DefaultClient, since this default also backs
	// the CodeForge adapter's Probe/Merge calls when the two seams share one
	// *rest.Client (issue #2256's shared-client seam; see
	// defaultForgejoHTTPTimeout's doc comment). Tests inject a client
	// pointed at a fake server.
	HTTPClient *http.Client
}

// ValidateForgejoEnv checks the FORGEJO_* config knobs required when
// ISSUE_TRACKER=forgejo, guarding the same fields ForgejoConfig carries.
// Returns a descriptive error for the first unmet requirement.
func ValidateForgejoEnv(baseURL, token string) error {
	if baseURL == "" {
		return fmt.Errorf("set FORGEJO_BASE_URL (Forgejo instance base URL) when ISSUE_TRACKER=forgejo")
	}
	if token == "" {
		return fmt.Errorf("set FORGEJO_TOKEN when ISSUE_TRACKER=forgejo")
	}
	return nil
}

// defaultForgejoBaseURL is used when ForgejoConfig.BaseURL is empty.
const defaultForgejoBaseURL = "https://codeberg.org"

// forgejoClient is the Forgejo REST adapter. It satisfies IssueTracker only.
type forgejoClient struct {
	cfg  ForgejoConfig
	rest *rest.Client
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
	// The 405/409 -> errMergeRefused entries only ever matter on the merge
	// endpoint (forgejo_codeforge.go's postMerge), which this IssueTracker
	// never calls -- they're carried here too so that when
	// NewForgejoCodeForge reuses this tracker's *rest.Client (issue #2256's
	// shared-client seam), the merge endpoint's disambiguation still works
	// against the reused instance.
	statuses := rest.StatusMap{
		http.StatusUnauthorized:     forge.ErrAuthFailure,
		http.StatusForbidden:        forge.ErrAuthFailure,
		http.StatusNotFound:         forge.ErrNotFound,
		http.StatusMethodNotAllowed: errMergeRefused,
		http.StatusConflict:         errMergeRefused,
	}
	restClient := rest.New(cfg.BaseURL, rest.TokenAuth{Scheme: "token", Token: cfg.Token}, "forgejo", statuses, hc)
	return &forgejoClient{cfg: cfg, rest: restClient}
}

// repoPath returns the API base path for the configured repo,
// /api/v1/repos/{owner}/{repo}.
func (c *forgejoClient) repoPath() string {
	return "/api/v1/repos/" + c.cfg.Repo
}

// forgejoLabel is the label shape Forgejo's REST API emits.
type forgejoLabel struct {
	Name string `json:"name"`
}

// forgejoIssuePayload is the subset of the Forgejo issue REST representation
// this adapter reads.
type forgejoIssuePayload struct {
	Number  int            `json:"number"`
	Title   string         `json:"title"`
	Body    string         `json:"body"`
	State   string         `json:"state"`
	Labels  []forgejoLabel `json:"labels"`
	HTMLURL string         `json:"html_url"`
}

// issueState maps Forgejo's "open"/"closed" state string to the canonical
// forge.IssueState.
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

// toForgeIssue converts a forgejoIssuePayload into the launcher's canonical
// forge.Issue shape.
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

// ListIssues returns open issues in dispatch state state, in canonical order
// (ascending issue number). Issues are matched by the label configured for
// state; when state has no configured label, no label filter is applied.
func (c *forgejoClient) ListIssues(state forge.DispatchState) ([]forge.Issue, error) {
	label := c.cfg.Labels.Label(state)
	return c.listIssues(label)
}

// ListOpenIssues returns every open issue, in canonical order (ascending
// issue number), regardless of dispatch state — unlike ListIssues, which
// scopes to one dispatch state's label, this carries no label filter, so
// untriaged issues (no dispatch label yet) are included too.
func (c *forgejoClient) ListOpenIssues() ([]forge.Issue, error) {
	return c.listIssues("")
}

// listIssues is the shared implementation behind ListIssues and
// ListOpenIssues: GET the open-issue page, optionally scoped by label, sort
// ascending by numeric issue number (Forgejo's own sort order is not
// guaranteed to be number-ascending), and warn if the page may have
// truncated the backlog.
func (c *forgejoClient) listIssues(label string) ([]forge.Issue, error) {
	q := url.Values{
		"state": {"open"},
		"type":  {"issues"},
		"limit": {strconv.Itoa(forge.ResultPageLimit)},
	}
	if label != "" {
		q.Set("labels", label)
	}
	var payload []forgejoIssuePayload
	if err := c.rest.Do(http.MethodGet, c.repoPath()+"/issues?"+q.Encode(), nil, &payload); err != nil {
		return nil, err
	}
	issues := make([]forge.Issue, len(payload))
	for i, p := range payload {
		issues[i] = toForgeIssue(p)
	}
	sort.Slice(issues, func(i, j int) bool {
		ni, _ := strconv.Atoi(issues[i].Number)
		nj, _ := strconv.Atoi(issues[j].Number)
		return ni < nj
	})
	forge.WarnPageMayTruncateBacklog("forgejo issue list", len(issues))
	return issues, nil
}

// setLabels replaces the full label set on issue num with names — Forgejo's
// replace-all-labels endpoint accepts label names directly, avoiding
// label-ID bookkeeping.
func (c *forgejoClient) setLabels(num string, names []string) error {
	if names == nil {
		names = []string{}
	}
	return c.rest.Do(http.MethodPut, c.repoPath()+"/issues/"+num+"/labels",
		map[string]any{"labels": names}, nil)
}

// TransitionState moves issue num from state from to state to by replacing
// its label set: the from label (and, on a claim to InProgress, any stale
// Complete/Failed terminal label) is removed and the to label is added.
func (c *forgejoClient) TransitionState(num string, from, to forge.DispatchState) error {
	iss, err := c.Issue(num)
	if err != nil {
		return err
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

// CompleteVerdict swaps num's InProgress label for verdict's terminal label
// — the research dispatch kind's Complete transition (ADR 0022).
//
// Before swapping, it asserts num currently carries the InProgress label —
// mirroring the github adapter's #701 double-dispatch guard — and errors
// without issuing the label update when it's absent. This is check-then-edit,
// not atomic compare-and-swap, the same narrowed-but-not-closed TOCTOU
// window jira's CompleteVerdict documents.
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

// forgejoDependencyPayload is the issue-summary shape Forgejo's dependencies
// and blocks endpoints emit.
type forgejoDependencyPayload struct {
	Number int `json:"number"`
}

// dependencyIDs converts a forgejoDependencyPayload slice into deduplicated
// issue-number strings, preserving API response order.
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

// nativeDepsOf queries Forgejo's issue-dependencies endpoint for the issues
// that block num.
func (c *forgejoClient) nativeDepsOf(num string) ([]string, error) {
	var payload []forgejoDependencyPayload
	if err := c.rest.Do(http.MethodGet, c.repoPath()+"/issues/"+num+"/dependencies", nil, &payload); err != nil {
		return nil, err
	}
	return dependencyIDs(payload), nil
}

// DepsOf returns the canonical dependencies for issue num, preferring
// Forgejo's native dependencies API and falling back to body-text parsing
// (inline refs / "## Blocked by" section) when the native lookup errors or
// yields no relationships.
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

// BlocksOf returns the canonical issues num blocks — DepsOf's reverse
// direction — read from Forgejo's native "blocks" endpoint. Unlike DepsOf
// there is no body-text fallback: no prose grammar declares a forward
// "blocks" relationship, so a native lookup failure has nothing to degrade
// to and is returned directly.
func (c *forgejoClient) BlocksOf(num string) ([]forge.Dependency, error) {
	var payload []forgejoDependencyPayload
	if err := c.rest.Do(http.MethodGet, c.repoPath()+"/issues/"+num+"/blocks", nil, &payload); err != nil {
		return nil, err
	}
	return forge.WithSource(dependencyIDs(payload), forge.DepSourceNative), nil
}

// TouchesOf returns the declared touch-set parsed from issue num's body —
// the shared body-grammar default (forge.ParseTouchPaths); Forgejo has no
// native touch-set concept to prefer over it.
func (c *forgejoClient) TouchesOf(num string) ([]string, error) {
	iss, err := c.Issue(num)
	if err != nil {
		return nil, err
	}
	return forge.ParseTouchPaths(iss.Body), nil
}

// CloseMergedIssue implements the optional forge.MergeCloser surface (issue
// #2259): a deterministic backstop for a merged agent PR whose body's
// Closes #<N> keyword Forgejo's own auto-close missed. Checks state before
// PATCHing so an already-closed issue (the common case — auto-close already
// ran) is a true no-op rather than relying on a redundant PATCH's status.
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

// PostIssue implements forge.HostPostedIssueFiler (issue #1964): it files a
// new issue against this adapter's own repo and returns the created issue's
// html_url. Forgejo's issue-creation endpoint wants label IDs rather than
// names, so labels are applied in a second call via setLabels (which accepts
// names), avoiding label-ID bookkeeping.
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

// StateLabels implements forge.LabeledTracker, returning the DispatchLabels
// c resolves DispatchState values through.
func (c *forgejoClient) StateLabels() forge.DispatchLabels {
	return c.cfg.Labels
}

// ListLabels returns the repository's defined label names.
func (c *forgejoClient) ListLabels() ([]string, error) {
	var payload []forgejoLabel
	if err := c.rest.Do(http.MethodGet, c.repoPath()+"/labels", nil, &payload); err != nil {
		return nil, err
	}
	return labelNames(payload), nil
}

// CreateLabel creates a repository label with the given name, description,
// and hex color (without the leading #) — Forgejo's label-creation endpoint
// wants a leading # on the hex color, unlike the color argument's own
// convention.
func (c *forgejoClient) CreateLabel(name, description, color string) error {
	return c.rest.Do(http.MethodPost, c.repoPath()+"/labels",
		map[string]any{"name": name, "description": description, "color": "#" + color}, nil)
}

// forgejoRepoPayload is the subset of the Forgejo repository REST
// representation Probe reads.
type forgejoRepoPayload struct {
	FullName string `json:"full_name"`
}

// Probe checks Forgejo connectivity/auth and returns the repository's full
// name (owner/repo).
func (c *forgejoClient) Probe() (string, error) {
	var payload forgejoRepoPayload
	if err := c.rest.Do(http.MethodGet, c.repoPath(), nil, &payload); err != nil {
		if errors.Is(err, forge.ErrAuthFailure) {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", forge.ErrRepoNotFound, err)
	}
	return payload.FullName, nil
}
