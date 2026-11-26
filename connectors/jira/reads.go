package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

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

	// countInterval is how often core re-asks Count. A JQL search carries no
	// conditional-request support, and Jira Cloud rate-limits REST calls, so
	// the probe stays infrequent.
	countInterval = 5 * time.Minute
)

// notPlannedResolutions are the resolution names that mean the work was
// declined, matched case-insensitively. Duplicate is left out on purpose: a
// duplicate finding should keep suppressing refiles.
var notPlannedResolutions = []string{"Won't Do", "Won't Fix", "Declined"}

// get sends a GET to t.API+path and decodes a 2xx JSON body into out. The host
// attaches the credential, so none is set here.
func get(t tracker.Target, path string, out any) error {
	return do(t, "GET", path, nil, out)
}

// do sends method to t.API+path, with body as JSON when non-nil, and decodes a
// 2xx JSON response into out, when there is one.
func do(t tracker.Target, method, path string, body, out any) error {
	req := connector.Request{
		Method:  method,
		URL:     t.API + path,
		Headers: map[string]string{"Accept": "application/json"},
	}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		req.Headers["Content-Type"] = "application/json"
		req.Body = string(b)
	}
	resp, err := connector.HTTPRequest(req)
	if err != nil {
		return connector.Errorf(connector.Transient, "%w", err)
	}
	if resp.Status < 200 || resp.Status > 299 {
		return connector.Errorf(connector.ClassForStatus(resp.Status), "%s %s: status %d%s", method, path, resp.Status, jiraReason(resp.Body))
	}
	// A write answers 204 with no body and passes a nil out; a read that
	// answers 200 empty must still fail to decode.
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("%s %s: decode: %w", method, path, err)
	}
	return nil
}

// maxReason caps the Jira text echoed into an error, which lands on one stderr
// or halt-event line; the body is remote-controlled and otherwise unbounded.
const maxReason = 512

// jiraReason renders Jira's own refusal ({"errorMessages":[...],"errors":
// {field:msg}}) as ": a; b" for an error tail, and "" for any other body, so an
// unknown Component in JQL reaches the operator instead of a bare status.
func jiraReason(body []byte) string {
	var e struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	msgs := slices.Clone(e.ErrorMessages)
	for _, f := range slices.Sorted(maps.Keys(e.Errors)) {
		msgs = append(msgs, f+": "+e.Errors[f])
	}
	if len(msgs) == 0 {
		return ""
	}
	reason := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp, unicode.Cf) {
			return ' '
		}
		return r
	}, strings.Join(msgs, "; "))
	if len(reason) > maxReason {
		cut := maxReason
		for !utf8.RuneStart(reason[cut]) {
			cut--
		}
		reason = reason[:cut] + "…"
	}
	return ": " + reason
}

