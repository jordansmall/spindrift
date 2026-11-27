package main

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/tracker"
)

// jiraAnswer is a plausible Jira that carries v back wherever the Connector
// reads a key, label, status, link or issue-type id from it. A hostile value
// therefore also reaches the requests the Connector builds from what Jira said
// (the createmeta issue-type id becomes a path segment), not only from what the
// caller sent.
func jiraAnswer(v string) func(connector.Request) (connector.Response, error) {
	q := func(s string) string { return fmt.Sprintf("%q", s) }
	issueJSON := `{"key":` + q(v) + `,"fields":{"summary":"s","status":{"name":` + q(v) + `,"statusCategory":{"key":"new"}},` +
		`"labels":[` + q(v) + `],"issuelinks":[{"type":{"inward":"is blocked by","outward":"blocks"},` +
		`"inwardIssue":{"key":` + q(v) + `},"outwardIssue":{"key":` + q(v) + `}}]}}`
	return func(req connector.Request) (connector.Response, error) {
		u, err := url.Parse(req.URL)
		if err != nil {
			return connector.Response{}, err
		}
		p, get := u.Path, req.Method == "GET"
		body := `{}`
		switch {
		case strings.HasSuffix(p, "/search/approximate-count"):
			body = `{"count":1}`
		case strings.HasSuffix(p, "/search/jql") || strings.HasSuffix(p, "/search"):
			body = `{"issues":[` + issueJSON + `],"total":1,"isLast":true,"nextPageToken":` + q(v) + `}`
		case strings.HasSuffix(p, "/label"):
			body = `{"values":[` + q(v) + `],"total":1,"isLast":true}`
		case strings.HasSuffix(p, "/statuses"):
			body = `[{"statuses":[{"name":` + q(v) + `,"statusCategory":{"key":` + q(v) + `}}]}]`
		case strings.HasSuffix(p, "/issuetypes"):
			body = `{"issueTypes":[{"id":` + q(v) + `,"name":` + q(v) + `}],"isLast":true}`
		case strings.Contains(p, "/issuetypes/"):
			body = `{"fields":[{"fieldId":` + q(v) + `,"name":` + q(v) + `}],"isLast":true}`
		case get && strings.HasSuffix(p, "/transitions"):
			body = `{"transitions":[{"id":` + q(v) + `,"to":{"name":` + q(v) + `},"fields":{}}]}`
		case get && strings.HasSuffix(p, "/comment"):
			body = `{"comments":[{"author":{"displayName":` + q(v) + `},"body":` + q(v) + `}]}`
		case get && strings.Contains(p, "/rest/api/2/issue/"):
			body = issueJSON
		case req.Method == "POST" && strings.HasSuffix(p, "/rest/api/2/issue"):
			body = `{"key":` + q(v) + `}`
		}
		return connector.Response{Status: 200, Body: []byte(body)}, nil
	}
}

