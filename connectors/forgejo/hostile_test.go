package main

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
	"spindrift.dev/sdk/tracker"
)

var (
	prPath      = regexp.MustCompile(`/pulls/[^/]+$`)
	issuePathRE = regexp.MustCompile(`/issues/[^/]+$`)
)

// forgejoAnswer is a plausible Forgejo that carries v back wherever the
// Connector reads a name or sha from it: labels, a PR's head and base, a status
// sha. A hostile value therefore also reaches the requests the Connector builds
// from what the forge said, not only from what the caller sent.
func forgejoAnswer(v string) func(connector.Request) (connector.Response, error) {
	q := func(s string) string { return fmt.Sprintf("%q", s) }
	return func(req connector.Request) (connector.Response, error) {
		u, err := url.Parse(req.URL)
		if err != nil {
			return connector.Response{}, err
		}
		p, get := u.Path, req.Method == "GET"
		body := `{}`
		var header map[string]string
		switch {
		case strings.Contains(p, "/actions/jobs/") && strings.HasSuffix(p, "/logs"):
			body = "log line"
		case strings.HasSuffix(p, "/actions/runs"):
			body = `{"workflow_runs":[{"id":1}]}`
		case strings.HasSuffix(p, "/jobs"):
			body = `[{"id":1}]`
		case strings.HasSuffix(p, "/statuses"):
			body = `[{"id":1,"context":"ci","state":"success","target_url":"/o/r/actions/runs/3/jobs/0"}]`
		case strings.HasSuffix(p, "/status"):
			body = `{"state":"success","total_count":1}`
		case get && strings.HasSuffix(p, "/labels"):
			body = `[{"id":1,"name":` + q(v) + `}]`
		case get && strings.HasSuffix(p, "/pulls"):
			body = `[{"number":1,"state":"open","head":{"ref":` + q(v) + `,"sha":"s"}}]`
		case get && strings.HasSuffix(p, "/files"):
			body = `[{"filename":"a.go"}]`
		case get && prPath.MatchString(p):
			body = `{"number":1,"state":"open","head":{"ref":` + q(v) + `,"sha":` + q(v) + `},"base":{"ref":` + q(v) + `}}`
		case get && strings.Contains(p, "/compare/"):
			body = `{"total_commits":2}`
		case get && strings.Contains(p, "/branches/"):
			body = `{"protected":true}`
		case get && strings.HasSuffix(p, "/issues"):
			body = `[{"number":1,"title":"t","state":"open","labels":[{"name":` + q(v) + `}]}]`
			header = map[string]string{"X-Total-Count": "1"}
		case get && issuePathRE.MatchString(p):
			body = `{"number":1,"title":"t","state":"open","labels":[{"name":` + q(v) + `}]}`
		case get && (strings.HasSuffix(p, "/comments") || strings.HasSuffix(p, "/dependencies") || strings.HasSuffix(p, "/blocks")):
			body = `[{"number":1,"user":{"login":"u"},"body":"b"}]`
		case req.Method == "POST" && strings.HasSuffix(p, "/pulls"):
			body = `{"number":1,"state":"open","html_url":"https://forge.test/o/r/pulls/1"}`
		case req.Method == "POST" && strings.HasSuffix(p, "/issues"):
			body = `{"number":1,"html_url":"https://forge.test/o/r/issues/1"}`
		case get && p == "/api/v1/repos/o/r":
			body = `{"full_name":"o/r","allow_rebase":true}`
		}
		return connector.Response{Status: 200, Body: []byte(body), Header: header}, nil
	}
}

