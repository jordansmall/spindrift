// Package jira is the Jira REST adapter. It satisfies only the parent forge
// package's IssueTracker interface; code still lands via the github Code Forge
// (ADR 0013).
package jira

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/rest"
)

// JiraConfig configures the Jira IssueTracker adapter.
type JiraConfig struct {
	BaseURL    string // Jira site base URL, e.g. https://yourcompany.atlassian.net
	ProjectKey string
	Email      string // Jira Cloud: Basic auth and Cloud search APIs; empty selects Bearer-token auth and v2 /search (Server/Data Center PAT)
	Token      string

	// StatusMapping maps canonical DispatchState values to native Jira status
	// names. TransitionState falls back to Labels when a state is unmapped or
	// the mapped transition is unavailable on the issue's workflow.
	StatusMapping map[forge.DispatchState]string
	// Labels are the fallback labels applied when a transition is unmapped or
	// blocked by the project's workflow.
	Labels forge.DispatchLabels
	// VerdictLabels configures CompleteVerdict, applied via the same
	// label-fallback path as Labels; native status mapping for research
	// verdicts is deferred (ADR 0022).
	VerdictLabels forge.VerdictLabels

	// HTTPClient overrides the client used for Jira REST calls; nil uses
	// http.DefaultClient.
	HTTPClient *http.Client
}

var statusMappingKeys = map[string]forge.DispatchState{
	"dispatchable": forge.Dispatchable,
	"inProgress":   forge.InProgress,
	"complete":     forge.Complete,
	"failed":       forge.Failed,
}

// ParseStatusMapping parses the JIRA_STATUS_MAPPING knob, a JSON object mapping
// statusMappingKeys to native Jira status names. An unknown key is an error so a
// typo fails fast at startup instead of silently dropping the mapping.
func ParseStatusMapping(s string) (map[forge.DispatchState]string, error) {
	out := map[forge.DispatchState]string{}
	if s == "" {
		return out, nil
	}
	var raw map[string]string
	if err := json.Unmarshal([]byte(s), &raw); err != nil {
		return nil, fmt.Errorf("parse JIRA_STATUS_MAPPING: %w", err)
	}
	for key, status := range raw {
		state, ok := statusMappingKeys[key]
		if !ok {
			return nil, fmt.Errorf("JIRA_STATUS_MAPPING: unknown key %q (want one of dispatchable, inProgress, complete, failed)", key)
		}
		out[state] = status
	}
	return out, nil
}

// ValidateJiraEnv checks the JIRA_* knobs required when ISSUE_TRACKER=jira,
// returning an error for the first unmet requirement.
func ValidateJiraEnv(baseURL, projectKey, token, statusMapping string) error {
	if baseURL == "" {
		return fmt.Errorf("set JIRA_BASE_URL (Jira site base URL) when ISSUE_TRACKER=jira")
	}
	if projectKey == "" {
		return fmt.Errorf("set JIRA_PROJECT_KEY when ISSUE_TRACKER=jira")
	}
	if token == "" {
		return fmt.Errorf("set JIRA_TOKEN when ISSUE_TRACKER=jira")
	}
	if _, err := ParseStatusMapping(statusMapping); err != nil {
		return err
	}
	return nil
}

type jiraClient struct {
	cfg  JiraConfig
	rest *rest.Client
	// cloud is decided once from cfg.Email; Cloud dropped v2 /search (410), so
	// search calls branch on it rather than falling back on a failed call.
	cloud bool
}

// NewJiraClient returns an IssueTracker backed by the Jira REST API.
func NewJiraClient(cfg JiraConfig) forge.IssueTracker {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	restClient := rest.New(cfg.BaseURL, jiraAuthStrategy{email: cfg.Email, token: cfg.Token}, "jira", jiraStatusMap(), hc)
	return &jiraClient{cfg: cfg, rest: restClient, cloud: cfg.Email != ""}
}

// jiraAuthStrategy implements rest.AuthStrategy: HTTP Basic (base64
// "email:token") for Jira Cloud when email is set, Bearer token for
// Server/Data Center PATs when it is empty.
type jiraAuthStrategy struct {
	email string
	token string
}

func (a jiraAuthStrategy) Apply(req *http.Request) {
	if a.email != "" {
		raw := a.email + ":" + a.token
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(raw)))
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
}

