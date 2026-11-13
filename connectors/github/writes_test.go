package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
)

const (
	refsURL  = "https://api.github.com/repos/o/r/git/refs/heads/"
	matchURL = "https://api.github.com/repos/o/r/git/matching-refs/heads/"
)

func classOf(t *testing.T, err error) connector.Class {
	t.Helper()
	var ce *connector.Error
	if !errors.As(err, &ce) {
		t.Fatalf("err = %v, want *connector.Error", err)
	}
	return ce.Class
}

func TestFetchClassesNon2xx(t *testing.T) {
	for status, want := range map[int]connector.Class{
		502: connector.Transient, 429: connector.RateLimited, 404: connector.NotFound,
		403: connector.Auth, 405: connector.Permanent,
	} {
		stub(t, map[string]connector.Response{"https://api.github.com/x": {Status: status}})
		_, err := fetch("GET", "https://api.github.com", "/x", nil)
		if got := classOf(t, err); got != want {
			t.Errorf("status %d: class %q, want %q", status, got, want)
		}
	}
}

func TestPRFillsWriteFields(t *testing.T) {
	cases := []struct {
		name, body string
		want       forge.PR
	}{
		{"open unknown mergeable, test-merge sha is not a landing",
			`{"number":5,"html_url":"u","state":"open","draft":true,"head":{"ref":"agent/issue-5","sha":"abc"},"merge_commit_sha":"test","mergeable":null,"merged_at":null}`,
			forge.PR{Number: "5", URL: "u", State: "open", Draft: true, Head: "agent/issue-5", HeadSHA: "abc", Mergeable: "UNKNOWN"}},
		{"open mergeable",
			`{"number":5,"html_url":"u","state":"open","head":{"ref":"b","sha":"abc"},"mergeable":true}`,
			forge.PR{Number: "5", URL: "u", State: "open", Head: "b", HeadSHA: "abc", Mergeable: "MERGEABLE"}},
		{"conflicting",
			`{"number":5,"html_url":"u","state":"open","head":{"ref":"b","sha":"abc"},"mergeable":false}`,
			forge.PR{Number: "5", URL: "u", State: "open", Head: "b", HeadSHA: "abc", Mergeable: "CONFLICTING"}},
		{"merged carries its commit",
			`{"number":5,"html_url":"u","state":"closed","head":{"ref":"b","sha":"abc"},"merge_commit_sha":"m1","mergeable":null,"merged_at":"2026-01-01T00:00:00Z"}`,
			forge.PR{Number: "5", URL: "u", State: "closed", Merged: true, Head: "b", HeadSHA: "abc", Mergeable: "UNKNOWN", MergeCommit: "m1"}},
	}
	for _, c := range cases {
		stub(t, map[string]connector.Response{"https://api.github.com/repos/o/r/pulls/5": ok(c.body)})
		got, err := pr(ftgt, "5")
		if err != nil || got != c.want {
			t.Errorf("%s: pr = %#v, err = %v", c.name, got, err)
		}
	}
}

func TestRawPRKeepsNodeIDAndBase(t *testing.T) {
	var r rawPR
	if err := json.Unmarshal([]byte(`{"node_id":"PR_x","base":{"ref":"main"}}`), &r); err != nil {
		t.Fatal(err)
	}
	if r.NodeID != "PR_x" || r.Base.Ref != "main" {
		t.Fatalf("raw = %#v", r)
	}
}

const repoReply = `{"data":{"repository":{"url":"https://github.com/o/r","nameWithOwner":"o/r","autoMergeAllowed":true}}}`

func TestRemote(t *testing.T) {
	got := stub(t, map[string]connector.Response{graphqlURL: ok(repoReply)})
	r, err := remote(ftgt)
	if err != nil {
		t.Fatal(err)
	}
	if want := (forge.Remote{CloneURL: "https://github.com/o/r.git", WebURL: "https://github.com/o/r"}); r != want {
		t.Fatalf("remote = %#v", r)
	}
	req := (*got)[0]
	var body struct {
		Variables map[string]string `json:"variables"`
	}
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil || req.Method != "POST" ||
		body.Variables["owner"] != "o" || body.Variables["repo"] != "r" {
		t.Fatalf("request = %#v, err = %v", req, err)
	}
}