// TestHostileInputs runs the SDK's hostile corpus through every method that
// takes an id, PR number, label or branch name. Serve fails the test on any
// request outside a declared Route, so a pass means each input was refused
// before sending or stayed inside one.
func TestHostileInputs(t *testing.T) {
	label := func(v string) []tracker.Selector { return []tracker.Selector{{Kind: "label", Value: v}} }
	methods := []struct {
		name string
		call func(in string) error
	}{
		{"forge.prsForBranch", func(in string) error { _, err := prsForBranch(ftgt, in); return err }},
		{"forge.pr", func(in string) error { _, err := pr(ftgt, in); return err }},
		{"forge.checks", func(in string) error { _, err := checks(ftgt, in); return err }},
		{"forge.jobLog", func(in string) error { _, err := jobLog(ftgt, in); return err }},
		{"forge.branchExists", func(in string) error { _, err := branchExists(ftgt, in); return err }},
		{"forge.deleteBranch", func(in string) error { return deleteBranch(ftgt, in) }},
		{"forge.branchProtected", func(in string) error { _, err := branchProtected(ftgt, in); return err }},
		{"forge.openPR head", func(in string) error {
			_, err := open(ftgt, forge.NewPR{Title: "t", Base: "main", Head: in})
			return err
		}},
		{"forge.openPR base", func(in string) error {
			_, err := open(ftgt, forge.NewPR{Title: "t", Base: in, Head: "h"})
			return err
		}},
		{"forge.behind", func(in string) error { _, err := behind(ftgt, in); return err }},
		{"forge.prFiles", func(in string) error { _, err := prFiles(ftgt, in); return err }},
		{"forge.setDraft", func(in string) error { return setDraft(ftgt, in, true) }},
		{"forge.comment", func(in string) error { return comment(ftgt, in, "b") }},
		{"forge.merge", func(in string) error { return merge(ftgt, in) }},
		{"forge.enqueueAutoMerge", func(in string) error { return enqueueAutoMerge(ftgt, in) }},

		{"tracker.issue", func(in string) error { _, err := issue(ttgt, in); return err }},
		{"tracker.comments", func(in string) error { _, err := comments(ttgt, in); return err }},
		{"tracker.nativeDeps", func(in string) error { _, err := nativeDeps(ttgt, in); return err }},
		{"tracker.blocks", func(in string) error { _, err := blocks(ttgt, in); return err }},
		{"tracker.matchesRoute number", func(in string) error { _, err := matchesRoute(ttgt, in, label("l")); return err }},
		{"tracker.matchesRoute label", func(in string) error { _, err := matchesRoute(ttgt, "1", label(in)); return err }},
		{"tracker.listIssues label", func(in string) error {
			_, err := listIssues(ttgt, tracker.Query{Label: in}, 10)
			return err
		}},
		{"tracker.listIssues route", func(in string) error {
			_, err := listIssues(ttgt, tracker.Query{Route: label(in)}, 10)
			return err
		}},
		{"tracker.count label", func(in string) error {
			_, err := count(ttgt, tracker.Query{Label: in}, 10, false)
			return err
		}},
		{"tracker.count route", func(in string) error {
			_, err := count(ttgt, tracker.Query{Route: label(in)}, 10, false)
			return err
		}},
		{"tracker.labeled label", func(in string) error {
			_, _, err := labeledPage(ttgt, in, nil, "open", "", 10)
			return err
		}},
		{"tracker.labeled route", func(in string) error {
			_, _, err := labeledPage(ttgt, "", label(in), "closed", "", 10)
			return err
		}},
		{"tracker.labeled cursor", func(in string) error {
			_, _, err := labeledPage(ttgt, "l", nil, "open", in, 10)
			return err
		}},
		{"tracker.labeled state", func(in string) error {
			_, _, err := labeledPage(ttgt, "l", nil, in, "", 10)
			return err
		}},
		{"tracker.searchIssues label", func(in string) error { _, err := search(ttgt, "t", "open", in, 10); return err }},
		{"tracker.searchIssues text", func(in string) error { _, err := search(ttgt, in, "open", "", 10); return err }},
		{"tracker.transition", func(in string) error {
			_, err := transition(ttgt, in, tracker.TransitionRequest{State: in})
			return err
		}},
		{"tracker.editLabels number", func(in string) error { return editLabels(ttgt, in, []string{"a"}, []string{"b"}) }},
		{"tracker.editLabels add", func(in string) error { return editLabels(ttgt, "1", []string{in}, nil) }},
		{"tracker.editLabels remove", func(in string) error { return editLabels(ttgt, "1", nil, []string{in}) }},
		{"tracker.comment", func(in string) error { return issueComment(ttgt, in, tracker.CommentRequest{Body: "b"}) }},
		{"tracker.fileIssue labels", func(in string) error {
			_, err := fileIssue(ttgt, tracker.IssueRequest{Title: "t", Labels: []string{in}})
			return err
		}},
		{"tracker.fileIssue route", func(in string) error {
			_, err := fileIssue(ttgt, tracker.IssueRequest{Title: "t", Route: label(in)})
			return err
		}},
		{"tracker.closeIssue", func(in string) error { return closeIssue(ttgt, in) }},
		{"tracker.createLabel name", func(in string) error { return createLabel(ttgt, in, "d", "ffffff") }},
		{"tracker.createLabel color", func(in string) error { return createLabel(ttgt, "n", "d", in) }},
	}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			for i, in := range connector.HostileInputs() {
				t.Run(fmt.Sprintf("input-%d", i), func(t *testing.T) {
					host.Serve(t, forgejoAnswer(in))
					_ = m.call(in)
				})
			}
		})
	}
}

