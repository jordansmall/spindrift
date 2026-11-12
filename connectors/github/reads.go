package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

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

// call sends one request to api+path and decodes a 2xx JSON body into out. The
// host attaches the credential, so none is set here.
func call(method, api, path string, body []byte, out any) error {
	headers := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
	if body != nil {
		headers["Content-Type"] = "application/json"
	}
	resp, err := send(connector.Request{Method: method, URL: api + path, Headers: headers, Body: string(body)})
	if err != nil {
		return err
	}
	if resp.Status < 200 || resp.Status > 299 {
		return fmt.Errorf("%s %s: status %d", method, path, resp.Status)
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
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
func pages[T any](t tracker.Target, path string, visit func([]T) bool) error {
	for page := 1; page <= maxPages; page++ {
		var raw []T
		if err := get(t, path+"&page="+strconv.Itoa(page), &raw); err != nil {
			return err
		}
		if visit(raw) || len(raw) < pageSize {
			return nil
		}
	}
	return fmt.Errorf("GET %s: more than %d pages", path, maxPages)
}

func listIssues(t tracker.Target, label string, limit int) ([]tracker.Issue, error) {
	if limit < 1 {
		return nil, fmt.Errorf("list issues: limit %d < 1", limit)
	}
	q := "?state=open&sort=created&direction=asc&per_page=" + strconv.Itoa(pageSize)
	if label != "" {
		q += "&labels=" + url.QueryEscape(label)
	}
	var out []tracker.Issue
	err := pages(t, repoPath(t)+q, func(raw []rawIssue) bool {
		for _, r := range raw {
			// The REST issues list includes pull requests; `gh issue list` does not.
			if r.PullRequest != nil {
				continue
			}
			out = append(out, r.issue())
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
	base, err := numberPath(t, number)
	if err != nil {
		return nil, err
	}
	type rawDep struct {
		Number int `json:"number"`
	}
	ids := []string{}
	path := fmt.Sprintf("%s/dependencies/blocked_by?per_page=%d", base, pageSize)
	err = pages(t, path, func(raw []rawDep) bool {
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
