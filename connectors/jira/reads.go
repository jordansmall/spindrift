package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

const (
	pageSize = 100
	// maxPages bounds every paging loop; a longer list fails rather than truncates.
	maxPages = 50

	// dedupPropertyKey is the issue entity property holding a filed finding's
	// dedup marker, and researchPropertyKey the comment property marking a
	// research verdict; both keep a marker out of text Jira's wiki markup
	// would render.
	dedupPropertyKey    = "spindrift.dedup"
	researchPropertyKey = "spindrift-research"

	// Jira's built-in "Blocks" link type, by direction.
	blockedByLink = "is blocked by"
	blocksLink    = "blocks"

	// dateLayout is the timestamp format Jira emits (not RFC3339: the zone
	// offset has no colon).
	dateLayout = "2006-01-02T15:04:05.000-0700"
)

// notPlannedResolutions are the resolution names that mean the work was
// declined, matched case-insensitively. Duplicate is left out on purpose: a
// duplicate finding should keep suppressing refiles.
var notPlannedResolutions = []string{"Won't Do", "Won't Fix", "Declined"}

// send is the HTTP transport; tests swap it because connector.HTTPRequest
// fails outside wasip1.
var send = connector.HTTPRequest

// get sends a GET to t.API+path and decodes a 2xx JSON body into out. The host
// attaches the credential, so none is set here.
func get(t tracker.Target, path string, out any) error {
	resp, err := send(connector.Request{
		Method:  "GET",
		URL:     t.API + path,
		Headers: map[string]string{"Accept": "application/json"},
	})
	if err != nil {
		return err
	}
	if resp.Status < 200 || resp.Status > 299 {
		return fmt.Errorf("GET %s: status %d", path, resp.Status)
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("GET %s: decode: %w", path, err)
	}
	return nil
}