// jiraStatusMap is the HTTP-status to sentinel-error table for this package's
// rest.Client. forge.ErrRepoNotFound stays Probe-specific, applied at Probe's
// own call site rather than here.
func jiraStatusMap() rest.StatusMap {
	return rest.StatusMap{
		http.StatusUnauthorized: forge.ErrAuthFailure,
		http.StatusForbidden:    forge.ErrAuthFailure,
		http.StatusNotFound:     forge.ErrNotFound,
	}
}

// jiraIssuePayload is the subset of Jira's issue representation this adapter reads.
type jiraIssuePayload struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string `json:"summary"`
		Description string `json:"description"`
		Status      struct {
			Name           string `json:"name"`
			StatusCategory struct {
				Key string `json:"key"`
			} `json:"statusCategory"`
		} `json:"status"`
		Labels     []string `json:"labels"`
		IssueLinks []struct {
			Type struct {
				Inward  string `json:"inward"`
				Outward string `json:"outward"`
			} `json:"type"`
			InwardIssue *struct {
				Key string `json:"key"`
			} `json:"inwardIssue"`
			OutwardIssue *struct {
				Key string `json:"key"`
			} `json:"outwardIssue"`
		} `json:"issuelinks"`
	} `json:"fields"`
}

// jiraBlockedByLink is the inward relationship text of Jira's built-in "Blocks" link type.
const jiraBlockedByLink = "is blocked by"

// jiraBlocksLink is the same link type's outward text, DepsOf's reverse
// direction (issue #1744).
const jiraBlocksLink = "blocks"

// DepsOf returns issue num's dependencies from native Jira issue links rather
// than prose parsing, so always DepSourceNative.
func (j *jiraClient) DepsOf(num string) ([]forge.Dependency, error) {
	var payload jiraIssuePayload
	if err := j.rest.Do(http.MethodGet, "/rest/api/2/issue/"+num, nil, &payload); err != nil {
		return nil, err
	}
	var deps []string
	seen := map[string]bool{}
	for _, link := range payload.Fields.IssueLinks {
		if link.Type.Inward == jiraBlockedByLink && link.InwardIssue != nil && !seen[link.InwardIssue.Key] {
			seen[link.InwardIssue.Key] = true
			deps = append(deps, link.InwardIssue.Key)
		}
	}
	return forge.WithSource(deps, forge.DepSourceNative), nil
}

// BlocksOf returns the issues num blocks, read from the same issuelinks
// payload's outward "blocks" entries (issue #1744).
func (j *jiraClient) BlocksOf(num string) ([]forge.Dependency, error) {
	var payload jiraIssuePayload
	if err := j.rest.Do(http.MethodGet, "/rest/api/2/issue/"+num, nil, &payload); err != nil {
		return nil, err
	}
	var deps []string
	seen := map[string]bool{}
	for _, link := range payload.Fields.IssueLinks {
		if link.Type.Outward == jiraBlocksLink && link.OutwardIssue != nil && !seen[link.OutwardIssue.Key] {
			seen[link.OutwardIssue.Key] = true
			deps = append(deps, link.OutwardIssue.Key)
		}
	}
	return forge.WithSource(deps, forge.DepSourceNative), nil
}

// issueState maps Jira's statusCategory to the canonical IssueState: "done" is
// Jira's terminal category whatever the workflow names its terminal status.
func issueState(p jiraIssuePayload) forge.IssueState {
	if p.Fields.Status.StatusCategory.Key == "done" {
		return forge.IssueClosed
	}
	return forge.IssueOpen
}

type jiraCommentsPayload struct {
	Comments []struct {
		Author struct {
			DisplayName string `json:"displayName"`
		} `json:"author"`
		Created string `json:"created"`
		Body    string `json:"body"`
	} `json:"comments"`
}

// Issue returns the Jira issue's summary, description, status, and labels. The
// comment thread is not inlined into Body: Comments delivers it via forge.IssueText.
func (j *jiraClient) Issue(num string) (forge.Issue, error) {
	var payload jiraIssuePayload
	if err := j.rest.Do(http.MethodGet, "/rest/api/2/issue/"+num, nil, &payload); err != nil {
		return forge.Issue{}, err
	}

	return forge.Issue{
		Number: payload.Key,
		Title:  payload.Fields.Summary,
		Body:   payload.Fields.Description,
		State:  issueState(payload),
		Labels: payload.Fields.Labels,
	}, nil
}

