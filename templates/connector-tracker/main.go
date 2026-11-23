// Command connectorname is a tracker Connector (ADR 0073): REST calls sent
// through the host's http_request Host call, which attaches the credential.
// The scaffold answers core's read primitives against a forgejo-shaped REST
// backend, which is what the conformance suite's scripted backend serves. Make
// each function below speak your tracker's API instead, and declare the routes
// it calls in routes.nix. Register tracker.Writer for the writes core asks for
// (transition, label edits, comments) once you implement them.
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

func init() {
	tracker.Register(tracker.Reader{
		ListIssues:    listIssues,
		Issue:         issue,
		Comments:      comments,
		NativeDeps:    nativeDeps,
		Count:         count,
		CountInterval: time.Second,
	})
}

// get decodes a 2xx JSON answer into v. A transport failure is Transient, so
// core may retry it; a non-2xx status is classified by status.
func get(u string, v any) error {
	resp, err := connector.HTTPRequest(connector.Request{Method: http.MethodGet, URL: u})
	if err != nil {
		return connector.Errorf(connector.Transient, "tracker backend unreachable: %w", err)
	}
	if resp.Status/100 != 2 {
		return connector.Errorf(connector.ClassForStatus(resp.Status), "tracker backend answered status %d", resp.Status)
	}
	return json.Unmarshal(resp.Body, v)
}

// issuesURL is the issue collection of the project; Target.API is the base URL
// the host resolved from the backend row's `base` knob.
func issuesURL(t tracker.Target) string { return t.API + "/repos/" + t.Repo + "/issues" }

// restIssue is the tracker's issue JSON; replace it with your tracker's shape.
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
	if err := get(issuesURL(t)+"?"+v.Encode(), &rest); err != nil {
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
	if err := get(issuesURL(t)+"/"+number, &r); err != nil {
		return tracker.Issue{}, err
	}
	return r.issue(), nil
}

func comments(t tracker.Target, number string) ([]tracker.Comment, error) {
	var rest []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		CreatedAt string `json:"created_at"`
		Body      string `json:"body"`
	}
	if err := get(issuesURL(t)+"/"+number+"/comments", &rest); err != nil {
		return nil, err
	}
	out := make([]tracker.Comment, len(rest))
	for i, c := range rest {
		out[i] = tracker.Comment{Author: c.User.Login, CreatedAt: c.CreatedAt, Body: c.Body}
	}
	return out, nil
}

func nativeDeps(t tracker.Target, number string) ([]string, error) {
	var rest []struct {
		Number int `json:"number"`
	}
	if err := get(issuesURL(t)+"/"+number+"/dependencies", &rest); err != nil {
		return nil, err
	}
	out := make([]string, len(rest))
	for i, d := range rest {
		out[i] = strconv.Itoa(d.Number)
	}
	return out, nil
}

// count walks ListIssues, for a tracker that serves no count of its own.
func count(t tracker.Target, q tracker.Query, limit int, _ bool) (int, error) {
	issues, err := listIssues(t, q, limit)
	return len(issues), err
}

// A reactor's exports are all it does; main is never called.
func main() {}