type rawIssue struct {
	Key        string                     `json:"key"`
	Properties map[string]json.RawMessage `json:"properties"`
	Fields     struct {
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

// issue maps the payload to the canonical shape. The close time and reason are
// reported only for a closed issue, and a malformed resolutiondate yields ""
// rather than failing the read.
func (r rawIssue) issue() tracker.Issue {
	i := tracker.Issue{
		Number: r.Key,
		Title:  r.Fields.Summary,
		Body:   r.Fields.Description,
		State:  "open",
		Status: r.Fields.Status.Name,
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
	if r.Fields.Status.StatusCategory.Key == tracker.StatusCategoryDone {
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
// dispatch label. Both selectors empty lists every open issue; a route covers
// only the open issues in it, and a route kind Jira cannot honour is an error.
func jql(project string, q tracker.Query) (string, error) {
	s, err := openJQL(project, q)
	if err != nil {
		return "", err
	}
	return s + " order by created asc", nil
}

// routeJQL renders the route as one AND clause per selector. The quoting is
// what keeps a value inside the project scope; Go's %q can emit escapes JQL
// rejects (\a, \x07, \U…), so such a value fails the query instead.
func routeJQL(route []tracker.Selector) (string, error) {
	terms, err := routeTerms(route)
	if err != nil {
		return "", err
	}
	var s string
	for _, term := range terms {
		s += fmt.Sprintf(" AND (%s = %q)", term.field, term.value)
	}
	return s, nil
}

// openJQL is jql without its ORDER BY, which a count does not need.
func openJQL(project string, q tracker.Query) (string, error) {
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
	r, err := routeJQL(q.Route)
	if err != nil {
		return "", err
	}
	return s + r + " AND statusCategory != Done", nil
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
	query, err := jql(t.Repo, q)
	if err != nil {
		return nil, err
	}
	return search(t, query, limit)
}

// matchesRoute reports whether issue number is in the route: one search of the
// key ANDed with every route clause, inside the project. A route kind Jira
// cannot honour is an error, never a miss.
func matchesRoute(t tracker.Target, number string, route []tracker.Selector) (bool, error) {
	routed, err := routeJQL(route)
	if err != nil {
		return false, err
	}
	found, err := search(t, fmt.Sprintf("project = %q AND key = %q%s", t.Repo, number, routed), 1)
	if err != nil {
		return false, err
	}
	return len(found) > 0, nil
}

// labeledPage returns one page of issues in state carrying label, newest
// first, and the cursor for the next ("" at the end). A closed one carries its
// close date and not-planned reason, which core's dedup scan reads: a finding
// closed as not planned must not be refiled. One call is one HTTP request, so
// the cursor is Jira's own (cloud nextPageToken, server startAt) and no row is
// lost to truncating a page that continues; core walks the pages.
func labeledPage(t tracker.Target, label string, route []tracker.Selector, state, cursor string, limit int) ([]tracker.Issue, string, error) {
	if limit < 1 {
		return nil, "", fmt.Errorf("list labeled issues: limit %d < 1", limit)
	}
	var category string
	switch state {
	case "open":
		category = "!="
	case "closed":
		category = "="
	default:
		return nil, "", fmt.Errorf("list labeled issues: unsupported issue state %q", state)
	}
	routed, err := routeJQL(route)
	if err != nil {
		return nil, "", fmt.Errorf("list labeled issues: %w", err)
	}
	jql := fmt.Sprintf("project = %q AND labels = %q%s AND statusCategory %s Done order by created desc", t.Repo, label, routed, category)
	var raw []rawIssue
	var next string
	switch t.Dialect {
	case "cloud":
		raw, next, err = cloudPage(t, jql, limit, cursor)
	case "server":
		raw, next, err = serverPage(t, jql, limit, cursor)
	default:
		err = fmt.Errorf("list labeled issues: unknown Jira dialect %q (want \"cloud\" or \"server\")", t.Dialect)
	}
	if err != nil {
		return nil, "", err
	}
	out := make([]tracker.Issue, len(raw))
	for i, r := range raw {
		out[i] = r.issue()
	}
	return out, next, nil
}

// cloudPage fetches one /search/jql page; its next is the response's token.
func cloudPage(t tracker.Target, jql string, limit int, token string) ([]rawIssue, string, error) {
	var p searchPayload
	if err := get(t, "/rest/api/2/search/jql?"+cloudQuery(jql, limit, token).Encode(), &p); err != nil {
		return nil, "", err
	}
	if p.IsLast {
		return p.Issues, "", nil
	}
	return p.Issues, p.NextPageToken, nil
}

// serverPage fetches one /search page at the cursor "<startAt>:<firstKey>"
// (firstKey is the previous page's first issue, "" on the first page); its next
// is the offset after the rows received plus this page's first key, or "" once
// total is reached. Carrying the key lets a stateless call catch a server that
// ignores startAt and replays a page, as searchServer does.
func serverPage(t tracker.Target, jql string, limit int, cursor string) ([]rawIssue, string, error) {
	startAt, prevFirstKey := 0, ""
	if cursor != "" {
		offset, key, _ := strings.Cut(cursor, ":")
		n, err := strconv.Atoi(offset)
		if err != nil || n < 0 {
			return nil, "", fmt.Errorf("list labeled issues: malformed cursor %q", cursor)
		}
		startAt, prevFirstKey = n, key
	}
	var p searchPayload
	if err := get(t, "/rest/api/2/search?"+serverQuery(jql, limit, startAt).Encode(), &p); err != nil {
		return nil, "", err
	}
	if len(p.Issues) == 0 && startAt < p.Total {
		return nil, "", fmt.Errorf("list labeled issues: search returned an empty page at startAt %d before total %d", startAt, p.Total)
	}
	if prevFirstKey != "" && len(p.Issues) > 0 && p.Issues[0].Key == prevFirstKey {
		return nil, "", fmt.Errorf("list labeled issues: search repeated the page starting at %s for startAt %d", prevFirstKey, startAt)
	}
	if end := startAt + len(p.Issues); end < p.Total {
		return p.Issues, strconv.Itoa(end) + ":" + p.Issues[0].Key, nil
	}
	return p.Issues, "", nil
}

func cloudQuery(jql string, limit int, token string) url.Values {
	q := url.Values{
		"jql":        {jql},
		"fields":     {"summary,description,status,labels,resolution,resolutiondate"},
		"properties": {dedupPropertyKey},
		"maxResults": {strconv.Itoa(min(limit, pageSize))},
	}
	if token != "" {
		q.Set("nextPageToken", token)
	}
	return q
}

func serverQuery(jql string, limit, startAt int) url.Values {
	return url.Values{
		"jql":        {jql},
		"properties": {dedupPropertyKey},
		"startAt":    {strconv.Itoa(startAt)},
		"maxResults": {strconv.Itoa(min(limit, pageSize))},
	}
}

// search runs jql through the target's dialect and maps up to limit rows.
func search(t tracker.Target, jql string, limit int) ([]tracker.Issue, error) {
	var raw []rawIssue
	var err error
	switch t.Dialect {
	case "cloud":
		raw, err = searchCloud(t, jql, limit)
	case "server":
		raw, err = searchServer(t, jql, limit)
	default:
		err = fmt.Errorf("search: unknown Jira dialect %q (want \"cloud\" or \"server\")", t.Dialect)
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
		var p searchPayload
		if err := get(t, "/rest/api/2/search/jql?"+cloudQuery(jql, limit, token).Encode(), &p); err != nil {
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
		var p searchPayload
		if err := get(t, "/rest/api/2/search?"+serverQuery(jql, limit, startAt).Encode(), &p); err != nil {
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

// segment refuses s when it cannot stay one path segment, such as "." or "..",
// before sending; the host would refuse it and close the Connector. what names
// the value in the error.
func segment(what, s string) (string, error) {
	seg, err := connector.PathSegment(s)
	if err != nil {
		return "", fmt.Errorf("%s: %w", what, err)
	}
	return seg, nil
}

func issuePath(key string) (string, error) {
	seg, err := segment("issue key", key)
	if err != nil {
		return "", err
	}
	return "/rest/api/2/issue/" + seg, nil
}

func issue(t tracker.Target, key string) (tracker.Issue, error) {
	var raw rawIssue
	path, err := issuePath(key)
	if err != nil {
		return tracker.Issue{}, err
	}
	if err := get(t, path+"?properties="+dedupPropertyKey, &raw); err != nil {
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
	path, err := issuePath(key)
	if err != nil {
		return nil, err
	}
	if err := get(t, path+"/comment?expand=properties", &payload); err != nil {
		return nil, err
	}
	out := make([]tracker.Comment, len(payload.Comments))
	for i, c := range payload.Comments {
		author := c.Author.DisplayName
		if author == "" {
			// Server/Data Center allows anonymous comments; core refuses an
			// empty author, and "Anonymous" is what Jira's own UI shows.
			author = "Anonymous"
		}
		out[i] = tracker.Comment{
			Author:    author,
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
	path, err := issuePath(key)
	if err != nil {
		return nil, err
	}
	if err := get(t, path, &raw); err != nil {
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