// Comments implements forge.CommentLister, returning issue num's comments
// oldest-first. Jira's comment endpoint emits them in creation order, which is
// what forge.IssueText assumes when it windows to the last 10.
func (j *jiraClient) Comments(num string) ([]forge.Comment, error) {
	var payload jiraCommentsPayload
	if err := j.rest.Do(http.MethodGet, "/rest/api/2/issue/"+num+"/comment", nil, &payload); err != nil {
		return nil, err
	}
	comments := make([]forge.Comment, len(payload.Comments))
	for i, c := range payload.Comments {
		comments[i] = forge.Comment{
			Author:    c.Author.DisplayName,
			CreatedAt: c.Created,
			Body:      c.Body,
		}
	}
	return comments, nil
}

var _ forge.CommentLister = (*jiraClient)(nil)

// TouchesOf parses the declared touch-set from issue num's description with
// the shared body grammar; Jira has no native touch-set to prefer over it.
func (j *jiraClient) TouchesOf(num string) ([]string, error) {
	iss, err := j.Issue(num)
	if err != nil {
		return nil, err
	}
	return forge.ParseTouchPaths(iss.Body), nil
}

func (j *jiraClient) Comment(num, body string) error {
	return j.rest.Do(http.MethodPost, "/rest/api/2/issue/"+num+"/comment",
		map[string]string{"body": body}, nil)
}

type jiraTransitionsPayload struct {
	Transitions []struct {
		ID string `json:"id"`
		To struct {
			Name string `json:"name"`
		} `json:"to"`
	} `json:"transitions"`
}

// errTransitionUnavailable marks the one case TransitionState falls back to a
// label for: the mapped status has no matching transition on the issue's
// current workflow. Any other error is an infra failure and must propagate
// rather than be swallowed into a silent fallback.
var errTransitionUnavailable = fmt.Errorf("jira: no available transition")

// transitionByStatus performs the workflow transition on issue num leading to
// targetStatus, returning errTransitionUnavailable when the issue's current
// workflow offers no such transition (ADR 0013).
func (j *jiraClient) transitionByStatus(num, targetStatus string) error {
	var payload jiraTransitionsPayload
	if err := j.rest.Do(http.MethodGet, "/rest/api/2/issue/"+num+"/transitions", nil, &payload); err != nil {
		return err
	}
	var transitionID string
	for _, t := range payload.Transitions {
		if strings.EqualFold(t.To.Name, targetStatus) {
			transitionID = t.ID
			break
		}
	}
	if transitionID == "" {
		return fmt.Errorf("%w to %q on issue %s", errTransitionUnavailable, targetStatus, num)
	}
	return j.rest.Do(http.MethodPost, "/rest/api/2/issue/"+num+"/transitions",
		map[string]any{"transition": map[string]string{"id": transitionID}}, nil)
}

// swapLabel adds the add label and removes the remove label on issue num via
// a single label-field update. Either may be empty to skip that half.
func (j *jiraClient) swapLabel(num, add, remove string) error {
	var ops []map[string]string
	if remove != "" {
		ops = append(ops, map[string]string{"remove": remove})
	}
	if add != "" {
		ops = append(ops, map[string]string{"add": add})
	}
	if len(ops) == 0 {
		return nil
	}
	return j.rest.Do(http.MethodPut, "/rest/api/2/issue/"+num,
		map[string]any{"update": map[string]any{"labels": ops}}, nil)
}

// alreadyClaimedNative reports whether num's current native status already
// equals the InProgress mapping, the native-mode half of the already-claimed
// check TransitionState runs before a claim. StatusMapping[from] == the
// InProgress target is exempted the same way the fallback-label helper
// exempts Label(from) == InProgress, mirroring the dispatch-workflow re-entry
// case (#3887).
func (j *jiraClient) alreadyClaimedNative(payload jiraIssuePayload, from forge.DispatchState) bool {
	target, ok := j.cfg.StatusMapping[forge.InProgress]
	if !ok || target == "" || j.cfg.StatusMapping[from] == target {
		return false
	}
	return strings.EqualFold(payload.Fields.Status.Name, target)
}

