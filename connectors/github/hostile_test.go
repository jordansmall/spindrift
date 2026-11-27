package main

import (
	"encoding/json"
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
	pullPathRE  = regexp.MustCompile(`/pulls/[^/]+$`)
	issuePathRE = regexp.MustCompile(`/issues/[^/]+$`)
)

// githubAnswer is a plausible GitHub that carries v back wherever the Connector
// reads a name, ref or id from it: labels, a PR's head and base, a node id, a
// check's name and URL. A hostile value therefore also reaches the requests the
// Connector builds from what the forge said, not only from what the caller sent.
func githubAnswer(v string) func(connector.Request) (connector.Response, error) {
	q := func(s string) string { return fmt.Sprintf("%q", s) }
	const prRef = `"number":1,"state":"open","html_url":"https://github.com/o/r/pull/1","draft":false`
	graphqlAnswer := func(query string) string {
		switch query {
		case documents["rollup"]:
			return `{"repository":{"pullRequest":{"url":"https://github.com/o/r/pull/1","commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":` + q(v) + `,"contexts":{"nodes":[` +
				`{"__typename":"CheckRun","name":` + q(v) + `,"conclusion":"SUCCESS","status":"IN_PROGRESS","summary":` + q(v) + `,"detailsUrl":"https://github.com/o/r/actions/runs/3/job/5"},` +
				`{"__typename":"CheckRun","name":"b","conclusion":"","status":"COMPLETED","detailsUrl":` + q(v) + `},` +
				`{"__typename":"StatusContext","context":` + q(v) + `,"state":"SUCCESS","description":` + q(v) + `}]}}}}]}}}}`
		case documents["comments"]:
			return `{"repository":{"issue":{"comments":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"author":{"login":` + q(v) + `},"createdAt":"t","body":` + q(v) + `,"isMinimized":false,"authorAssociation":` + q(v) + `}]}}}}`
		case documents["repository"]:
			return `{"repository":{"nameWithOwner":` + q(v) + `,"autoMergeAllowed":true}}`
		case documents["viewer"]:
			return `{"viewer":{"login":` + q(v) + `}}`
		case documents["issueID"]:
			return `{"repository":{"issue":{"id":` + q(v) + `}}}`
		case documents["labelID"]:
			return `{"repository":{"issue":{"id":` + q(v) + `},"label":{"id":` + q(v) + `}}}`
		}
		return `{}`
	}
	return func(req connector.Request) (connector.Response, error) {
		u, err := url.Parse(req.URL)
		if err != nil {
			return connector.Response{}, err
		}
		p, get := u.Path, req.Method == "GET"
		body := `{}`
		var header map[string]string
		switch {
		case req.Method == "POST" && p == "/graphql":
			var b struct{ Query string }
			if err := json.Unmarshal([]byte(req.Body), &b); err != nil {
				return connector.Response{}, err
			}
			body = `{"data":` + graphqlAnswer(b.Query) + `}`
			header = map[string]string{"X-OAuth-Scopes": v}
		case get && strings.HasSuffix(p, "/logs"):
			body = "log line"
		case get && p == "/installation/repositories":
			body = `{"repositories":[{"full_name":"o/r","permissions":{"push":true}}]}`
		case get && p == "/search/issues":
			body = `{"items":[{"number":1,"title":` + q(v) + `,"state":"open","labels":[{"name":` + q(v) + `}]}]}`
		case get && strings.HasSuffix(p, "/labels"):
			body = `[{"name":` + q(v) + `}]`
		case get && strings.HasSuffix(p, "/files"):
			body = `[{"filename":` + q(v) + `}]`
		case get && strings.Contains(p, "/compare/"):
			body = `{"behind_by":2}`
		case get && strings.Contains(p, "/matching-refs/"):
			body = `[{"ref":"refs/heads/` + v + `"}]`
		case get && strings.Contains(p, "/rules/branches/"):
			body = `[]`
		case get && strings.HasSuffix(p, "/protection"):
			body = `{}`
		case get && strings.Contains(p, "/dependencies/"):
			body = `[{"number":1}]`
		case get && strings.HasSuffix(p, "/timeline"):
			body = `[{"event":"unlabeled","label":{"name":` + q(v) + `}}]`
		case get && strings.HasSuffix(p, "/pulls"):
			body = `[{` + prRef + `,"node_id":` + q(v) + `,"head":{"ref":` + q(v) + `,"sha":` + q(v) + `},"base":{"ref":` + q(v) + `}}]`
		case get && pullPathRE.MatchString(p):
			body = `{` + prRef + `,"node_id":` + q(v) + `,"head":{"ref":` + q(v) + `,"sha":` + q(v) + `},"base":{"ref":` + q(v) + `}}`
		case get && strings.HasSuffix(p, "/issues"):
			body = `[{"number":1,"title":"t","state":"open","labels":[{"name":` + q(v) + `}]}]`
		case get && issuePathRE.MatchString(p):
			body = `{"number":1,"title":"t","state":"open","labels":[{"name":` + q(v) + `}]}`
		case req.Method == "POST" && strings.HasSuffix(p, "/pulls"):
			body = `{` + prRef + `}`
		case req.Method == "POST" && strings.HasSuffix(p, "/issues"):
			body = `{"html_url":"https://github.com/o/r/issues/1"}`
		case req.Method == "DELETE":
			return connector.Response{Status: 204}, nil
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
		{"forge.ciRun", func(in string) error { _, err := ciRun(ftgt, in); return err }},
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
		{"forge.probe", func(string) error { _, err := probe(ftgt); return err }},
		{"forge.autoMergeAllowed", func(string) error { _, err := autoMergeAllowed(ftgt); return err }},
		{"forge.tokenGrant prefix", func(in string) error { _, err := tokenGrant(ftgt, in); return err }},
		{"forge.tokenGrant scopes", func(string) error { _, err := tokenGrant(ftgt, "ghp_"); return err }},
		{"forge.tokenGrant installation", func(string) error { _, err := tokenGrant(ftgt, "ghs_"); return err }},

		{"tracker.issue", func(in string) error { _, err := issue(tgt, in); return err }},
		{"tracker.comments", func(in string) error { _, err := commentsOf(tgt, in); return err }},
		{"tracker.nativeDeps", func(in string) error { _, err := nativeDeps(tgt, in); return err }},
		{"tracker.blocks", func(in string) error { _, err := blocks(tgt, in); return err }},
		{"tracker.removedLabels", func(in string) error { _, err := removedLabels(tgt, in); return err }},
		{"tracker.probe", func(string) error { _, err := trackerProbe(tgt); return err }},
		{"tracker.matchesRoute number", func(in string) error { _, err := matchesRoute(tgt, in, label("l")); return err }},
		{"tracker.matchesRoute label", func(in string) error { _, err := matchesRoute(tgt, "1", label(in)); return err }},
		{"tracker.listIssues label", func(in string) error {
			_, err := listIssues(tgt, tracker.Query{Label: in}, 10)
			return err
		}},
		{"tracker.listIssues route", func(in string) error {
			_, err := listIssues(tgt, tracker.Query{Route: label(in)}, 10)
			return err
		}},
		{"tracker.count label", func(in string) error {
			_, err := count(tgt, tracker.Query{Label: in}, 10, true)
			return err
		}},
		{"tracker.count route", func(in string) error {
			_, err := count(tgt, tracker.Query{Route: label(in)}, 10, true)
			return err
		}},
		{"tracker.labeled label", func(in string) error {
			_, _, err := labeledPage(tgt, in, nil, "open", "", 10)
			return err
		}},
		{"tracker.labeled route", func(in string) error {
			_, _, err := labeledPage(tgt, "", label(in), "closed", "", 10)
			return err
		}},
		{"tracker.labeled cursor", func(in string) error {
			_, _, err := labeledPage(tgt, "l", nil, "open", in, 10)
			return err
		}},
		{"tracker.labeled state", func(in string) error {
			_, _, err := labeledPage(tgt, "l", nil, in, "", 10)
			return err
		}},
		{"tracker.searchIssues label", func(in string) error { _, err := search(tgt, "t", "open", in, 10); return err }},
		{"tracker.searchIssues text", func(in string) error { _, err := search(tgt, in, "open", "", 10); return err }},
		{"tracker.searchIssues state", func(in string) error { _, err := search(tgt, "t", in, "", 10); return err }},
		{"tracker.transition", func(in string) error {
			_, err := trackerTransition(tgt, in, tracker.TransitionRequest{State: in})
			return err
		}},
		{"tracker.editLabels number", func(in string) error { return editLabels(tgt, in, []string{"a"}, []string{"b"}) }},
		{"tracker.editLabels add", func(in string) error { return editLabels(tgt, "1", []string{in}, nil) }},
		{"tracker.editLabels remove", func(in string) error { return editLabels(tgt, "1", nil, []string{in}) }},
		{"tracker.comment", func(in string) error { return issueComment(tgt, in, tracker.CommentRequest{Body: "b"}) }},
		{"tracker.fileIssue labels", func(in string) error {
			_, err := fileIssue(tgt, tracker.IssueRequest{Title: "t", Labels: []string{in}})
			return err
		}},
		{"tracker.fileIssue route", func(in string) error {
			_, err := fileIssue(tgt, tracker.IssueRequest{Title: "t", Route: label(in)})
			return err
		}},
		{"tracker.closeIssue", func(in string) error { return closeIssue(tgt, in) }},
		{"tracker.listLabels", func(string) error { _, err := listLabels(tgt); return err }},
		{"tracker.createLabel name", func(in string) error { return createLabel(tgt, in, "d", "ffffff") }},
		{"tracker.createLabel color", func(in string) error { return createLabel(tgt, "n", "d", in) }},
	}
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			for i, in := range connector.HostileInputs() {
				t.Run(fmt.Sprintf("input-%d", i), func(t *testing.T) {
					host.Serve(t, githubAnswer(in))
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
	tt := tracker.Target{API: tgt.API, Repo: tgt.Repo}
	answer := githubAnswer("feat")
	route := []tracker.Selector{{Kind: "label", Value: "feat"}}
	type obj = map[string]any
	samples := map[string]connector.Sample{
		"forge.prsForBranch":     {Params: obj{"target": ft, "branch": "feat"}, Answer: answer},
		"forge.pr":               {Params: obj{"target": ft, "number": "1"}, Answer: answer},
		"forge.checks":           {Params: obj{"target": ft, "number": "1"}, Answer: answer},
		"forge.jobLog":           {Params: obj{"target": ft, "jobId": "5"}, Answer: answer},
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
		"forge.ciRun":            {Params: obj{"target": ft, "number": "1"}, Answer: answer},
		"forge.tokenGrant":       {Params: obj{"target": ft, "tokenPrefix": "ghp_"}, Answer: answer},

		"tracker.issue":         {Params: obj{"target": tt, "number": "1"}, Answer: answer},
		"tracker.listIssues":    {Params: obj{"target": tt, "label": "feat", "limit": 10}, Answer: answer},
		"tracker.comments":      {Params: obj{"target": tt, "number": "1"}, Answer: answer},
		"tracker.nativeDeps":    {Params: obj{"target": tt, "number": "1"}, Answer: answer},
		"tracker.blocks":        {Params: obj{"target": tt, "number": "1"}, Answer: answer},
		"tracker.removedLabels": {Params: obj{"target": tt, "number": "1"}, Answer: answer},
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