type rawIssue struct {
	Key        string                     `json:"key"`
	Properties map[string]json.RawMessage `json:"properties"`
	Fields     struct {
		Summary     string `json:"summary"`
		Description string `json:"description"`
		Status      struct {
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

// issue maps the payload to the canonical shape. The close time and reason are
// reported only for a closed issue, and a malformed resolutiondate yields ""
// rather than failing the read.
func (r rawIssue) issue() tracker.Issue {
	i := tracker.Issue{
		Number: r.Key,
		Title:  r.Fields.Summary,
		Body:   r.Fields.Description,
		State:  "open",
		Labels: append([]string{}, r.Fields.Labels...),
	}
	if raw, ok := r.Properties[dedupPropertyKey]; ok {
		var prop struct {
			Marker string `json:"marker"`
		}
		if json.Unmarshal(raw, &prop) == nil {
			i.DedupMarker = prop.Marker
		}
	}
	// statusCategory "done" is Jira's terminal category whatever the workflow
	// names its terminal status.
	if r.Fields.Status.StatusCategory.Key == "done" {
		i.State = "closed"
		i.ClosedAt = rfc3339(r.Fields.ResolutionDate)
		i.ClosedNotPlanned = slices.ContainsFunc(notPlannedResolutions, func(n string) bool {
			return strings.EqualFold(n, r.Fields.Resolution.Name)
		})
	}
	return i
}

// rfc3339 re-spells a Jira timestamp as RFC3339, the form the SDK contract
// documents, keeping any milliseconds; a malformed one yields "" rather than
// failing the read.
func rfc3339(jiraTime string) string {
	at, err := time.Parse(dateLayout, jiraTime)
	if err != nil {
		return ""
	}
	return at.Format(time.RFC3339Nano)
}

// jql is the open-issue query for the selectors: an issue matching the status
// OR the label, never a resolved one even if it still carries a stale
// dispatch label. Both selectors empty lists every open issue.
func jql(project string, q tracker.Query) string {
	s := fmt.Sprintf("project = %q", project)
	var sel []string
	if q.Status != "" {
		sel = append(sel, fmt.Sprintf("status = %q", q.Status))
	}
	if q.Label != "" {
		sel = append(sel, fmt.Sprintf("labels = %q", q.Label))
	}
	if len(sel) > 0 {
		s += " AND (" + strings.Join(sel, " OR ") + ")"
	}
	return s + " AND statusCategory != Done order by created asc"
}

type searchPayload struct {
	Issues []rawIssue `json:"issues"`
	Total  int        `json:"total"`

	// Cloud's search/jql pages by an opaque token instead of startAt/total.
	NextPageToken string `json:"nextPageToken"`
	IsLast        bool   `json:"isLast"`
}

func listIssues(t tracker.Target, q tracker.Query, limit int) ([]tracker.Issue, error) {
	if limit < 1 {
		return nil, fmt.Errorf("list issues: limit %d < 1", limit)
	}
	var raw []rawIssue
	var err error
	switch t.Dialect {
	case "cloud":
		raw, err = searchCloud(t, jql(t.Repo, q), limit)
	case "server":
		raw, err = searchServer(t, jql(t.Repo, q), limit)
	default:
		err = fmt.Errorf("list issues: unknown Jira dialect %q (want \"cloud\" or \"server\")", t.Dialect)
	}
	if err != nil {
		return nil, err
	}
	out := make([]tracker.Issue, 0, min(limit, len(raw)))
	for _, r := range raw[:min(limit, len(raw))] {
		out = append(out, r.issue())
	}
	return out, nil
}

// searchCloud walks /search/jql by nextPageToken. Cloud removed v2 /search
// (410); the v2 variant is used because it returns description as plain text
// rather than ADF, and its fields must be named or only the id comes back.
// Cloud may not honour maxResults, so the walk never counts rows to find the
// end, and it fails on a repeated token rather than spin.
func searchCloud(t tracker.Target, jql string, limit int) ([]rawIssue, error) {
	var all []rawIssue
	token := ""
	for range maxPages {
		q := url.Values{
			"jql":        {jql},
			"fields":     {"summary,description,status,labels,resolution,resolutiondate"},
			"properties": {dedupPropertyKey},
			"maxResults": {strconv.Itoa(min(limit, pageSize))},
		}
		if token != "" {
			q.Set("nextPageToken", token)
		}
		var p searchPayload
		if err := get(t, "/rest/api/2/search/jql?"+q.Encode(), &p); err != nil {
			return nil, err
		}
		all = append(all, p.Issues...)
		if !p.IsLast && p.NextPageToken != "" && p.NextPageToken == token {
			return nil, fmt.Errorf("jira search/jql returned the same nextPageToken twice; refusing to loop")
		}
		token = p.NextPageToken
		if p.IsLast || token == "" || len(all) >= limit {
			return all, nil
		}
	}
	return nil, fmt.Errorf("GET /rest/api/2/search/jql: more than %d pages", maxPages)
}

// searchServer walks v2 /search by startAt until total. It errors rather than
// return a short result when a page comes back empty before total or repeats
// the previous page, so a nil error means the result is complete.
func searchServer(t tracker.Target, jql string, limit int) ([]rawIssue, error) {
	var all []rawIssue
	var prevFirstKey string
	for range maxPages {
		// Advance by rows received, not a fixed stride: the server may cap the
		// page below maxResults.
		startAt := len(all)
		q := url.Values{
			"jql":        {jql},
			"properties": {dedupPropertyKey},
			"startAt":    {strconv.Itoa(startAt)},
			"maxResults": {strconv.Itoa(min(limit, pageSize))},
		}
		var p searchPayload
		if err := get(t, "/rest/api/2/search?"+q.Encode(), &p); err != nil {
			return nil, err
		}
		if len(p.Issues) == 0 && startAt < p.Total {
			return nil, fmt.Errorf("jira: search returned an empty page at startAt %d before total %d", startAt, p.Total)
		}
		// A server ignoring startAt replays the previous page; appending it
		// would duplicate rows until total is reached.
		if startAt > 0 && len(p.Issues) > 0 && p.Issues[0].Key == prevFirstKey {
			return nil, fmt.Errorf("jira: search repeated the page starting at %s for startAt %d", prevFirstKey, startAt)
		}
		all = append(all, p.Issues...)
		if len(p.Issues) > 0 {
			prevFirstKey = p.Issues[0].Key
		}
		if len(all) >= p.Total || len(all) >= limit {
			return all, nil
		}
	}
	return nil, fmt.Errorf("GET /rest/api/2/search: more than %d pages", maxPages)
}

func issuePath(key string) string { return "/rest/api/2/issue/" + url.PathEscape(key) }

func issue(t tracker.Target, key string) (tracker.Issue, error) {
	var raw rawIssue
	if err := get(t, issuePath(key)+"?properties="+dedupPropertyKey, &raw); err != nil {
		return tracker.Issue{}, err
	}
	return raw.issue(), nil
}

type property struct {
	Key string `json:"key"`
}

func comments(t tracker.Target, key string) ([]tracker.Comment, error) {
	var payload struct {
		Comments []struct {
			Author struct {
				DisplayName string `json:"displayName"`
			} `json:"author"`
			Created    string     `json:"created"`
			Body       string     `json:"body"`
			Properties []property `json:"properties"`
		} `json:"comments"`
	}
	if err := get(t, issuePath(key)+"/comment?expand=properties", &payload); err != nil {
		return nil, err
	}
	out := make([]tracker.Comment, len(payload.Comments))
	for i, c := range payload.Comments {
		out[i] = tracker.Comment{
			Author:    c.Author.DisplayName,
			CreatedAt: rfc3339(c.Created),
			Body:      c.Body,
			ResearchMarker: slices.ContainsFunc(c.Properties, func(p property) bool {
				return p.Key == researchPropertyKey
			}),
		}
	}
	return out, nil
}

// linkDirection names one side of a Jira link: the relationship text and
// whether the peer sits on the inward or outward end.
type linkDirection struct {
	text   string
	inward bool
}

var (
	blockedBy = linkDirection{text: blockedByLink, inward: true}
	blocking  = linkDirection{text: blocksLink}
)

// linkKeys returns the keys on dir's side of key's issuelinks whose
// relationship text matches, deduped in payload order.
func linkKeys(t tracker.Target, key string, dir linkDirection) ([]string, error) {
	var raw rawIssue
	if err := get(t, issuePath(key), &raw); err != nil {
		return nil, err
	}
	ids := []string{}
	for _, l := range raw.Fields.IssueLinks {
		peer, label := l.OutwardIssue, l.Type.Outward
		if dir.inward {
			peer, label = l.InwardIssue, l.Type.Inward
		}
		if peer != nil && label == dir.text && !slices.Contains(ids, peer.Key) {
			ids = append(ids, peer.Key)
		}
	}
	return ids, nil
}

// nativeDeps are the issues key is blocked by: inward "is blocked by" links.
func nativeDeps(t tracker.Target, key string) ([]string, error) {
	return linkKeys(t, key, blockedBy)
}

// blocks are the issues key blocks: outward "blocks" links.
func blocks(t tracker.Target, key string) ([]string, error) {
	return linkKeys(t, key, blocking)
}
