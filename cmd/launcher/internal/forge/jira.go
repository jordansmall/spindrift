package forge

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// JiraConfig configures the Jira IssueTracker adapter. Per ADR 0013, Jira
// implements only IssueTracker — code still lands via the github CodeForge.
type JiraConfig struct {
	BaseURL    string // Jira site base URL, e.g. https://yourcompany.atlassian.net
	ProjectKey string
	Email      string // Jira Cloud Basic auth; empty selects Bearer-token auth (Server/Data Center PAT)
	Token      string

	// StatusMapping maps canonical DispatchState values to native Jira status
	// names. TransitionState performs the matching workflow transition; when a
	// state is unmapped, or the mapped transition is not available on the
	// issue's current workflow, TransitionState falls back to Labels.
	StatusMapping map[DispatchState]string
	// Labels are the fallback labels applied when a transition is unmapped or
	// blocked by the project's workflow.
	Labels DispatchLabels
	// IncludeComments, when true, appends the issue's comment thread to the
	// Body returned by Issue. Opt-in to keep the prompt-injection surface tight
	// by default.
	IncludeComments bool

	// HTTPClient overrides the HTTP client used for Jira REST calls; nil uses
	// http.DefaultClient. Tests inject a client pointed at a fake server.
	HTTPClient *http.Client
}

// statusMappingKeys maps the JSON keys accepted by ParseStatusMapping to
// their canonical DispatchState.
var statusMappingKeys = map[string]DispatchState{
	"dispatchable": Dispatchable,
	"inProgress":   InProgress,
	"complete":     Complete,
	"failed":       Failed,
}

// ParseStatusMapping parses the JIRA_STATUS_MAPPING config knob: a JSON
// object with keys "dispatchable", "inProgress", "complete", "failed" mapping
// to native Jira status names. An empty string yields an empty mapping (every
// state falls back to its label). An unknown key is a config error, so a
// typo fails fast at startup rather than silently dropping the mapping.
func ParseStatusMapping(s string) (map[DispatchState]string, error) {
	out := map[DispatchState]string{}
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

// jiraClient is the Jira REST adapter. It satisfies IssueTracker only.
type jiraClient struct {
	cfg JiraConfig
	hc  *http.Client
}

// NewJiraClient returns an IssueTracker backed by the Jira REST API.
func NewJiraClient(cfg JiraConfig) IssueTracker {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return &jiraClient{cfg: cfg, hc: hc}
}

// authHeader returns the Authorization header value: Basic email:token for
// Jira Cloud, or Bearer token for Server/Data Center PATs.
func (j *jiraClient) authHeader() string {
	if j.cfg.Email != "" {
		raw := j.cfg.Email + ":" + j.cfg.Token
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(raw))
	}
	return "Bearer " + j.cfg.Token
}

// do issues a Jira REST request with the given method, path, and JSON body
// (nil for none), and decodes a JSON response into out (nil to discard the
// body). It returns the HTTP status code so callers can branch on it.
func (j *jiraClient) do(method, path string, body, out any) (int, error) {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("jira: marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, j.cfg.BaseURL+path, reqBody)
	if err != nil {
		return 0, fmt.Errorf("jira: build request: %w", err)
	}
	req.Header.Set("Authorization", j.authHeader())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := j.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("jira: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
			return resp.StatusCode, fmt.Errorf("jira: decode response from %s %s: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// jiraIssuePayload is the subset of the Jira issue REST representation this
// adapter reads.
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
				Inward string `json:"inward"`
			} `json:"type"`
			InwardIssue *struct {
				Key string `json:"key"`
			} `json:"inwardIssue"`
		} `json:"issuelinks"`
	} `json:"fields"`
}

// jiraBlockedByLink is the inward relationship text Jira's built-in "Blocks"
// link type uses to mean "this issue is blocked by the linked issue".
const jiraBlockedByLink = "is blocked by"

