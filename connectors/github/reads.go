package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

const (
	pageSize = 100
	// maxPages bounds every paging loop; a longer list fails rather than truncates.
	maxPages = 50
)

// send is the HTTP transport; tests swap it because connector.HTTPRequest
// fails outside wasip1.
var send = connector.HTTPRequest

// request sends one request to api+path. The host attaches the credential, so
// none is set here. The status is the caller's to judge.
func request(method, api, path string, body []byte) (connector.Response, error) {
	return requestWith(method, api, path, body, nil)
}

// requestWith is request plus extra headers, such as If-None-Match.
func requestWith(method, api, path string, body []byte, extra map[string]string) (connector.Response, error) {
	headers := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
	for k, v := range extra {
		headers[k] = v
	}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}
	return send(connector.Request{Method: method, URL: api + path, Headers: headers, Body: string(body)})
}

// statusError is a non-2xx answer, carrying the class core retries on.
func statusError(method, path string, status int) error {
	return connector.Errorf(connector.ClassForStatus(status), "%s %s: status %d", method, path, status)
}

func is2xx(status int) bool { return status >= 200 && status <= 299 }

// fetch is request returning a 2xx body.
func fetch(method, api, path string, body []byte) ([]byte, error) {
	resp, err := request(method, api, path, body)
	if err != nil {
		return nil, err
	}
	if !is2xx(resp.Status) {
		return nil, statusError(method, path, resp.Status)
	}
	return resp.Body, nil
}

// call is fetch plus decoding the JSON body into out.
func call(method, api, path string, body []byte, out any) error {
	b, err := fetch(method, api, path, body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s %s: decode: %w", method, path, err)
	}
	return nil
}

func get(t tracker.Target, path string, out any) error {
	return call("GET", t.API, path, nil, out)
}