// TestHostileInputs runs the SDK's hostile corpus through every method that
// takes a key, label, status, route or issue type, on both dialects. Serve
// fails the test on any request outside a declared Route, so a pass means each
// input was refused before sending or stayed inside one.
func TestHostileInputs(t *testing.T) {
	label := func(v string) []tracker.Selector { return []tracker.Selector{{Kind: "label", Value: v}} }
	mapTo := func(status string) map[string]tracker.StatusTarget {
		return map[string]tracker.StatusTarget{"done": {Status: status, Resolution: "Done"}}
	}
	methods := []struct {
		name string
		call func(tgt tracker.Target, in string) error
	}{
		{"issue", func(tgt tracker.Target, in string) error { _, err := issue(tgt, in); return err }},
		{"comments", func(tgt tracker.Target, in string) error { _, err := comments(tgt, in); return err }},
		{"nativeDeps", func(tgt tracker.Target, in string) error { _, err := nativeDeps(tgt, in); return err }},
		{"blocks", func(tgt tracker.Target, in string) error { _, err := blocks(tgt, in); return err }},
		{"matchesRoute number", func(tgt tracker.Target, in string) error {
			_, err := matchesRoute(tgt, in, label("l"))
			return err
		}},
		{"matchesRoute label", func(tgt tracker.Target, in string) error {
			_, err := matchesRoute(tgt, "ENG-1", label(in))
			return err
		}},
		{"listIssues label", func(tgt tracker.Target, in string) error {
			_, err := listIssues(tgt, tracker.Query{Label: in}, 10)
			return err
		}},
		{"listIssues status", func(tgt tracker.Target, in string) error {
			_, err := listIssues(tgt, tracker.Query{Status: in}, 10)
			return err
		}},
		{"listIssues route", func(tgt tracker.Target, in string) error {
			_, err := listIssues(tgt, tracker.Query{Route: label(in)}, 10)
			return err
		}},
		{"count label", func(tgt tracker.Target, in string) error {
			_, err := count(tgt, tracker.Query{Label: in}, 10, false)
			return err
		}},
		{"count status", func(tgt tracker.Target, in string) error {
			_, err := count(tgt, tracker.Query{Status: in}, 10, false)
			return err
		}},
		{"count route", func(tgt tracker.Target, in string) error {
			_, err := count(tgt, tracker.Query{Route: label(in)}, 10, false)
			return err
		}},
		{"labeledPage label", func(tgt tracker.Target, in string) error {
			_, _, err := labeledPage(tgt, in, nil, "open", "", 10)
			return err
		}},
		{"labeledPage route", func(tgt tracker.Target, in string) error {
			_, _, err := labeledPage(tgt, "l", label(in), "closed", "", 10)
			return err
		}},
		{"labeledPage cursor", func(tgt tracker.Target, in string) error {
			_, _, err := labeledPage(tgt, "l", nil, "open", in, 10)
			return err
		}},
		{"labeledPage state", func(tgt tracker.Target, in string) error {
			_, _, err := labeledPage(tgt, "l", nil, in, "", 10)
			return err
		}},
		{"probe", func(tgt tracker.Target, _ string) error { _, err := probe(tgt); return err }},
		{"statuses", func(tgt tracker.Target, _ string) error { _, err := statuses(tgt); return err }},
		{"createMeta", func(tgt tracker.Target, in string) error { _, err := createMeta(tgt, in); return err }},
		{"transition number", func(tgt tracker.Target, in string) error {
			_, err := transition(tgt, in, tracker.TransitionRequest{State: "done", Mapping: mapTo("Done")})
			return err
		}},
		{"transition status", func(tgt tracker.Target, in string) error {
			_, err := transition(tgt, "ENG-1", tracker.TransitionRequest{State: "done", Mapping: mapTo(in)})
			return err
		}},
		{"transition state", func(tgt tracker.Target, in string) error {
			_, err := transition(tgt, "ENG-1", tracker.TransitionRequest{State: in, Mapping: map[string]tracker.StatusTarget{in: {Status: in}}})
			return err
		}},
		{"editLabels number", func(tgt tracker.Target, in string) error {
			return editLabels(tgt, in, []string{"a"}, []string{"b"})
		}},
		{"editLabels add", func(tgt tracker.Target, in string) error { return editLabels(tgt, "ENG-1", []string{in}, nil) }},
		{"editLabels remove", func(tgt tracker.Target, in string) error { return editLabels(tgt, "ENG-1", nil, []string{in}) }},
		{"listLabels", func(tgt tracker.Target, _ string) error { _, err := listLabels(tgt); return err }},
		{"comment", func(tgt tracker.Target, in string) error { return comment(tgt, in, tracker.CommentRequest{Body: "b"}) }},
		{"fileIssue type", func(tgt tracker.Target, in string) error {
			_, err := fileIssue(tgt, tracker.IssueRequest{Title: "t", IssueType: in})
			return err
		}},
		{"fileIssue labels", func(tgt tracker.Target, in string) error {
			_, err := fileIssue(tgt, tracker.IssueRequest{Title: "t", IssueType: "Task", Labels: []string{in}})
			return err
		}},
		{"fileIssue route", func(tgt tracker.Target, in string) error {
			_, err := fileIssue(tgt, tracker.IssueRequest{Title: "t", IssueType: "Task", Route: label(in)})
			return err
		}},
	}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			for i, in := range connector.HostileInputs() {
				t.Run(fmt.Sprintf("input-%d", i), func(t *testing.T) {
					for _, tgt := range []tracker.Target{cloud, server} {
						host.Serve(t, jiraAnswer(in))
						_ = m.call(tgt, in)
					}
				})
			}
		})
	}
}

// TestDeclaredCapabilitiesAnswer calls every method the registered
// capabilities require, through the SDK's dispatcher, and needs a real result.
func TestDeclaredCapabilitiesAnswer(t *testing.T) {
	tt := tracker.Target{API: cloud.API, Repo: cloud.Repo, Dialect: cloud.Dialect}
	answer := jiraAnswer("ENG-1")
	route := []tracker.Selector{{Kind: "label", Value: "feat"}}
	mapping := map[string]tracker.StatusTarget{"done": {Status: "ENG-1"}}
	type obj = map[string]any
	samples := map[string]connector.Sample{
		"tracker.issue":         {Params: obj{"target": tt, "number": "ENG-1"}, Answer: answer},
		"tracker.listIssues":    {Params: obj{"target": tt, "label": "feat", "limit": 10}, Answer: answer},
		"tracker.comments":      {Params: obj{"target": tt, "number": "ENG-1"}, Answer: answer},
		"tracker.nativeDeps":    {Params: obj{"target": tt, "number": "ENG-1"}, Answer: answer},
		"tracker.blocks":        {Params: obj{"target": tt, "number": "ENG-1"}, Answer: answer},
		"tracker.probe":         {Params: obj{"target": tt}, Answer: answer},
		"tracker.statuses":      {Params: obj{"target": tt}, Answer: answer},
		"tracker.createMeta":    {Params: obj{"target": tt, "issueType": "ENG-1"}, Answer: answer},
		"tracker.count":         {Params: obj{"target": tt, "label": "feat", "limit": 10}, Answer: answer},
		"tracker.countInterval": {},
		"tracker.labeled":       {Params: obj{"target": tt, "label": "feat", "state": "open", "limit": 10, "route": route}, Answer: answer},
		"tracker.matchesRoute":  {Params: obj{"target": tt, "number": "ENG-1", "route": route}, Answer: answer},
		"tracker.transition":    {Params: obj{"target": tt, "number": "ENG-1", "state": "done", "mapping": mapping}, Answer: answer},
		"tracker.editLabels":    {Params: obj{"target": tt, "number": "ENG-1", "add": []string{"a"}, "remove": []string{"b"}}, Answer: answer},
		"tracker.listLabels":    {Params: obj{"target": tt}, Answer: answer},
		"tracker.comment":       {Params: obj{"target": tt, "number": "ENG-1", "body": "b", "research": false}, Answer: answer},
		"tracker.fileIssue":     {Params: obj{"target": tt, "title": "t", "body": "b", "issueType": "Task", "labels": []string{"feat"}}, Answer: answer},
		"tracker.createLabel":   {Params: obj{"target": tt, "name": "n", "description": "d", "color": "ffffff"}},
	}
	host.CheckDeclared(t, samples)
}