// DepsOf returns the canonical dependency IDs for issue num, resolved from
// native Jira issue links (not prose parsing).
func (j *jiraClient) DepsOf(num string) ([]string, error) {
	var payload jiraIssuePayload
	status, err := j.do(http.MethodGet, "/rest/api/2/issue/"+num, nil, &payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("jira: issue %s: unexpected status %d", num, status)
	}
	var deps []string
	for _, link := range payload.Fields.IssueLinks {
		if link.Type.Inward == jiraBlockedByLink && link.InwardIssue != nil {
			deps = append(deps, link.InwardIssue.Key)
		}
	}
	return deps, nil
}

// issueState maps Jira's statusCategory to the canonical IssueState: "done"
// is Jira's terminal category (regardless of the workflow's custom terminal
// status name, e.g. "Done", "Won't Fix", "Resolved").
func issueState(p jiraIssuePayload) IssueState {
	if p.Fields.Status.StatusCategory.Key == "done" {
		return IssueClosed
	}
	return IssueOpen
}

type jiraCommentsPayload struct {
	Comments []struct {
		Body string `json:"body"`
	} `json:"comments"`
}

// Issue returns the Jira issue's summary, description, status, and labels.
// When IncludeComments is set, the comment thread is appended to Body.
func (j *jiraClient) Issue(num string) (Issue, error) {
	var payload jiraIssuePayload
	status, err := j.do(http.MethodGet, "/rest/api/2/issue/"+num, nil, &payload)
	if err != nil {
		return Issue{}, err
	}
	if status != http.StatusOK {
		return Issue{}, fmt.Errorf("jira: issue %s: unexpected status %d", num, status)
	}

	body := payload.Fields.Description
	if j.cfg.IncludeComments {
		var comments jiraCommentsPayload
		cStatus, cErr := j.do(http.MethodGet, "/rest/api/2/issue/"+num+"/comment", nil, &comments)
		if cErr != nil {
			return Issue{}, cErr
		}
		if cStatus != http.StatusOK {
			return Issue{}, fmt.Errorf("jira: issue %s comments: unexpected status %d", num, cStatus)
		}
		if len(comments.Comments) > 0 {
			var b bytes.Buffer
			b.WriteString(body)
			b.WriteString("\n\n## Comments\n")
			for _, c := range comments.Comments {
				b.WriteString("\n- ")
				b.WriteString(c.Body)
			}
			body = b.String()
		}
	}

	return Issue{
		Number: payload.Key,
		Title:  payload.Fields.Summary,
		Body:   body,
		State:  issueState(payload),
		Labels: payload.Fields.Labels,
	}, nil
}

// Comment posts a comment on the Jira issue.
func (j *jiraClient) Comment(num, body string) error {
	status, err := j.do(http.MethodPost, "/rest/api/2/issue/"+num+"/comment",
		map[string]string{"body": body}, nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("jira: comment %s: unexpected status %d", num, status)
	}
	return nil
}

type jiraTransitionsPayload struct {
	Transitions []struct {
		ID string `json:"id"`
		To struct {
			Name string `json:"name"`
		} `json:"to"`
	} `json:"transitions"`
}

// errTransitionUnavailable marks the case TransitionState should fall back to
// a label for: the mapped status has no matching transition on the issue's
// current workflow. Any other error from transitionByStatus is an infra
// failure (network, auth, 5xx) and must propagate, not be swallowed into a
// silent fallback.
var errTransitionUnavailable = fmt.Errorf("jira: no available transition")

// transitionByStatus performs the workflow transition on issue num that leads
// to targetStatus. It returns errTransitionUnavailable if no such transition
// is available on the issue's current workflow (the unmapped/blocked case
// TransitionState falls back to a label for, per ADR 0013); any other
// non-nil error is an infra failure that must propagate.
func (j *jiraClient) transitionByStatus(num, targetStatus string) error {
	var payload jiraTransitionsPayload
	status, err := j.do(http.MethodGet, "/rest/api/2/issue/"+num+"/transitions", nil, &payload)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("jira: list transitions for %s: unexpected status %d", num, status)
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
	status, err = j.do(http.MethodPost, "/rest/api/2/issue/"+num+"/transitions",
		map[string]any{"transition": map[string]string{"id": transitionID}}, nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("jira: transition %s to %q: unexpected status %d", num, targetStatus, status)
	}
	return nil
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
	status, err := j.do(http.MethodPut, "/rest/api/2/issue/"+num,
		map[string]any{"update": map[string]any{"labels": ops}}, nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("jira: update labels on %s: unexpected status %d", num, status)
	}
	return nil
}