// TransitionState moves issue num from state from to state to via the Jira
// workflow transition matching StatusMapping[to]. When to is unmapped or that
// transition is unavailable, it swaps the DispatchLabels for from/to instead
// (ADR 0013) so the lifecycle always makes progress.
//
// A claim (to == InProgress) first GETs num and errors on
// forge.ErrAlreadyClaimed without transitioning when either the native status
// or the fallback label already reads InProgress (#3887), since either mode
// may be the one that actually landed a prior claim. The check is
// read-then-write, not atomic: another claimer can still land between the
// two.
func (j *jiraClient) TransitionState(num string, from, to forge.DispatchState) error {
	if to == forge.InProgress {
		var payload jiraIssuePayload
		if err := j.rest.Do(http.MethodGet, "/rest/api/2/issue/"+num, nil, &payload); err != nil {
			return err
		}
		if j.cfg.Labels.AlreadyClaimed(from, to, payload.Fields.Labels) || j.alreadyClaimedNative(payload, from) {
			return fmt.Errorf("jira: issue %s: %w (%q)", num, forge.ErrAlreadyClaimed, payload.Fields.Status.Name)
		}
	}
	if target, ok := j.cfg.StatusMapping[to]; ok && target != "" {
		err := j.transitionByStatus(num, target)
		if err == nil {
			// ListIssues matches a state by status OR its fallback label, so a
			// stale from label must not survive a successful native transition.
			// Best-effort: a cleanup failure must not undo the transition that
			// already succeeded.
			_ = j.swapLabel(num, "", j.cfg.Labels.Label(from))
			return nil
		}
		if !errors.Is(err, errTransitionUnavailable) {
			return err
		}
	}
	toLabel := j.cfg.Labels.Label(to)
	if toLabel == "" {
		return fmt.Errorf("jira: no status mapping or fallback label configured for state %v", to)
	}
	return j.swapLabel(num, toLabel, j.cfg.Labels.Label(from))
}

// CompleteVerdict swaps num's InProgress fallback label for verdict's terminal
// label; Jira has no native status mapping for research verdicts yet (ADR
// 0022). It first asserts the InProgress label is present as a double-dispatch
// guard (#701), a check-then-edit with the same TOCTOU window exec.go's
// CompleteVerdict documents.
func (j *jiraClient) CompleteVerdict(num string, verdict forge.Verdict) error {
	add := j.cfg.VerdictLabels.Label(verdict)
	if add == "" {
		return fmt.Errorf("jira: no label configured for verdict %v", verdict)
	}

	remove := j.cfg.Labels.Label(forge.InProgress)
	if remove != "" {
		var payload jiraIssuePayload
		if err := j.rest.Do(http.MethodGet, "/rest/api/2/issue/"+num, nil, &payload); err != nil {
			return err
		}
		if !slices.Contains(payload.Fields.Labels, remove) {
			return fmt.Errorf("jira: issue %s: expected %q label, issue has %v", num, remove, payload.Fields.Labels)
		}
	}

	return j.swapLabel(num, add, remove)
}

type jiraSearchPayload struct {
	Issues     []jiraIssuePayload `json:"issues"`
	StartAt    int                `json:"startAt"`
	MaxResults int                `json:"maxResults"`
	Total      int                `json:"total"`

	// Cloud's search/jql pages by an opaque token instead of startAt/total.
	NextPageToken string `json:"nextPageToken"`
	IsLast        bool   `json:"isLast"`
}

// ListIssues returns open issues in dispatch state state, created-time
// ascending. The query matches the mapped Jira status when one is configured
// and always ORs in the fallback label, so issues that fell back to a label
// are still found.
func (j *jiraClient) ListIssues(state forge.DispatchState) ([]forge.Issue, error) {
	issues, err := j.doSearch(j.stateJQL(state) + " order by created asc")
	if err != nil {
		return nil, err
	}
	return issuesFromIssues(issues), nil
}

// demandProbeInterval is how often the daemon re-counts Jira demand. A JQL
// search carries no conditional-request support, and Jira Cloud rate-limits
// REST calls, so the probe stays infrequent.
const demandProbeInterval = 5 * time.Minute

// ProbeInterval implements forge.DemandCounter.
func (j *jiraClient) ProbeInterval() time.Duration { return demandProbeInterval }