type rawIssue struct {
	Number int     `json:"number"`
	Title  string  `json:"title"`
	Body   *string `json:"body"`
	State  string  `json:"state"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest json.RawMessage `json:"pull_request"`
	// StateReason and ClosedAt are null on an open issue, and ClosedAt may be
	// null on a closed one; only labeled reads them.
	StateReason *string `json:"state_reason"`
	ClosedAt    *string `json:"closed_at"`
}

func (r rawIssue) issue() tracker.Issue {
	i := tracker.Issue{
		Number: strconv.Itoa(r.Number),
		Title:  r.Title,
		Body:   deref(r.Body),
		State:  r.State,
		Labels: []string{},
	}
	for _, l := range r.Labels {
		i.Labels = append(i.Labels, l.Name)
	}
	return i
}

// deref reads a nullable JSON string; GitHub sends null for an empty body.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func repoPath(t tracker.Target) string { return "/repos/" + t.Repo + "/issues" }

// numberPath is the request path of one issue. The number goes into the path
// raw, so anything but a positive decimal integer is refused.
func numberPath(t tracker.Target, number string) (string, error) {
	if n, err := strconv.Atoi(number); err != nil || n < 1 || strconv.Itoa(n) != number {
		return "", fmt.Errorf("issue number %q is not a positive integer", number)
	}
	return repoPath(t) + "/" + number, nil
}

// pages walks a paginated list: path must already carry a query string. visit
// returns true to stop early; a short page ends the walk. Exceeding maxPages
// is an error so a runaway list never truncates silently.
func pages[T any](api, path string, visit func([]T) bool) error {
	return pagesOf(api, path, func(raw []T) int { return len(raw) }, visit)
}

// pagesOf is pages for a response that wraps its list: size counts the items
// of one decoded page, and a page shorter than pageSize ends the walk.
func pagesOf[B any](api, path string, size func(B) int, visit func(B) bool) error {
	for page := 1; page <= maxPages; page++ {
		var raw B
		if err := call("GET", api, path+"&page="+strconv.Itoa(page), nil, &raw); err != nil {
			return err
		}
		if visit(raw) || size(raw) < pageSize {
			return nil
		}
	}
	return fmt.Errorf("GET %s: more than %d pages", path, maxPages)
}

// listQuery selects the open issues q names, oldest first; count sizes the
// same set through its own update-ordered countQuery.
func listQuery(q tracker.Query) string {
	s := "?state=open&sort=created&direction=asc&per_page=" + strconv.Itoa(pageSize)
	if q.Label != "" {
		s += "&labels=" + url.QueryEscape(q.Label)
	}
	return s
}

func listIssues(t tracker.Target, query tracker.Query, limit int) ([]tracker.Issue, error) {
	if limit < 1 {
		return nil, fmt.Errorf("list issues: limit %d < 1", limit)
	}
	var out []tracker.Issue
	err := pages(t.API, repoPath(t)+listQuery(query), func(raw []rawIssue) bool {
		for _, r := range raw {
			// The REST issues list includes pull requests; `gh issue list` does not.
			if r.PullRequest != nil {
				continue
			}
			// Matches `gh issue list --json number,title,labels`: no body or state.
			i := r.issue()
			i.Body, i.State = "", ""
			out = append(out, i)
		}
		return len(out) >= limit
	})
	if err != nil {
		return nil, err
	}
	return out[:min(limit, len(out))], nil
}

func issue(t tracker.Target, number string) (tracker.Issue, error) {
	path, err := numberPath(t, number)
	if err != nil {
		return tracker.Issue{}, err
	}
	var raw rawIssue
	if err := get(t, path, &raw); err != nil {
		return tracker.Issue{}, err
	}
	// REST serves pull requests under /issues/{n}; `gh issue view` does not.
	if raw.PullRequest != nil {
		return tracker.Issue{}, fmt.Errorf("#%s is a pull request, not an issue", number)
	}
	return raw.issue(), nil
}

func nativeDeps(t tracker.Target, number string) ([]string, error) {
	return dependencies(t, number, "blocked_by")
}

// blocks lists the issues number blocks, the reverse of nativeDeps.
func blocks(t tracker.Target, number string) ([]string, error) {
	return dependencies(t, number, "blocking")
}

// dependencies reads number's issue-dependency relationships in one direction,
// "blocked_by" or "blocking", in the API's order.
func dependencies(t tracker.Target, number, direction string) ([]string, error) {
	base, err := numberPath(t, number)
	if err != nil {
		return nil, err
	}
	type rawDep struct {
		Number int `json:"number"`
	}
	ids := []string{}
	path := fmt.Sprintf("%s/dependencies/%s?per_page=%d", base, direction, pageSize)
	err = pages(t.API, path, func(raw []rawDep) bool {
		for _, r := range raw {
			ids = append(ids, strconv.Itoa(r.Number))
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// removedLabels lists the labels removed from issue number, oldest removal
// first, from its timeline. Events with no label (a label deleted from the
// repository leaves a null) name nothing and are skipped.
func removedLabels(t tracker.Target, number string) ([]string, error) {
	base, err := numberPath(t, number)
	if err != nil {
		return nil, err
	}
	type event struct {
		Event string `json:"event"`
		Label *struct {
			Name string `json:"name"`
		} `json:"label"`
	}
	names := []string{}
	err = pages(t.API, fmt.Sprintf("%s/timeline?per_page=%d", base, pageSize), func(raw []event) bool {
		for _, e := range raw {
			if e.Event == "unlabeled" && e.Label != nil {
				names = append(names, e.Label.Name)
			}
		}
		return false
	})
	if err != nil {
		return nil, err
	}
	return names, nil
}

type searchPage struct {
	Items []rawIssue `json:"items"`
}

// search returns up to limit issues whose title or body matches text. text
// comes from an untrusted Box and lands in a GitHub search string, where a bare
// qualifier such as "repo:other/x" or "is:pr" would widen the scope, so each
// term is double-quoted into a literal phrase; text or a label that could
// break out of the quoting is rejected rather than escaped, a backslash
// included, since the search parser may read `\"` as an escaped quote.
func search(t tracker.Target, text, state, label string, limit int) ([]tracker.Issue, error) {
	if limit < 1 {
		return nil, fmt.Errorf("search issues: limit %d < 1", limit)
	}
	if state != "open" && state != "closed" {
		return nil, fmt.Errorf("search issues: unsupported issue state %q", state)
	}
	unsafe := func(r rune) bool { return r == '"' || r == '\\' || unicode.IsControl(r) }
	if strings.ContainsFunc(text, unsafe) || strings.ContainsFunc(label, unsafe) {
		return nil, fmt.Errorf("search issues: text and label must not contain a double quote, backslash or control character")
	}
	terms := strings.Fields(text)
	if len(terms) == 0 {
		return nil, fmt.Errorf("search issues: search text is empty")
	}
	for i, term := range terms {
		terms[i] = `"` + term + `"`
	}
	q := "repo:" + t.Repo + " is:issue state:" + state
	if label != "" {
		q += ` label:"` + label + `"`
	}
	q += " " + strings.Join(terms, " ") + " in:title,body"
	path := fmt.Sprintf("/search/issues?q=%s&per_page=%d", url.QueryEscape(q), pageSize)
	var out []tracker.Issue
	err := pagesOf(t.API, path, func(p searchPage) int { return len(p.Items) }, func(p searchPage) bool {
		for _, r := range p.Items {
			if r.PullRequest != nil {
				continue
			}
			// Matches `gh issue list --json number,title,state,labels`: no body.
			i := r.issue()
			i.Body = ""
			out = append(out, i)
		}
		return len(out) >= limit
	})
	if err != nil {
		return nil, err
	}
	return out[:min(limit, len(out))], nil
}

// commentsQuery reads one page of an issue's comments, oldest first. GraphQL
// is the only API that reports isMinimized and minimizedReason, which core
// drops comments on.
const commentsQuery = `query($owner:String!,$repo:String!,$number:Int!,$after:String){repository(owner:$owner,name:$repo){issue(number:$number){comments(first:100,after:$after){pageInfo{hasNextPage endCursor} nodes{author{login} createdAt body isMinimized minimizedReason authorAssociation}}}}}`

// commentsOf returns every comment of issue number, oldest first, as `gh issue
// view --json comments` does: the body is untouched, the research marker stays
// in it, and a deleted author is the empty login. A thread longer than maxPages
// pages fails rather than truncates.
func commentsOf(t tracker.Target, number string) ([]tracker.Comment, error) {
	n, err := parseNumber(number)
	if err != nil {
		return nil, err
	}
	owner, name, err := splitRepo(t.Repo)
	if err != nil {
		return nil, err
	}
	comments := []tracker.Comment{}
	var after *string
	for range maxPages {
		var data struct {
			Repository *struct {
				Issue *struct {
					Comments struct {
						PageInfo struct {
							HasNextPage bool   `json:"hasNextPage"`
							EndCursor   string `json:"endCursor"`
						} `json:"pageInfo"`
						Nodes []struct {
							Author *struct {
								Login string `json:"login"`
							} `json:"author"`
							CreatedAt         string  `json:"createdAt"`
							Body              string  `json:"body"`
							IsMinimized       bool    `json:"isMinimized"`
							MinimizedReason   *string `json:"minimizedReason"`
							AuthorAssociation string  `json:"authorAssociation"`
						} `json:"nodes"`
					} `json:"comments"`
				} `json:"issue"`
			} `json:"repository"`
		}
		vars := map[string]any{"owner": owner, "repo": name, "number": n, "after": after}
		if err := graphql(t.API, commentsQuery, vars, &data); err != nil {
			return nil, err
		}
		if data.Repository == nil || data.Repository.Issue == nil {
			return nil, fmt.Errorf("POST /graphql: no issue %s#%s", t.Repo, number)
		}
		page := data.Repository.Issue.Comments
		for _, c := range page.Nodes {
			out := tracker.Comment{
				CreatedAt:       c.CreatedAt,
				Body:            c.Body,
				Minimized:       c.IsMinimized,
				MinimizedReason: deref(c.MinimizedReason),
				Association:     c.AuthorAssociation,
			}
			if c.Author != nil {
				out.Author = c.Author.Login
			}
			comments = append(comments, out)
		}
		if !page.PageInfo.HasNextPage {
			return comments, nil
		}
		cursor := page.PageInfo.EndCursor
		after = &cursor
	}
	return nil, fmt.Errorf("POST /graphql: more than %d pages of comments on %s#%s", maxPages, t.Repo, number)
}

// labeledPage returns one REST page of the issues in state carrying label,
// newest first, with the close date and not-planned reason core's dedup scan
// reads. The cursor is GitHub's page number at per_page=min(limit, pageSize)
// ("" is page 1), so a walk must keep limit constant. next is set while the
// raw page was full, so one call is one request; a page may hold fewer issues
// because pull requests are skipped. Unlike the list reads, the body stays.
func labeledPage(t tracker.Target, label string, route []tracker.Selector, state, cursor string, limit int) ([]tracker.Issue, string, error) {
	if len(route) > 0 {
		// No route kind is declared for this tracker; narrowing nothing would
		// hand back the whole scope as one route's findings.
		return nil, "", fmt.Errorf("list labeled issues: cannot narrow by route")
	}
	if limit < 1 {
		return nil, "", fmt.Errorf("list labeled issues: limit %d < 1", limit)
	}
	if state != "open" && state != "closed" {
		return nil, "", fmt.Errorf("list labeled issues: unsupported issue state %q", state)
	}
	page := 1
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 1 {
			return nil, "", fmt.Errorf("list labeled issues: malformed cursor %q", cursor)
		}
		page = n
	}
	perPage := min(limit, pageSize)
	path := fmt.Sprintf("%s?state=%s&labels=%s&sort=created&direction=desc&per_page=%d&page=%d", repoPath(t), state, url.QueryEscape(label), perPage, page)
	var raw []rawIssue
	if err := call("GET", t.API, path, nil, &raw); err != nil {
		return nil, "", err
	}
	var out []tracker.Issue
	for _, r := range raw {
		if r.PullRequest != nil {
			continue
		}
		i := r.issue()
		if r.State == "closed" {
			i.ClosedAt = deref(r.ClosedAt)
			i.ClosedNotPlanned = deref(r.StateReason) == "not_planned"
		}
		out = append(out, i)
	}
	next := ""
	if len(raw) >= perPage {
		next = strconv.Itoa(page + 1)
	}
	return out, next, nil
}
