// Command tracker is the fixture tracker Connector: it registers tracker.Reader
// and translates the read primitives to a forgejo-shaped REST backend over
// http_request, with Target.API as the base URL (the scripted backend's port is
// dynamic). Link-time mode (-ldflags=-X=main.mode=<mode>) selects a variant:
// "" is faithful and read-only; "violate" answers Issue with a wrong title,
// which the read contract must catch; "writes" also registers tracker.Writer
// (transition and editLabels, by label name) so the full tracker contract runs;
// "writes-violate" is "writes" with an editLabels that ignores removals, which
// the full contract must catch as a stale label; "dialect" is faithful and
// read-only but probes GET {Target.API}/repos/{Target.Repo}/dialect before each
// read and fails on a non-2xx, which only a backend scripted to answer it
// satisfies; "hostile" is faithful and read-only plus tracker.fileIssue (POST
// {API}/repos/{Repo}/issues, answering its html_url verbatim) and
// tracker.listLabels (GET {API}/repos/{Repo}/labels), and passes a comment's
// author_association through unvetted, so a scripted backend can make it relay
// answers core must refuse.
package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

var mode string

func init() {
	if mode == "writes" || mode == "writes-violate" {
		tracker.RegisterWrites(tracker.Writer{Transition: transition, EditLabels: editLabels})
	}
	if mode == "hostile" {
		tracker.RegisterWrites(tracker.Writer{FileIssue: fileIssue, ListLabels: listLabels})
	}
	tracker.Register(tracker.Reader{
		ListIssues:    listIssues,
		Issue:         issue,
		Comments:      comments,
		NativeDeps:    nativeDeps,
		Count:         count,
		CountInterval: time.Second,
	})
}

// send makes one request and returns its 2xx answer. A transport failure is
// Transient, so core may retry it; a non-2xx status is classified by status.
func send(method, u, body string) ([]byte, error) {
	resp, err := connector.HTTPRequest(connector.Request{Method: method, URL: u, Body: body})
	if err != nil {
		return nil, connector.Errorf(connector.Transient, "tracker backend unreachable: %w", err)
	}
	if resp.Status/100 != 2 {
		return nil, connector.Errorf(connector.ClassForStatus(resp.Status), "tracker backend answered status %d", resp.Status)
	}
	return resp.Body, nil
}

// get decodes a 2xx JSON answer into v.
func get(t tracker.Target, u string, v any) error {
	if mode == "dialect" {
		if _, err := send(http.MethodGet, t.API+"/repos/"+t.Repo+"/dialect", ""); err != nil {
			return err
		}
	}
	body, err := send(http.MethodGet, u, "")
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}

// transition answers unmapped when the mapping names no status for the state:
// the backend has no statuses, so core falls back to editLabels.
func transition(_ tracker.Target, _ string, r tracker.TransitionRequest) (tracker.TransitionResult, error) {
	status := r.Mapping[r.State].Status
	if status == "" {
		return tracker.TransitionResult{Outcome: tracker.TransitionUnmapped}, nil
	}
	return tracker.TransitionResult{Outcome: tracker.TransitionUnavailable, Status: status, Offered: []string{}}, nil
}

func editLabels(t tracker.Target, number string, add, remove []string) error {
	base := issuesURL(t) + "/" + number + "/labels"
	if len(add) > 0 {
		body, err := json.Marshal(map[string][]string{"labels": add})
		if err != nil {
			return err
		}
		if _, err := send(http.MethodPost, base, string(body)); err != nil {
			return err
		}
	}
	if mode == "writes-violate" {
		return nil
	}
	for _, l := range remove {
		// The backend answers 404 for a label the issue does not carry, which is
		// the state a removal asks for.
		resp, err := connector.HTTPRequest(connector.Request{Method: http.MethodDelete, URL: base + "/" + url.PathEscape(l)})
		if err != nil {
			return connector.Errorf(connector.Transient, "tracker backend unreachable: %w", err)
		}
		if resp.Status/100 != 2 && resp.Status != http.StatusNotFound {
			return connector.Errorf(connector.ClassForStatus(resp.Status), "tracker backend answered status %d", resp.Status)
		}
	}
	return nil
}

func fileIssue(t tracker.Target, r tracker.IssueRequest) (string, error) {
	body, err := json.Marshal(map[string]any{"title": r.Title, "body": r.Body, "labels": r.Labels})
	if err != nil {
		return "", err
	}
	resp, err := send(http.MethodPost, issuesURL(t), string(body))
	if err != nil {
		return "", err
	}
	var created struct {
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		return "", err
	}
	return created.HTMLURL, nil
}

func listLabels(t tracker.Target) ([]string, error) {
	var rest []struct {
		Name string `json:"name"`
	}
	if err := get(t, t.API+"/repos/"+t.Repo+"/labels", &rest); err != nil {
		return nil, err
	}
	out := make([]string, len(rest))
	for i, l := range rest {
		out[i] = l.Name
	}
	return out, nil
}

func issuesURL(t tracker.Target) string { return t.API + "/repos/" + t.Repo + "/issues" }

type restIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

func (r restIssue) issue() tracker.Issue {
	i := tracker.Issue{Number: strconv.Itoa(r.Number), Title: r.Title, Body: r.Body, State: r.State, Labels: []string{}}
	for _, l := range r.Labels {
		i.Labels = append(i.Labels, l.Name)
	}
	return i
}

func listIssues(t tracker.Target, q tracker.Query, limit int) ([]tracker.Issue, error) {
	labels, err := tracker.RouteLabels(q.Label, q.Route)
	if err != nil {
		return nil, err
	}
	v := url.Values{"state": {"open"}, "limit": {strconv.Itoa(limit)}}
	if len(labels) > 0 {
		v.Set("labels", strings.Join(labels, ","))
	}
	var rest []restIssue
	if err := get(t, issuesURL(t)+"?"+v.Encode(), &rest); err != nil {
		return nil, err
	}
	out := make([]tracker.Issue, len(rest))
	for i, r := range rest {
		out[i] = r.issue()
	}
	return out, nil
}

func issue(t tracker.Target, number string) (tracker.Issue, error) {
	var r restIssue
	if err := get(t, issuesURL(t)+"/"+number, &r); err != nil {
		return tracker.Issue{}, err
	}
	i := r.issue()
	if mode == "violate" {
		i.Title = "violated"
	}
	return i, nil
}

func comments(t tracker.Target, number string) ([]tracker.Comment, error) {
	var rest []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		CreatedAt   string `json:"created_at"`
		Body        string `json:"body"`
		Association string `json:"author_association"`
	}
	if err := get(t, issuesURL(t)+"/"+number+"/comments", &rest); err != nil {
		return nil, err
	}
	out := make([]tracker.Comment, len(rest))
	for i, c := range rest {
		out[i] = tracker.Comment{Author: c.User.Login, CreatedAt: c.CreatedAt, Body: c.Body, Association: c.Association}
	}
	return out, nil
}

func nativeDeps(t tracker.Target, number string) ([]string, error) {
	var rest []struct {
		Number int `json:"number"`
	}
	if err := get(t, issuesURL(t)+"/"+number+"/dependencies", &rest); err != nil {
		return nil, err
	}
	out := make([]string, len(rest))
	for i, d := range rest {
		out[i] = strconv.Itoa(d.Number)
	}
	return out, nil
}

// count is the open issues ListIssues would return, walked rather than read
// from a total: the backend serves no count of its own.
func count(t tracker.Target, q tracker.Query, limit int, _ bool) (int, error) {
	issues, err := listIssues(t, q, limit)
	return len(issues), err
}

func main() {}