// CountReady implements forge.DemandCounter: the open Dispatchable issues,
// read without walking them: Cloud's approximate-count endpoint (the count may
// lag recent updates, which a demand probe tolerates since the child lists
// issues itself), or the total of a zero-row v2 search on Server/DC.
func (j *jiraClient) CountReady(_ bool) (int, error) {
	if j.cloud {
		var out struct {
			Count int `json:"count"`
		}
		body := map[string]string{"jql": j.stateJQL(forge.Dispatchable)}
		if err := j.rest.Do(http.MethodPost, "/rest/api/3/search/approximate-count", body, &out); err != nil {
			return 0, err
		}
		return out.Count, nil
	}
	q := url.Values{
		"jql":        {j.stateJQL(forge.Dispatchable)},
		"maxResults": {"0"},
	}
	var payload jiraSearchPayload
	if err := j.rest.Do(http.MethodGet, "/rest/api/2/search?"+q.Encode(), nil, &payload); err != nil {
		return 0, err
	}
	return payload.Total, nil
}

// stateJQL is the unordered JQL ListIssues and CountReady share, so the demand
// probe counts exactly the set a dispatch would list.
func (j *jiraClient) stateJQL(state forge.DispatchState) string {
	clauses := []string{fmt.Sprintf("project = %q", j.cfg.ProjectKey)}
	var stateClauses []string
	if target, ok := j.cfg.StatusMapping[state]; ok && target != "" {
		stateClauses = append(stateClauses, fmt.Sprintf("status = %q", target))
	}
	if label := j.cfg.Labels.Label(state); label != "" {
		stateClauses = append(stateClauses, fmt.Sprintf("labels = %q", label))
	}
	if len(stateClauses) > 0 {
		clauses = append(clauses, "("+strings.Join(stateClauses, " OR ")+")")
	}
	// A resolved issue must never come back as dispatchable, even when it still
	// carries a stale dispatch label from an earlier fallback transition.
	clauses = append(clauses, "statusCategory != Done")
	return strings.Join(clauses, " AND ")
}

// ListOpenIssues returns every open issue in the project, created-time
// ascending, with no status or label clause, so untriaged issues are included.
func (j *jiraClient) ListOpenIssues() ([]forge.Issue, error) {
	jql := fmt.Sprintf("project = %q AND statusCategory != Done order by created asc", j.cfg.ProjectKey)

	issues, err := j.doSearch(jql)
	if err != nil {
		return nil, err
	}
	return issuesFromIssues(issues), nil
}

// ListIssuesWithLabels implements forge.LabeledBacklogLister (issue #3873):
// state scopes the scan to open or closed issues, since a closed finding is a
// durable triage decision the host must not refile. One "labels in (...)"
// JQL clause covers every label in a single query, unlike github's and
// forgejo's per-label calls, because JQL natively supports an any-of match.
func (j *jiraClient) ListIssuesWithLabels(state forge.IssueState, labels []string) ([]forge.Issue, error) {
	var statusClause string
	switch state {
	case forge.IssueOpen:
		statusClause = "statusCategory != Done"
	case forge.IssueClosed:
		statusClause = "statusCategory = Done"
	default:
		return nil, fmt.Errorf("jira: unsupported issue state %q", state)
	}
	if len(labels) == 0 {
		return nil, nil
	}
	quoted := make([]string, len(labels))
	for i, label := range labels {
		quoted[i] = fmt.Sprintf("%q", label)
	}
	jql := fmt.Sprintf("project = %q AND labels in (%s) AND %s order by created desc",
		j.cfg.ProjectKey, strings.Join(quoted, ", "), statusClause)

	issues, err := j.doSearch(jql)
	if err != nil {
		return nil, err
	}
	return issuesFromIssues(issues), nil
}

func issuesFromIssues(payload []jiraIssuePayload) []forge.Issue {
	issues := make([]forge.Issue, len(payload))
	for i, p := range payload {
		issues[i] = forge.Issue{
			Number: p.Key,
			Title:  p.Fields.Summary,
			Body:   p.Fields.Description,
			State:  issueState(p),
			Labels: p.Fields.Labels,
		}
	}
	return issues
}