func TestProbeAndAutoMerge(t *testing.T) {
	stub(t, map[string]connector.Response{graphqlURL: ok(repoReply)})
	if name, err := probe(ftgt); err != nil || name != "o/r" {
		t.Fatalf("probe = %q, %v", name, err)
	}
	if on, err := autoMergeAllowed(ftgt); err != nil || !on {
		t.Fatalf("autoMergeAllowed = %v, %v", on, err)
	}
}

func TestRepositoryErrors(t *testing.T) {
	for name, body := range map[string]string{
		"graphql errors":  `{"errors":[{"message":"boom"}]}`,
		"null repository": `{"data":{"repository":null}}`,
	} {
		stub(t, map[string]connector.Response{graphqlURL: ok(body)})
		if _, err := remote(ftgt); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := remote(forge.Target{API: ftgt.API, Repo: "norepo"}); err == nil {
		t.Error("repo without owner: want error")
	}
}

func TestBranchExists(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		matchURL + "agent/issue-1": ok(`[{"ref":"refs/heads/agent/issue-1"}]`),
		matchURL + "agent/issue-2": ok(`[{"ref":"refs/heads/agent/issue-20"}]`),
		matchURL + "agent/issue-3": ok(`[]`),
	})
	for branch, want := range map[string]bool{"agent/issue-1": true, "agent/issue-2": false, "agent/issue-3": false} {
		if ok, err := branchExists(ftgt, branch); err != nil || ok != want {
			t.Errorf("%s: exists = %v, err = %v", branch, ok, err)
		}
	}
	if r := (*got)[0]; r.Method != "GET" {
		t.Fatalf("request = %#v", r)
	}
}

func TestBranchExistsEscapesSegments(t *testing.T) {
	got := stub(t, map[string]connector.Response{matchURL + "a%20b/c%3Fd": ok(`[]`)})
	if _, err := branchExists(ftgt, "a b/c?d"); err != nil || len(*got) != 1 {
		t.Fatalf("err = %v, requests = %d", err, len(*got))
	}
}

func TestBranchNamesRefused(t *testing.T) {
	got := stub(t, nil)
	for _, b := range []string{"", ".", "..", "a/../b", "a//b", "/a", "a/", "a/."} {
		if _, err := branchExists(ftgt, b); err == nil {
			t.Errorf("branchExists(%q): want error", b)
		}
		if err := deleteBranch(ftgt, b); err == nil {
			t.Errorf("deleteBranch(%q): want error", b)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("sent %d requests for refused names", len(*got))
	}
}

func TestDeleteBranch(t *testing.T) {
	got := stub(t, map[string]connector.Response{refsURL + "agent/issue-1": {Status: 204}})
	if err := deleteBranch(ftgt, "agent/issue-1"); err != nil {
		t.Fatal(err)
	}
	if r := (*got)[0]; r.Method != "DELETE" {
		t.Fatalf("request = %#v", r)
	}
}

func TestDeleteBranchMissingRefIsNotFound(t *testing.T) {
	for status, want := range map[int]connector.Class{
		422: connector.NotFound, 404: connector.NotFound, 500: connector.Transient, 403: connector.Auth,
	} {
		stub(t, map[string]connector.Response{refsURL + "gone": {Status: status, Body: []byte(`{"message":"Reference does not exist"}`)}})
		if got := classOf(t, deleteBranch(ftgt, "gone")); got != want {
			t.Errorf("status %d: class %q, want %q", status, got, want)
		}
	}
}

const (
	apiRepo  = "https://api.github.com/repos/o/r"
	prJSON   = `{"number":5,"node_id":"PR_node","draft":true,"state":"open","html_url":"https://github.com/o/r/pull/5","head":{"ref":"agent/issue-9","sha":"abc"},"base":{"ref":"main"}}`
	readyGQL = "markPullRequestReadyForReview"
)

func TestOpen(t *testing.T) {
	got := stub(t, map[string]connector.Response{apiRepo + "/pulls": ok(prJSON)})
	p, err := open(ftgt, forge.NewPR{Title: "T", Body: "B", Base: "main", Head: "agent/issue-9", Draft: true})
	if err != nil || p.Number != "5" || !p.Draft || p.Head != "agent/issue-9" {
		t.Fatalf("open = %#v, %v", p, err)
	}
	r := (*got)[0]
	if r.Method != "POST" {
		t.Fatalf("request = %#v", r)
	}
	var body forge.NewPR
	if err := json.Unmarshal([]byte(r.Body), &body); err != nil || body != (forge.NewPR{Title: "T", Body: "B", Base: "main", Head: "agent/issue-9", Draft: true}) {
		t.Fatalf("body = %s, %v", r.Body, err)
	}
}

func TestOpenAlreadyExistsIsClassedError(t *testing.T) {
	stub(t, map[string]connector.Response{apiRepo + "/pulls": {Status: 422, Body: []byte("{}")}})
	_, err := open(ftgt, forge.NewPR{Head: "h", Base: "main"})
	if got := classOf(t, err); got != connector.Permanent {
		t.Fatalf("class = %v", got)
	}
}

func TestBehind(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		apiRepo + "/pulls/5":                      ok(prJSON),
		apiRepo + "/compare/main...agent/issue-9": ok(`{"behind_by":3}`),
	})
	n, err := behind(ftgt, "5")
	if err != nil || n != 3 {
		t.Fatalf("behind = %d, %v", n, err)
	}
	if len(*got) != 2 || (*got)[1].Method != "GET" {
		t.Fatalf("requests = %#v", *got)
	}
	if _, err := behind(ftgt, "5/../x"); err == nil {
		t.Error("bad number: want error")
	}
}