// TestDeclaredCapabilitiesAnswer calls every method the registered
// capabilities require, through the SDK's dispatcher, and needs a real result.
func TestDeclaredCapabilitiesAnswer(t *testing.T) {
	ft := forge.Target{API: ftgt.API, Repo: ftgt.Repo}
	tt := tracker.Target{API: ttgt.API, Repo: ttgt.Repo}
	answer := forgejoAnswer("feat")
	route := []tracker.Selector{{Kind: "label", Value: "feat"}}
	type obj = map[string]any
	samples := map[string]connector.Sample{
		"forge.prsForBranch":     {Params: obj{"target": ft, "branch": "feat"}, Answer: answer},
		"forge.pr":               {Params: obj{"target": ft, "number": "1"}, Answer: answer},
		"forge.checks":           {Params: obj{"target": ft, "number": "1"}, Answer: answer},
		"forge.branchExists":     {Params: obj{"target": ft, "branch": "feat"}, Answer: answer},
		"forge.deleteBranch":     {Params: obj{"target": ft, "branch": "feat"}, Answer: answer},
		"forge.probe":            {Params: obj{"target": ft}, Answer: answer},
		"forge.behind":           {Params: obj{"target": ft, "number": "1"}, Answer: answer},
		"forge.prFiles":          {Params: obj{"target": ft, "number": "1"}, Answer: answer},
		"forge.setDraft":         {Params: obj{"target": ft, "number": "1", "draft": true}, Answer: answer},
		"forge.comment":          {Params: obj{"target": ft, "number": "1", "body": "b"}, Answer: answer},
		"forge.merge":            {Params: obj{"target": ft, "number": "1"}, Answer: answer},
		"forge.openPR":           {Params: obj{"target": ft, "pr": forge.NewPR{Title: "t", Base: "main", Head: "feat"}}, Answer: answer},
		"forge.autoMergeAllowed": {Params: obj{"target": ft}, Answer: answer},
		"forge.enqueueAutoMerge": {Params: obj{"target": ft, "number": "1"}, Answer: answer},
		"forge.branchProtected":  {Params: obj{"target": ft, "branch": "feat"}, Answer: answer},

		"tracker.issue":         {Params: obj{"target": tt, "number": "1"}, Answer: answer},
		"tracker.listIssues":    {Params: obj{"target": tt, "label": "feat", "limit": 10}, Answer: answer},
		"tracker.comments":      {Params: obj{"target": tt, "number": "1"}, Answer: answer},
		"tracker.nativeDeps":    {Params: obj{"target": tt, "number": "1"}, Answer: answer},
		"tracker.blocks":        {Params: obj{"target": tt, "number": "1"}, Answer: answer},
		"tracker.probe":         {Params: obj{"target": tt}, Answer: answer},
		"tracker.count":         {Params: obj{"target": tt, "label": "feat", "limit": 10}, Answer: answer},
		"tracker.countInterval": {},
		"tracker.searchIssues":  {Params: obj{"target": tt, "text": "t", "state": "open", "label": "feat", "limit": 10}, Answer: answer},
		"tracker.labeled":       {Params: obj{"target": tt, "label": "feat", "state": "open", "limit": 10, "route": route}, Answer: answer},
		"tracker.transition":    {Params: obj{"target": tt, "number": "1", "state": "done", "mapping": obj{}}},
		"tracker.editLabels":    {Params: obj{"target": tt, "number": "1", "add": []string{"a"}, "remove": []string{"b"}}, Answer: answer},
		"tracker.comment":       {Params: obj{"target": tt, "number": "1", "body": "b", "research": false}, Answer: answer},
		"tracker.fileIssue":     {Params: obj{"target": tt, "title": "t", "body": "b", "labels": []string{"feat"}}, Answer: answer},
		"tracker.closeIssue":    {Params: obj{"target": tt, "number": "1"}, Answer: answer},
		"tracker.matchesRoute":  {Params: obj{"target": tt, "number": "1", "route": route}, Answer: answer},
		"tracker.createLabel":   {Params: obj{"target": tt, "name": "n", "description": "d", "color": "ffffff"}, Answer: answer},
		"tracker.listLabels":    {Params: obj{"target": tt}, Answer: answer},
	}
	host.CheckDeclared(t, samples)
}
