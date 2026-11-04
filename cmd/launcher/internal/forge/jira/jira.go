// Package jira is the Jira REST adapter. It satisfies only the parent forge
// package's IssueTracker interface; code lands via whichever Code Forge
// CODE_FORGE selects (ADR 0013).
package jira

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/rest"
)

// defaultJiraHTTPTimeout bounds every Jira REST call so a server that accepts
// the connection but never answers cannot stall a caller forever.
const defaultJiraHTTPTimeout = 30 * time.Second

// JiraConfig configures the Jira IssueTracker adapter.
type JiraConfig struct {
	BaseURL    string // Jira site base URL, e.g. https://yourcompany.atlassian.net
	ProjectKey string
	Email      string // Jira Cloud: Basic auth and Cloud search APIs; empty selects Bearer-token auth and v2 /search (Server/Data Center PAT)
	Token      string

	// StatusMapping maps canonical DispatchState values to native Jira status
	// targets. TransitionState falls back to Labels when a state is unmapped or
	// the mapped transition is unavailable on the issue's workflow.
	StatusMapping map[forge.DispatchState]StatusTarget
	// Labels are the fallback labels applied when a transition is unmapped or
	// blocked by the project's workflow.
	Labels forge.DispatchLabels
	// VerdictLabels configures CompleteVerdict, applied via the same
	// label-fallback path as Labels; native status mapping for research
	// verdicts is deferred (ADR 0022).
	VerdictLabels forge.VerdictLabels

	// HTTPClient overrides the client used for Jira REST calls; nil gets a
	// client bounded by defaultJiraHTTPTimeout, never the untimed
	// http.DefaultClient.
	HTTPClient *http.Client
}

var statusMappingKeys = map[string]forge.DispatchState{
	"dispatchable": forge.Dispatchable,
	"inProgress":   forge.InProgress,
	"complete":     forge.Complete,
	"failed":       forge.Failed,
	"ambiguous":    forge.Ambiguous,
}

// StatusTarget is what a canonical state maps to in Jira: the native status
// name, plus the resolution to set on the transition (empty leaves it to the
// workflow's default).
type StatusTarget struct {
	Status     string `json:"status"`
	Resolution string `json:"resolution"`
}

// statusTargetFields are the fields a JIRA_STATUS_MAPPING object value allows.
var statusTargetFields = []string{"status", "resolution"}

// quotedStatusTargetFields renders statusTargetFields as the "status", "resolution"
// list the object-shape error message shows.
func quotedStatusTargetFields() string {
	quoted := make([]string, len(statusTargetFields))
	for i, f := range statusTargetFields {
		quoted[i] = fmt.Sprintf("%q", f)
	}
	return strings.Join(quoted, ", ")
}

// jsonTypeName names v's JSON type as the error messages report it.
func jsonTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case []any:
		return "array"
	default:
		return "object"
	}
}

// ParseStatusMapping parses the JIRA_STATUS_MAPPING knob, a JSON object mapping
// statusMappingKeys to a native Jira status name or a {"status", "resolution"}
// object. An unknown key or field is an error so a typo fails fast at startup
// instead of silently dropping the mapping. Keys are checked in sorted order so
// the first error is deterministic; the Nix mirror reports the same one. Two
// keys may share a status (compared with strings.EqualFold, as the claim check
// is) only if they are failed and ambiguous, which both mean a human must act
// and fit one Blocked column; any other share would, say, make every ready card
// look claimed. The Nix eval-time mirror folds ASCII only, so a non-ASCII
// case-only share is caught at startup only.
func ParseStatusMapping(s string) (map[forge.DispatchState]StatusTarget, error) {
	out := map[forge.DispatchState]StatusTarget{}
	if s == "" {
		return out, nil
	}
	var top any
	if err := json.Unmarshal([]byte(s), &top); err != nil {
		return nil, fmt.Errorf("parse JIRA_STATUS_MAPPING: %w", err)
	}
	if top == nil {
		return out, nil
	}
	raw, ok := top.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("JIRA_STATUS_MAPPING: expected a JSON object, got %s", jsonTypeName(top))
	}
	keys := slices.Sorted(maps.Keys(raw))
	for _, key := range keys {
		state, ok := statusMappingKeys[key]
		if !ok {
			return nil, fmt.Errorf("JIRA_STATUS_MAPPING: unknown key %q (want one of dispatchable, inProgress, complete, failed, ambiguous)", key)
		}
		target, err := parseStatusTarget(key, raw[key])
		if err != nil {
			return nil, err
		}
		out[state] = target
	}
	// Keys are sorted so the error names the same pair every run and
	// ambiguous always precedes failed.
	for i, k := range keys {
		status := out[statusMappingKeys[k]].Status
		if status == "" {
			continue
		}
		for _, p := range keys[:i] {
			held := out[statusMappingKeys[p]].Status
			if !strings.EqualFold(held, status) || (p == "ambiguous" && k == "failed") {
				continue
			}
			return nil, fmt.Errorf("JIRA_STATUS_MAPPING: keys %q and %q both map to status %q (only failed and ambiguous may share a status)", p, k, held)
		}
	}
	return out, nil
}