func TestFilesPagesAndKeepsDeletedPaths(t *testing.T) {
	var page1 []string
	for i := range pageSize {
		page1 = append(page1, `{"filename":"f`+string(rune('a'+i%26))+`"}`)
	}
	stub(t, map[string]connector.Response{
		apiRepo + "/pulls/5/files?per_page=100&page=1": ok("[" + strings.Join(page1, ",") + "]"),
		apiRepo + "/pulls/5/files?per_page=100&page=2": ok(`[{"filename":"old.go","status":"removed"}]`),
	})
	files, err := prFiles(ftgt, "5")
	if err != nil || len(files) != pageSize+1 || files[pageSize] != "old.go" {
		t.Fatalf("files = %d, %v", len(files), err)
	}
}

func TestFilesEmptyIsNotNil(t *testing.T) {
	stub(t, map[string]connector.Response{apiRepo + "/pulls/5/files?per_page=100&page=1": ok(`[]`)})
	files, err := prFiles(ftgt, "5")
	if err != nil || files == nil || len(files) != 0 {
		t.Fatalf("files = %#v, %v", files, err)
	}
}

func TestFilesOverPageCeilingIsError(t *testing.T) {
	old := send
	send = func(connector.Request) (connector.Response, error) {
		return ok("[" + strings.Repeat(`{"filename":"f"},`, pageSize-1) + `{"filename":"f"}]`), nil
	}
	t.Cleanup(func() { send = old })
	if _, err := prFiles(ftgt, "5"); err == nil {
		t.Fatal("want error, not a truncated list")
	}
}

func TestSetDraftFlipsAndIsIdempotent(t *testing.T) {
	resp := map[string]connector.Response{
		apiRepo + "/pulls/5": ok(prJSON), // draft: true
		graphqlURL:           ok(`{"data":{}}`),
	}
	got := stub(t, resp)
	if err := setDraft(ftgt, "5", true); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 {
		t.Fatalf("already-draft PR sent a mutation: %#v", *got)
	}
	if err := setDraft(ftgt, "5", false); err != nil {
		t.Fatal(err)
	}
	r := (*got)[len(*got)-1]
	if r.Method != "POST" || r.URL != graphqlURL || !strings.Contains(r.Body, readyGQL) || !strings.Contains(r.Body, "PR_node") {
		t.Fatalf("mutation = %#v", r)
	}
	// ready PR back to draft
	ready := strings.Replace(prJSON, `"draft":true`, `"draft":false`, 1)
	resp[apiRepo+"/pulls/5"] = ok(ready)
	if err := setDraft(ftgt, "5", true); err != nil {
		t.Fatal(err)
	}
	if r := (*got)[len(*got)-1]; !strings.Contains(r.Body, "convertPullRequestToDraft") {
		t.Fatalf("mutation = %#v", r)
	}
}