// doSearch runs a JQL search, walking every result page so a backlog larger
// than one ResultPageLimit page is never silently truncated. JQL orders
// results server-side, so appending pages in fetch order preserves
// oldest-first without a client-side sort.
//
// Cloud removed v2 /search (410), so it walks /search/jql by nextPageToken. The
// v2 variant is used because it returns description as plain text rather than
// ADF, and its fields must be named or only the id comes back. Cloud may not
// honour maxResults, so the walk never counts rows, and it fails on a repeated
// token rather than spin. Server/DC walks startAt until total, returning an
// error rather than a short result when a page comes back empty before total
// or repeats the previous page, so a nil error means the result is complete.
func (j *jiraClient) doSearch(jql string) ([]jiraIssuePayload, error) {
	var all []jiraIssuePayload
	if j.cloud {
		token := ""
		err := j.rest.Paginate(func(int) (bool, error) {
			q := url.Values{
				"jql":        {jql},
				"fields":     {"summary,description,status,labels"},
				"maxResults": {fmt.Sprintf("%d", forge.ResultPageLimit)},
			}
			if token != "" {
				q.Set("nextPageToken", token)
			}
			var payload jiraSearchPayload
			if err := j.rest.Do(http.MethodGet, "/rest/api/2/search/jql?"+q.Encode(), nil, &payload); err != nil {
				return false, err
			}
			all = append(all, payload.Issues...)
			if !payload.IsLast && payload.NextPageToken != "" && payload.NextPageToken == token {
				return false, fmt.Errorf("jira search/jql returned the same nextPageToken twice; refusing to loop")
			}
			token = payload.NextPageToken
			return payload.IsLast || token == "", nil
		})
		if err != nil {
			return nil, err
		}
		return all, nil
	}
	var prevFirstKey string
	err := j.rest.Paginate(func(int) (bool, error) {
		// Advance by rows received, not a fixed stride: the server may cap the
		// page below maxResults.
		startAt := len(all)
		q := url.Values{
			"jql":        {jql},
			"startAt":    {fmt.Sprintf("%d", startAt)},
			"maxResults": {fmt.Sprintf("%d", forge.ResultPageLimit)},
		}
		var payload jiraSearchPayload
		if err := j.rest.Do(http.MethodGet, "/rest/api/2/search?"+q.Encode(), nil, &payload); err != nil {
			return false, err
		}
		if len(payload.Issues) == 0 && startAt < payload.Total {
			return false, fmt.Errorf("jira: search returned an empty page at startAt %d before total %d", startAt, payload.Total)
		}
		// A server ignoring startAt replays the previous page; appending it would
		// duplicate rows until total is reached.
		if startAt > 0 && len(payload.Issues) > 0 && payload.Issues[0].Key == prevFirstKey {
			return false, fmt.Errorf("jira: search repeated the page starting at %s for startAt %d", prevFirstKey, startAt)
		}
		all = append(all, payload.Issues...)
		if len(payload.Issues) > 0 {
			prevFirstKey = payload.Issues[0].Key
		}
		return len(all) >= payload.Total, nil
	})
	if err != nil {
		return nil, err
	}
	return all, nil
}

type jiraLabelsPayload struct {
	Values []string `json:"values"`
}

// WalksAllPages implements forge.FullyPaginated: doSearch walks every page, so
// results are never truncated at forge.ResultPageLimit and a caller's
// page-limit fail-safe can trust a full-looking result as complete.
func (j *jiraClient) WalksAllPages() bool {
	return true
}

// ListLabels returns Jira's site-wide label list.
func (j *jiraClient) ListLabels() ([]string, error) {
	var payload jiraLabelsPayload
	if err := j.rest.Do(http.MethodGet, "/rest/api/2/label", nil, &payload); err != nil {
		return nil, err
	}
	return payload.Values, nil
}

// CreateLabel is a no-op: Jira labels are free text with no registration
// endpoint, created implicitly the first time they are applied to an issue.
func (j *jiraClient) CreateLabel(name, description, color string) error {
	return nil
}

// Probe checks Jira connectivity/auth and returns the configured project key.
func (j *jiraClient) Probe() (string, error) {
	if err := j.rest.Do(http.MethodGet, "/rest/api/2/myself", nil, nil); err != nil {
		if errors.Is(err, forge.ErrAuthFailure) {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", forge.ErrRepoNotFound, err)
	}
	return j.cfg.ProjectKey, nil
}