// parseStatusTarget decodes one JIRA_STATUS_MAPPING value: null or a string is
// a bare status name (empty means unmapped), an object carries the fields in
// statusTargetFields.
func parseStatusTarget(key string, v any) (StatusTarget, error) {
	switch v := v.(type) {
	case nil:
		return StatusTarget{}, nil
	case string:
		return StatusTarget{Status: v}, nil
	case map[string]any:
		fields := map[string]string{}
		for _, name := range slices.Sorted(maps.Keys(v)) {
			if !slices.Contains(statusTargetFields, name) {
				return StatusTarget{}, fmt.Errorf("JIRA_STATUS_MAPPING: key %q: unknown field %q (want status, resolution)", key, name)
			}
			str, ok := v[name].(string)
			if !ok {
				return StatusTarget{}, fmt.Errorf("JIRA_STATUS_MAPPING: key %q: field %q must be a string, got %s", key, name, jsonTypeName(v[name]))
			}
			fields[name] = str
		}
		if fields["status"] == "" {
			return StatusTarget{}, fmt.Errorf("JIRA_STATUS_MAPPING: key %q: %q must be a non-empty string", key, "status")
		}
		if res, present := fields["resolution"]; present && res == "" {
			return StatusTarget{}, fmt.Errorf("JIRA_STATUS_MAPPING: key %q: %q must be a non-empty string", key, "resolution")
		}
		return StatusTarget{Status: fields["status"], Resolution: fields["resolution"]}, nil
	default:
		return StatusTarget{}, fmt.Errorf("JIRA_STATUS_MAPPING: key %q: expected a status name or a {%s} object, got %s", key, quotedStatusTargetFields(), jsonTypeName(v))
	}
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
		hc = &http.Client{Timeout: defaultJiraHTTPTimeout}
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
		Resolution struct {
			Name string `json:"name"`
		} `json:"resolution"`
		ResolutionDate string `json:"resolutiondate"`
		IssueLinks     []struct {
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

// jiraDateLayout is the timestamp format Jira emits (not RFC3339: the zone
// offset has no colon).
const jiraDateLayout = "2006-01-02T15:04:05.000-0700"

// jiraNotPlannedResolutions are the default resolution names that mean the
// work was declined, matched case-insensitively. Duplicate is left out on
// purpose: github's NOT_PLANNED mapping excludes it, and a duplicate finding
// should keep suppressing refiles.
var jiraNotPlannedResolutions = []string{"Won't Do", "Won't Fix", "Declined"}

// issue maps the payload to a forge.Issue. The close reason and time are
// reported only for a closed issue, and a malformed resolutiondate leaves
// ClosedAt zero (which never expires a cooldown) rather than failing the read.
func (p jiraIssuePayload) issue() forge.Issue {
	iss := forge.Issue{
		Number: p.Key,
		Title:  p.Fields.Summary,
		Body:   p.Fields.Description,
		State:  issueState(p),
		Labels: p.Fields.Labels,
	}
	if iss.State == forge.IssueClosed {
		iss.ClosedAt, _ = time.Parse(jiraDateLayout, p.Fields.ResolutionDate)
		iss.ClosedNotPlanned = slices.ContainsFunc(jiraNotPlannedResolutions, func(r string) bool {
			return strings.EqualFold(r, p.Fields.Resolution.Name)
		})
	}
	return iss
}

type jiraCommentsPayload struct {
	Comments []struct {
		Author struct {
			DisplayName string `json:"displayName"`
		} `json:"author"`
		Created    string         `json:"created"`
		Body       string         `json:"body"`
		Properties []jiraProperty `json:"properties"`
	} `json:"comments"`
}

// Issue returns the Jira issue's summary, description, status, and labels. The
// comment thread is not inlined into Body: Comments delivers it via forge.IssueText.
func (j *jiraClient) Issue(num string) (forge.Issue, error) {
	var payload jiraIssuePayload
	if err := j.rest.Do(http.MethodGet, "/rest/api/2/issue/"+num, nil, &payload); err != nil {
		return forge.Issue{}, err
	}
	return payload.issue(), nil
}

// Comments implements forge.CommentLister, returning issue num's comments
// oldest-first. Jira's comment endpoint emits them in creation order, which is
// what forge.IssueText assumes when it windows to the last 10.
func (j *jiraClient) Comments(num string) ([]forge.Comment, error) {
	var payload jiraCommentsPayload
	if err := j.rest.Do(http.MethodGet, "/rest/api/2/issue/"+num+"/comment?expand=properties", nil, &payload); err != nil {
		return nil, err
	}
	comments := make([]forge.Comment, len(payload.Comments))
	for i, c := range payload.Comments {
		comments[i] = forge.Comment{
			Author:    c.Author.DisplayName,
			CreatedAt: c.Created,
			Body:      restoreResearchMarker(c.Body, c.Properties),
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
	body, isResearch := stripResearchMarker(body)
	req := jiraCommentRequest{Body: toWikiMarkup(body)}
	if isResearch {
		req.Properties = []jiraProperty{{Key: researchProperty, Value: true}}
	}
	return j.rest.Do(http.MethodPost, "/rest/api/2/issue/"+num+"/comment", req, nil)
}

// researchProperty is the comment entity property that carries the research
// marker: wiki markup has no comment syntax, so the literal would render.
const researchProperty = "spindrift-research"

type jiraProperty struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

type jiraCommentRequest struct {
	Body       string         `json:"body"`
	Properties []jiraProperty `json:"properties,omitempty"`
}

// stripResearchMarker drops the first line that is exactly the research marker,
// the only one restoreResearchMarker puts back; later copies (a verdict quoting
// it) stay verbatim. The dedup marker stays inline.
func stripResearchMarker(body string) (string, bool) {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == forge.ResearchMarker {
			return strings.Join(append(lines[:i], lines[i+1:]...), "\n"), true
		}
	}
	return body, false
}

// restoreResearchMarker puts the marker back as the first line when the comment
// carries the property. Comments without it (human, or verdicts posted with an
// inline marker) read back untouched.
func restoreResearchMarker(body string, props []jiraProperty) string {
	for _, p := range props {
		if p.Key == researchProperty {
			return forge.ResearchMarker + "\n" + body
		}
	}
	return body
}

type jiraTransitionsPayload struct {
	Transitions []struct {
		ID string `json:"id"`
		To struct {
			Name string `json:"name"`
		} `json:"to"`
		Fields map[string]struct {
			Required        bool   `json:"required"`
			HasDefaultValue bool   `json:"hasDefaultValue"`
			Name            string `json:"name"`
		} `json:"fields"`
	} `json:"transitions"`
}

// errTransitionUnavailable marks the one case TransitionState falls back to a
// label for: the mapped status has no matching transition on the issue's
// current workflow. Any other error is an infra failure and must propagate
// rather than be swallowed into a silent fallback.
var errTransitionUnavailable = fmt.Errorf("jira: no available transition")

// transitionByStatus performs the workflow transition on issue num leading to
// target.Status, returning errTransitionUnavailable when the issue's current
// workflow offers no such transition (ADR 0013). target.Resolution, when set,
// rides the POST as fields.resolution. A transition that requires some other
// field, or a resolution the transition's screen cannot take (Jira would answer
// 400), fails before any POST with an error that is deliberately not
// errTransitionUnavailable: a label fallback would hide a mapping the operator
// must fix (ADR 0072).
func (j *jiraClient) transitionByStatus(num string, target StatusTarget) error {
	var payload jiraTransitionsPayload
	if err := j.rest.Do(http.MethodGet, "/rest/api/2/issue/"+num+"/transitions?expand=transitions.fields", nil, &payload); err != nil {
		return err
	}
	for _, t := range payload.Transitions {
		if !strings.EqualFold(t.To.Name, target.Status) {
			continue
		}
		if _, ok := t.Fields["resolution"]; target.Resolution != "" && !ok {
			return fmt.Errorf("jira: transition to %q on issue %s has no resolution field on its screen, so the resolution %q that JIRA_STATUS_MAPPING sets cannot be applied",
				target.Status, num, target.Resolution)
		}
		var missing []string
		for _, key := range slices.Sorted(maps.Keys(t.Fields)) {
			f := t.Fields[key]
			if !f.Required || f.HasDefaultValue || (key == "resolution" && target.Resolution != "") {
				continue
			}
			name := key
			if f.Name != "" {
				name = fmt.Sprintf("%s (%s)", f.Name, key)
			}
			missing = append(missing, name)
		}
		if len(missing) > 0 {
			return fmt.Errorf("jira: transition to %q on issue %s requires field(s) %s that JIRA_STATUS_MAPPING does not supply (only resolution can be set, via the {\"status\",\"resolution\"} object form)",
				target.Status, num, strings.Join(missing, ", "))
		}
		body := map[string]any{"transition": map[string]string{"id": t.ID}}
		if target.Resolution != "" {
			body["fields"] = map[string]any{"resolution": map[string]string{"name": target.Resolution}}
		}
		return j.rest.Do(http.MethodPost, "/rest/api/2/issue/"+num+"/transitions", body, nil)
	}
	return fmt.Errorf("%w to %q on issue %s", errTransitionUnavailable, target.Status, num)
}

// swapLabel adds the add label and removes every removes label on issue num
// via a single label-field update. add and any removes entry may be empty to
// skip that op.
func (j *jiraClient) swapLabel(num, add string, removes ...string) error {
	var ops []map[string]string
	for _, remove := range removes {
		if remove != "" {
			ops = append(ops, map[string]string{"remove": remove})
		}
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

// nativeStatus returns the StatusTarget StatusMapping maps s to, and whether s
// is natively mapped at all (an empty status counts as unmapped). Every read of
// the mapping goes through it so "mapped" has one definition.
func (j *jiraClient) nativeStatus(s forge.DispatchState) (StatusTarget, bool) {
	target := j.cfg.StatusMapping[s]
	return target, target.Status != ""
}

// alreadyClaimedNative reports whether num's current native status already
// equals the InProgress mapping, the native-mode half of the already-claimed
// check TransitionState runs before a claim. StatusMapping[from] == the
// InProgress target is exempted the same way the fallback-label helper
// exempts Label(from) == InProgress, mirroring the dispatch-workflow re-entry
// case (#3887).
func (j *jiraClient) alreadyClaimedNative(payload jiraIssuePayload, from forge.DispatchState) bool {
	target, ok := j.nativeStatus(forge.InProgress)
	if fromTarget, _ := j.nativeStatus(from); !ok || fromTarget.Status == target.Status {
		return false
	}
	return strings.EqualFold(payload.Fields.Status.Name, target.Status)
}

// TransitionState moves issue num from state from to state to via the Jira
// workflow transition matching StatusMapping[to]. When to is unmapped or that
// transition is unavailable, it swaps the DispatchLabels for from/to instead
// (ADR 0013) so the lifecycle always makes progress. Either way it removes the
// labels forge.TransitionRemoveLabels names, so a claim or landing also strips
// a stale terminal or research verdict label like the other trackers.
//
// A claim (to == InProgress) first GETs num and errors on
// forge.ErrAlreadyClaimed without transitioning when either the native status
// or the fallback label already reads InProgress (#3887), since either mode
// may be the one that actually landed a prior claim. The check is
// read-then-write, not atomic: another claimer can still land between the
// two. On a native claim it then strips any settled labels that GET showed
// before transitioning, and fails the claim if that strip fails: nothing has
// moved yet, whereas a surviving settled label makes shutdown.unsettled read
// the live Box as already settled (#4917). The post-transition
// TransitionRemoveLabels strip stays best-effort.
func (j *jiraClient) TransitionState(num string, from, to forge.DispatchState) error {
	target, native := j.nativeStatus(to)
	if to == forge.InProgress {
		var payload jiraIssuePayload
		if err := j.rest.Do(http.MethodGet, "/rest/api/2/issue/"+num, nil, &payload); err != nil {
			return err
		}
		if j.cfg.Labels.AlreadyClaimed(from, to, payload.Fields.Labels) || j.alreadyClaimedNative(payload, from) {
			return fmt.Errorf("jira: issue %s: %w (%q)", num, forge.ErrAlreadyClaimed, payload.Fields.Status.Name)
		}
		if native {
			var stale []string
			for _, l := range forge.SettledLabels(j.cfg.Labels, j.cfg.VerdictLabels) {
				if slices.Contains(payload.Fields.Labels, l) {
					stale = append(stale, l)
				}
			}
			if err := j.swapLabel(num, "", stale...); err != nil {
				return fmt.Errorf("jira: issue %s: strip settled labels before claim: %w", num, err)
			}
		}
	}
	if native {
		err := j.transitionByStatus(num, target)
		if err == nil {
			// ListIssues matches a state by status OR its fallback label, so a
			// stale from label (or a stale terminal the shared rule strips)
			// must not survive a successful native transition. Best-effort: a
			// cleanup failure must not undo the transition that already
			// succeeded.
			_ = j.swapLabel(num, "", forge.TransitionRemoveLabels(j.cfg.Labels, j.cfg.VerdictLabels, from, to)...)
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
	return j.swapLabel(num, toLabel, forge.TransitionRemoveLabels(j.cfg.Labels, j.cfg.VerdictLabels, from, to)...)
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
	if target, ok := j.nativeStatus(state); ok {
		stateClauses = append(stateClauses, fmt.Sprintf("status = %q", target.Status))
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
		issues[i] = p.issue()
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
				"fields":     {"summary,description,status,labels,resolution,resolutiondate"},
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
