package main

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"spindrift.dev/sdk/forge"
	"spindrift.dev/sdk/tracker"
)

type rawIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	// ClosedAt is RFC3339, empty (null) for an open issue.
	ClosedAt string `json:"closed_at"`
	Labels   []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

// key is the pagination identity pageUntilEmpty compares across pages.
func (r rawIssue) key() int { return r.Number }

func (r rawIssue) issue() tracker.Issue {
	i := tracker.Issue{
		Number: strconv.Itoa(r.Number),
		Title:  r.Title,
		Body:   r.Body,
		State:  r.State,
		Labels: []string{},
	}
	for _, l := range r.Labels {
		i.Labels = append(i.Labels, l.Name)
	}
	return i
}

// issuePath is a REST path under the tracker repository; rest starts with "/".
func issuePath(t tracker.Target, rest string) string {
	return forgePath(forge.Target{API: t.API, Repo: t.Repo}, rest)
}

// numberedPath is the path of issue number's sub-resource; the number goes
// into the path raw, so anything but a positive decimal integer is refused.
func numberedPath(t tracker.Target, number, rest string) (string, error) {
	if _, err := parseNumber(number); err != nil {
		return "", err
	}
	return issuePath(t, "/issues/"+number+rest), nil
}

func issue(t tracker.Target, number string) (tracker.Issue, error) {
	path, err := numberedPath(t, number, "")
	if err != nil {
		return tracker.Issue{}, err
	}
	var raw rawIssue
	if err := call("GET", t.API, path, nil, &raw); err != nil {
		return tracker.Issue{}, err
	}
	return raw.issue(), nil
}

// matchesRoute reports whether issue number carries every route label. The
// route is validated before the read, so an unhonourable one costs no request.
func matchesRoute(t tracker.Target, number string, route []tracker.Selector) (bool, error) {
	required, err := tracker.RouteLabels("", route)
	if err != nil {
		return false, err
	}
	i, err := issue(t, number)
	if err != nil {
		return false, err
	}
	return hasAll(i, required), nil
}

// listIssues returns the open issues carrying q.Label and every route label,
// ascending by number; q.Status is ignored, Forgejo has none.
func listIssues(t tracker.Target, q tracker.Query, limit int) ([]tracker.Issue, error) {
	if limit < 1 {
		return nil, fmt.Errorf("list issues: limit %d < 1", limit)
	}
	v := url.Values{"state": {"open"}, "type": {"issues"}}
	required, err := tracker.RouteLabels(q.Label, q.Route)
	if err != nil {
		return nil, err
	}
	if len(required) > 0 {
		v.Set("labels", strings.Join(required, ","))
	}
	// An undefined route label makes Forgejo drop the filter and answer every
	// open issue, so skip the walk. Unrouted requests stay as they were.
	if len(q.Route) > 0 && !allDefined(t, required) {
		return []tracker.Issue{}, nil
	}
	raw, err := pageUntilEmpty(t.API, issuePath(t, "/issues"), v, rawIssue.key)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(raw, func(a, b rawIssue) int { return a.Number - b.Number })
	issues := []tracker.Issue{}
	for _, r := range raw {
		i := r.issue()
		// Forgejo silently drops an unresolved labels= filter and returns every
		// open issue (#3952), so every label is re-checked here.
		if !hasAll(i, required) {
			continue
		}
		issues = append(issues, i)
	}
	return issues[:min(limit, len(issues))], nil
}

// comments reads the whole thread in one request: the endpoint ignores page
// and limit (#3978). The research marker stays in the body.
func comments(t tracker.Target, number string) ([]tracker.Comment, error) {
	path, err := numberedPath(t, number, "/comments")
	if err != nil {
		return nil, err
	}
	var raw []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		CreatedAt string `json:"created_at"`
		Body      string `json:"body"`
	}
	if err := call("GET", t.API, path, nil, &raw); err != nil {
		return nil, err
	}
	out := []tracker.Comment{}
	for _, c := range raw {
		out = append(out, tracker.Comment{Author: c.User.Login, CreatedAt: c.CreatedAt, Body: c.Body})
	}
	return out, nil
}

func nativeDeps(t tracker.Target, number string) ([]string, error) {
	return links(t, number, "dependencies")
}

// blocks lists the issues number blocks, the reverse of nativeDeps.
func blocks(t tracker.Target, number string) ([]string, error) {
	return links(t, number, "blocks")
}

// links reads every page of number's native link endpoint, deduplicated in
// the API's order.
func links(t tracker.Target, number, endpoint string) ([]string, error) {
	path, err := numberedPath(t, number, "/"+endpoint)
	if err != nil {
		return nil, err
	}
	raw, err := pageUntilEmpty(t.API, path, url.Values{}, rawIssue.key)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, r := range raw {
		if id := strconv.Itoa(r.Number); !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