func TestMutationGraphQLErrorsFail(t *testing.T) {
	stub(t, map[string]connector.Response{
		apiRepo + "/pulls/5": ok(prJSON),
		graphqlURL:           ok(`{"errors":[{"message":"nope"}]}`),
	})
	if err := setDraft(ftgt, "5", false); err == nil {
		t.Error("setDraft: want error")
	}
	if err := enqueueAutoMerge(ftgt, "5"); err == nil {
		t.Error("enqueueAutoMerge: want error")
	}
}

func TestEnqueueAutoMerge(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		apiRepo + "/pulls/5": ok(prJSON),
		graphqlURL:           ok(`{"data":{}}`),
	})
	if err := enqueueAutoMerge(ftgt, "5"); err != nil {
		t.Fatal(err)
	}
	r := (*got)[1]
	if r.Method != "POST" || !strings.Contains(r.Body, "enablePullRequestAutoMerge") ||
		!strings.Contains(r.Body, "REBASE") || !strings.Contains(r.Body, "PR_node") {
		t.Fatalf("mutation = %#v", r)
	}
}

func TestComment(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		apiRepo + "/issues/5/comments": {Status: 201, Body: []byte("{}")},
	})
	if err := comment(ftgt, "5", "hi"); err != nil {
		t.Fatal(err)
	}
	r := (*got)[0]
	if r.Method != "POST" || r.Body != `{"body":"hi"}` {
		t.Fatalf("request = %#v", r)
	}
}

func TestMerge(t *testing.T) {
	url := apiRepo + "/pulls/5/merge"
	got := stub(t, map[string]connector.Response{url: ok(`{"merged":true}`)})
	if err := merge(ftgt, "5"); err != nil {
		t.Fatal(err)
	}
	if r := (*got)[0]; r.Method != "PUT" || r.Body != `{"merge_method":"rebase"}` {
		t.Fatalf("request = %#v", r)
	}
	for status, want := range map[int]connector.Class{502: connector.Transient, 405: connector.Permanent, 409: connector.Permanent} {
		stub(t, map[string]connector.Response{url: {Status: status, Body: []byte("{}")}})
		if got := classOf(t, merge(ftgt, "5")); got != want {
			t.Errorf("status %d: class = %v, want %v", status, got, want)
		}
	}
}

func TestBranchProtected(t *testing.T) {
	classic := apiRepo + "/branches/release/1/protection"
	rules := apiRepo + "/rules/branches/release/1"
	for name, c := range map[string]struct {
		resp    map[string]connector.Response
		want    bool
		wantErr bool
	}{
		"classic rule":         {map[string]connector.Response{classic: ok(`{}`)}, true, false},
		"absent, ruleset":      {map[string]connector.Response{rules: ok(`[{"type":"pull_request"}]`)}, true, false},
		"absent, no ruleset":   {map[string]connector.Response{rules: ok(`[]`)}, false, false},
		"unreadable, ruleset":  {map[string]connector.Response{classic: {Status: 403, Body: []byte("{}")}, rules: ok(`[{}]`)}, true, false},
		"unreadable, none":     {map[string]connector.Response{classic: {Status: 403, Body: []byte("{}")}, rules: ok(`[]`)}, false, true},
		"classic read failure": {map[string]connector.Response{classic: {Status: 502, Body: []byte("{}")}}, false, true},
		"ruleset read failure": {map[string]connector.Response{rules: {Status: 500, Body: []byte("{}")}}, false, true},
	} {
		stub(t, c.resp)
		got, err := branchProtected(ftgt, "release/1")
		if got != c.want || (err != nil) != c.wantErr {
			t.Errorf("%s: protected = %v, err = %v", name, got, err)
		}
	}
	if _, err := branchProtected(ftgt, ""); err == nil {
		t.Error("empty branch: want error")
	}
}