// TransitionState moves issue num from state from to state to. It performs
// the Jira workflow transition matching StatusMapping[to]; when to is
// unmapped or the matching transition is not available on the issue's
// current workflow, it falls back to swapping the DispatchLabels for from/to
// (per ADR 0013) so the lifecycle always makes progress.
func (j *jiraClient) TransitionState(num string, from, to DispatchState) error {
	if target, ok := j.cfg.StatusMapping[to]; ok && target != "" {
		err := j.transitionByStatus(num, target)
		if err == nil {
			// ListIssues matches a state by status OR its fallback label, so
			// an issue discovered via the from label (a prior fallback, or
			// an operator-applied label) must not still carry it after a
			// successful native-status transition — best-effort; a cleanup
			// failure must not undo the transition that already succeeded.
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

type jiraSearchPayload struct {
	Issues []jiraIssuePayload `json:"issues"`
}

// ListIssues returns open issues in dispatch state state, in canonical order
// (created-time ascending). Issues are matched by the mapped Jira status for
// state when one is configured; the fallback label for state is always
// included in the query too, so issues that fell back to a label (because
// the mapped transition was unmapped or blocked) are still found.
func (j *jiraClient) ListIssues(state DispatchState) ([]Issue, error) {
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
	// Mirrors the github adapter's --state open: a resolved/closed issue must
	// never be returned as dispatchable, even if it still carries a stale
	// dispatch label from an earlier fallback transition.
	clauses = append(clauses, "statusCategory != Done")
	jql := strings.Join(clauses, " AND ") + " order by created asc"

	var payload jiraSearchPayload
	status, err := j.doSearch(jql, &payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("jira: search: unexpected status %d", status)
	}
	if len(payload.Issues) >= jiraSearchMaxResults {
		fmt.Printf("WARNING: jira search returned %d issues (limit %d); backlog may be larger — rerun to drain\n",
			len(payload.Issues), jiraSearchMaxResults)
	}
	issues := make([]Issue, len(payload.Issues))
	for i, p := range payload.Issues {
		issues[i] = Issue{
			Number: p.Key,
			Title:  p.Fields.Summary,
			Body:   p.Fields.Description,
			State:  issueState(p),
			Labels: p.Fields.Labels,
		}
	}
	return issues, nil
}

// jiraSearchMaxResults bounds a single ListIssues search page (mirroring the
// github adapter's issueQueryLimit); a backlog larger than this drains over
// successive dispatch runs rather than in one unbounded response.
const jiraSearchMaxResults = 100

// doSearch issues a Jira JQL search request via GET /rest/api/2/search.
func (j *jiraClient) doSearch(jql string, out any) (int, error) {
	q := url.Values{"jql": {jql}, "maxResults": {fmt.Sprintf("%d", jiraSearchMaxResults)}}
	return j.do(http.MethodGet, "/rest/api/2/search?"+q.Encode(), nil, out)
}

type jiraLabelsPayload struct {
	Values []string `json:"values"`
}

// ListLabels returns Jira's site-wide label list.
func (j *jiraClient) ListLabels() ([]string, error) {
	var payload jiraLabelsPayload
	status, err := j.do(http.MethodGet, "/rest/api/2/label", nil, &payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("jira: list labels: unexpected status %d", status)
	}
	return payload.Values, nil
}

// CreateLabel is a no-op: Jira labels are free text with no registration
// endpoint — a label is created implicitly the first time it is applied to
// an issue (see swapLabel/TransitionState's fallback path).
func (j *jiraClient) CreateLabel(name, description, color string) error {
	return nil
}

// Probe checks Jira connectivity/auth and returns the configured project key.
func (j *jiraClient) Probe() (string, error) {
	status, err := j.do(http.MethodGet, "/rest/api/2/myself", nil, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrRepoNotFound, err)
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return "", fmt.Errorf("%w: jira returned %d", ErrAuthFailure, status)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("%w: jira returned %d", ErrRepoNotFound, status)
	}
	return j.cfg.ProjectKey, nil
}
