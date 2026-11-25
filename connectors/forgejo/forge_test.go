package main

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
)

var ftgt = forge.Target{API: "https://forge.test", Repo: "o/r"}

const repoAPI = "https://forge.test/api/v1/repos/o/r"

// stub serves canned responses keyed by URL through the fake host and records
// the requests it admits; an unlisted URL answers 404.
func stub(t *testing.T, resp map[string]connector.Response) *[]connector.Request {
	t.Helper()
	return stubOn(t, host, resp)
}

func stubOn(t *testing.T, h *connector.FakeHost, resp map[string]connector.Response) *[]connector.Request {
	t.Helper()
	return h.Serve(t, func(r connector.Request) (connector.Response, error) {
		v, ok := resp[r.URL]
		if !ok {
			return connector.Response{Status: 404, Body: []byte("{}")}, nil
		}
		return v, nil
	})
}

func ok(body string) connector.Response {
	return connector.Response{Status: 200, Body: []byte(body)}
}

func status(code int) connector.Response { return connector.Response{Status: code} }

func prList(page int) string {
	return repoAPI + "/pulls?limit=100&page=" + strconv.Itoa(page) + "&state=all"
}

func TestPRsForBranch(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		prList(1): ok(`[
			{"number":3,"html_url":"https://forge.test/o/r/pulls/3","state":"closed","merged":true,"merge_commit_sha":"m3","mergeable":false,"head":{"ref":"agent/issue-1","sha":"h3"}},
			{"number":4,"html_url":"https://forge.test/o/r/pulls/4","state":"open","head":{"ref":"other","sha":"h4"}}]`),
		prList(2): ok(`[
			{"number":1,"html_url":"https://forge.test/o/r/pulls/1","state":"closed","merged":false,"head":{"ref":"agent/issue-1","sha":"h1"}},
			{"number":5,"html_url":"https://forge.test/o/r/pulls/5","state":"open","mergeable":true,"head":{"ref":"agent/issue-1","sha":"h5"}}]`),
		prList(3): ok(`[]`),
	})
	prs, err := prsForBranch(ftgt, "agent/issue-1")
	if err != nil {
		t.Fatal(err)
	}
	// Newest first, whatever order the pages came in.
	want := []forge.PR{
		{Number: "5", URL: "https://forge.test/o/r/pulls/5", State: "open", Head: "agent/issue-1", HeadSHA: "h5", Mergeable: "MERGEABLE"},
		{Number: "3", URL: "https://forge.test/o/r/pulls/3", State: "closed", Merged: true, Head: "agent/issue-1", HeadSHA: "h3", Mergeable: "CONFLICTING", MergeCommit: "m3"},
		{Number: "1", URL: "https://forge.test/o/r/pulls/1", State: "closed", Head: "agent/issue-1", HeadSHA: "h1", Mergeable: "CONFLICTING"},
	}
	if !reflect.DeepEqual(prs, want) {
		t.Fatalf("prs = %#v", prs)
	}
	r := (*got)[0]
	if r.Method != "GET" || r.Headers["Accept"] != "application/json" {
		t.Fatalf("request = %#v", r)
	}
}

func TestPRsForBranchStopsOnRepeatedPage(t *testing.T) {
	// A forge that ignores `page` answers the same first page forever.
	page := ok(`[{"number":2,"html_url":"u","state":"open","head":{"ref":"b","sha":"s"}}]`)
	stub(t, map[string]connector.Response{prList(1): page, prList(2): page})
	prs, err := prsForBranch(ftgt, "b")
	if err != nil || len(prs) != 1 {
		t.Fatalf("prs = %#v, err = %v", prs, err)
	}
}

func TestPRsForBranchEmpty(t *testing.T) {
	stub(t, map[string]connector.Response{prList(1): ok(`[]`)})
	prs, err := prsForBranch(ftgt, "b")
	if err != nil || len(prs) != 0 {
		t.Fatalf("prs = %#v, err = %v", prs, err)
	}
}

func TestPR(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		repoAPI + "/pulls/5": ok(`{"number":5,"html_url":"https://forge.test/o/r/pulls/5","state":"open","title":"x","head":{"ref":"b","sha":"s5"}}`),
	})
	p, err := pr(ftgt, "5")
	if err != nil {
		t.Fatal(err)
	}
	want := forge.PR{Number: "5", URL: "https://forge.test/o/r/pulls/5", State: "open", Head: "b", HeadSHA: "s5", Mergeable: "CONFLICTING"}
	if p != want {
		t.Fatalf("pr = %#v", p)
	}
	if r := (*got)[0]; r.Method != "GET" {
		t.Fatalf("request = %#v", r)
	}
}

func TestPRDraft(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`"draft":true,"title":"x"`, true},
		{`"draft":false,"title":"WIP: x"`, true},
		{`"title":"  wip: x"`, true},
		{`"title":"[WIP] x"`, true},
		{`"title":"[wip]: x"`, true},
		{`"title":"x WIP:"`, false},
		{`"title":"WIPE the cache"`, false},
		{`"title":"x"`, false},
	} {
		stub(t, map[string]connector.Response{
			repoAPI + "/pulls/5": ok(`{"number":5,"state":"open",` + tc.raw + `}`),
		})
		p, err := pr(ftgt, "5")
		if err != nil || p.Draft != tc.want {
			t.Errorf("%s: draft = %v (err %v), want %v", tc.raw, p.Draft, err, tc.want)
		}
	}
}

func TestPRRefusesBadNumber(t *testing.T) {
	stub(t, nil)
	for _, n := range []string{"", "0", "-1", "05", "1/../2", "x"} {
		if _, err := pr(ftgt, n); err == nil {
			t.Errorf("pr(%q) accepted", n)
		}
	}
}

func TestPRNotFoundIsClassed(t *testing.T) {
	stub(t, nil)
	_, err := pr(ftgt, "9")
	var ce *connector.Error
	if !errors.As(err, &ce) || ce.Class != connector.NotFound {
		t.Fatalf("err = %#v", err)
	}
}

const (
	prJSON    = `{"number":7,"state":"open","head":{"ref":"b","sha":"abc"}}`
	statusURL = repoAPI + "/commits/abc/status"
	listURL   = repoAPI + "/commits/abc/statuses?limit=100&page="
)

func TestChecks(t *testing.T) {
	stub(t, map[string]connector.Response{
		repoAPI + "/pulls/7": ok(prJSON),
		statusURL:            ok(`{"state":"failure","total_count":3}`),
		// Newest first: ci/build was re-run after it failed.
		listURL + "1": ok(`[
			{"id":9,"context":"ci/build","state":"success","description":"ok","target_url":"u1"},
			{"id":8,"context":"ci/lint","state":"failure","description":"bad","target_url":"u2"},
			{"id":7,"context":"ci/build","state":"failure","description":"old"},
			{"id":6,"context":"ci/skip","state":"skipped","description":""}]`),
		listURL + "2": ok(`[]`),
	})
	ro, err := checks(ftgt, "7")
	if err != nil {
		t.Fatal(err)
	}
	want := forge.Rollup{State: "FAILURE", Checks: []forge.Check{
		{Kind: "StatusContext", Name: "ci/build", Conclusion: "SUCCESS", Summary: "ok"},
		{Kind: "StatusContext", Name: "ci/lint", Conclusion: "FAILURE", Summary: "bad"},
		{Kind: "StatusContext", Name: "ci/skip", Conclusion: "SKIPPED"},
	}}
	if !reflect.DeepEqual(ro, want) {
		t.Fatalf("rollup = %#v", ro)
	}
}

func TestChecksNone(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		repoAPI + "/pulls/7": ok(prJSON),
		statusURL:            ok(`{"state":"pending","total_count":0}`),
	})
	ro, err := checks(ftgt, "7")
	if err != nil {
		t.Fatal(err)
	}
	if want := (forge.Rollup{Checks: []forge.Check{}}); !reflect.DeepEqual(ro, want) {
		t.Fatalf("rollup = %#v", ro)
	}
	if len(*got) != 2 {
		t.Fatalf("a commit with no statuses must not list them: %d requests", len(*got))
	}
}

func TestChecksCombinedStates(t *testing.T) {
	for _, tc := range []struct {
		combined, want string
	}{
		{"success", "SUCCESS"},
		{"pending", "PENDING"},
		{"failure", "FAILURE"},
		{"error", "ERROR"},
		// Every context skipped ranks best.
		{"skipped", "SUCCESS"},
	} {
		stub(t, map[string]connector.Response{
			repoAPI + "/pulls/7": ok(prJSON),
			statusURL:            ok(`{"state":"` + tc.combined + `","total_count":1}`),
			listURL + "1":        ok(`[{"id":1,"context":"c","state":"success"}]`),
			listURL + "2":        ok(`[]`),
		})
		ro, err := checks(ftgt, "7")
		if err != nil || ro.State != tc.want {
			t.Errorf("combined %q: state = %q (err %v), want %q", tc.combined, ro.State, err, tc.want)
		}
	}
}

// "warning" is advisory and can mask a pending context, so the state is read
// off the newest status of each context instead.
func TestChecksWarningResolvesFromStatuses(t *testing.T) {
	for _, tc := range []struct {
		statuses, want string
	}{
		{`{"id":2,"context":"a","state":"warning"},{"id":1,"context":"b","state":"success"}`, "SUCCESS"},
		{`{"id":2,"context":"a","state":"warning"},{"id":1,"context":"b","state":"pending"}`, "PENDING"},
		{`{"id":2,"context":"a","state":"warning"},{"id":1,"context":"b","state":"error"}`, "ERROR"},
		{`{"id":3,"context":"a","state":"warning"},{"id":2,"context":"b","state":"failure"},{"id":1,"context":"b","state":"success"},{"id":0,"context":"c","state":"pending"}`, "FAILURE"},
	} {
		stub(t, map[string]connector.Response{
			repoAPI + "/pulls/7": ok(prJSON),
			statusURL:            ok(`{"state":"warning","total_count":2}`),
			listURL + "1":        ok(`[` + tc.statuses + `]`),
			listURL + "2":        ok(`[]`),
		})
		ro, err := checks(ftgt, "7")
		if err != nil || ro.State != tc.want {
			t.Errorf("%s: state = %q (err %v), want %q", tc.statuses, ro.State, err, tc.want)
		}
	}
}

func TestChecksRefusesUnknownCombinedState(t *testing.T) {
	stub(t, map[string]connector.Response{
		repoAPI + "/pulls/7": ok(prJSON),
		statusURL:            ok(`{"state":"mystery","total_count":1}`),
	})
	if _, err := checks(ftgt, "7"); err == nil || !strings.Contains(err.Error(), "mystery") {
		t.Fatalf("err = %v", err)
	}
}

func TestBranchExists(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		repoAPI + "/branches/agent/issue-1":  ok(`{"name":"agent/issue-1"}`),
		repoAPI + "/branches/feat/a%20b%23c": ok(`{}`),
		repoAPI + "/branches/boom":           status(500),
	})
	for branch, want := range map[string]bool{
		"agent/issue-1": true,
		"agent/issue-2": false,
		"feat/a b#c":    true,
	} {
		if exists, err := branchExists(ftgt, branch); err != nil || exists != want {
			t.Errorf("branchExists(%q) = %v, %v; want %v", branch, exists, err, want)
		}
	}
	if _, err := branchExists(ftgt, "boom"); err == nil {
		t.Error("a 500 must be an error, not absence")
	}
	for _, r := range *got {
		if r.Method != "GET" || strings.Contains(r.URL, "%2F") {
			t.Fatalf("request = %#v", r)
		}
	}
}

func TestBranchRefusesTraversal(t *testing.T) {
	stub(t, nil)
	for _, b := range []string{"", "a//b", "../x", "a/./b", "a/..", "/a"} {
		if _, err := branchExists(ftgt, b); err == nil {
			t.Errorf("branchExists(%q) accepted", b)
		}
		if err := deleteBranch(ftgt, b); err == nil {
			t.Errorf("deleteBranch(%q) accepted", b)
		}
	}
}

func TestDeleteBranch(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		repoAPI + "/branches/agent/issue-1": status(204),
	})
	if err := deleteBranch(ftgt, "agent/issue-1"); err != nil {
		t.Fatal(err)
	}
	if r := (*got)[0]; r.Method != "DELETE" || r.URL != repoAPI+"/branches/agent/issue-1" {
		t.Fatalf("request = %#v", r)
	}
}

func TestDeleteBranchMissingIsNotFound(t *testing.T) {
	stub(t, nil)
	err := deleteBranch(ftgt, "gone")
	var ce *connector.Error
	if !errors.As(err, &ce) || ce.Class != connector.NotFound {
		t.Fatalf("err = %#v", err)
	}
}

func TestPRsForBranchWalksPastFiftyPages(t *testing.T) {
	const pages = 60
	host.Serve(t, func(r connector.Request) (connector.Response, error) {
		page, _ := strconv.Atoi(strings.Split(strings.Split(r.URL, "page=")[1], "&")[0])
		if page > pages {
			return ok(`[]`), nil
		}
		ref := "other"
		if page == 1 || page == pages {
			ref = "b"
		}
		return ok(`[{"number":` + strconv.Itoa(page) + `,"html_url":"u","state":"open","head":{"ref":"` + ref + `","sha":"s"}}]`), nil
	})
	prs, err := prsForBranch(ftgt, "b")
	if err != nil || len(prs) != 2 || prs[0].Number != strconv.Itoa(pages) {
		t.Fatalf("prs = %#v, err = %v", prs, err)
	}
}

func TestJobIDFromTargetURL(t *testing.T) {
	for _, tc := range []struct {
		name, api, target, want string
	}{
		{"relative", "https://forge.test", "/o/r/actions/runs/12/jobs/3", "12/3"},
		{"case-insensitive repo", "https://forge.test", "/O/R/actions/runs/1/jobs/0", "1/0"},
		{"sub-path base", "https://forge.test/forge", "/forge/o/r/actions/runs/12/jobs/3", "12/3"},
		{"absolute on base host", "https://forge.test", "https://forge.test/o/r/actions/runs/12/jobs/3", ""},
		{"absolute on another host", "https://forge.test", "https://evil.test/o/r/actions/runs/12/jobs/3", ""},
		{"another repo", "https://forge.test", "/o/other/actions/runs/12/jobs/3", ""},
		{"non-numeric run", "https://forge.test", "/o/r/actions/runs/x/jobs/3", ""},
		{"non-numeric job", "https://forge.test", "/o/r/actions/runs/12/jobs/x", ""},
		{"run zero", "https://forge.test", "/o/r/actions/runs/0/jobs/0", ""},
		{"signed job", "https://forge.test", "/o/r/actions/runs/12/jobs/-1", ""},
		{"wrong sub-path", "https://forge.test/forge", "/other/o/r/actions/runs/12/jobs/3", ""},
		{"missing sub-path", "https://forge.test/forge", "/o/r/actions/runs/12/jobs/3", ""},
		{"too few segments", "https://forge.test", "/o/r/actions/runs/12", ""},
		{"too many segments", "https://forge.test", "/o/r/actions/runs/12/jobs/3/x", ""},
		{"wrong literal", "https://forge.test", "/o/r/pulls/runs/12/jobs/3", ""},
		{"empty", "https://forge.test", "", ""},
	} {
		if got := jobID(forge.Target{API: tc.api, Repo: "o/r"}, tc.target); got != tc.want {
			t.Errorf("%s: jobID(%q) = %q, want %q", tc.name, tc.target, got, tc.want)
		}
	}
}

func TestChecksSetsJobID(t *testing.T) {
	stub(t, map[string]connector.Response{
		repoAPI + "/pulls/7": ok(prJSON),
		statusURL:            ok(`{"state":"failure","total_count":2}`),
		listURL + "1": ok(`[
			{"id":2,"context":"ci/build","state":"failure","target_url":"/o/r/actions/runs/5/jobs/1"},
			{"id":1,"context":"ci/other","state":"success","target_url":"https://elsewhere.test/x"}]`),
		listURL + "2": ok(`[]`),
	})
	ro, err := checks(ftgt, "7")
	if err != nil {
		t.Fatal(err)
	}
	if ro.Checks[0].JobID != "5/1" || ro.Checks[1].JobID != "" {
		t.Fatalf("job ids = %q, %q", ro.Checks[0].JobID, ro.Checks[1].JobID)
	}
}

const (
	runsURL = repoAPI + "/actions/runs?run_number=5"
	jobsURL = repoAPI + "/actions/runs/903/jobs"
)

func TestJobLog(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		runsURL:                           ok(`{"workflow_runs":[{"id":903}],"total_count":1}`),
		jobsURL:                           ok(`[{"id":40},{"id":41}]`),
		repoAPI + "/actions/jobs/41/logs": {Status: 200, Body: []byte("--- FAIL: TestX\n")},
	})
	log, err := jobLog(ftgt, "5/1")
	if err != nil {
		t.Fatal(err)
	}
	if log != "--- FAIL: TestX\n" {
		t.Fatalf("log = %q", log)
	}
	want := []string{runsURL, jobsURL, repoAPI + "/actions/jobs/41/logs"}
	if len(*got) != len(want) {
		t.Fatalf("requests = %#v", *got)
	}
	for i, r := range *got {
		if r.URL != want[i] || r.Method != "GET" || r.Body != "" {
			t.Errorf("request %d = %#v, want GET %s", i, r, want[i])
		}
		if !strings.HasPrefix(r.URL, repoAPI+"/") {
			t.Errorf("request %d leaves the repository API: %s", i, r.URL)
		}
	}
}

func TestJobLogUnderSubPathBase(t *testing.T) {
	api := "https://forge.test/forge"
	prefix := api + "/api/v1/repos/o/r"
	// The host prefixes each Route with the base's path, so this base needs its own table.
	routes, err := resolvedRoutes(api, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	got := stubOn(t, connector.NewFakeHost(routes), map[string]connector.Response{
		prefix + "/actions/runs?run_number=5": ok(`{"workflow_runs":[{"id":903}]}`),
		prefix + "/actions/runs/903/jobs":     ok(`[{"id":40},{"id":41}]`),
		prefix + "/actions/jobs/41/logs":      {Status: 200, Body: []byte("boom\n")},
	})
	log, err := jobLog(forge.Target{API: api, Repo: "o/r"}, "5/1")
	if err != nil || log != "boom\n" {
		t.Fatalf("log = %q, err = %v", log, err)
	}
	if len(*got) != 3 {
		t.Fatalf("requests = %#v", *got)
	}
}

func TestJobLogErrors(t *testing.T) {
	logURL := repoAPI + "/actions/jobs/40/logs"
	for name, resp := range map[string]map[string]connector.Response{
		"no run":         {runsURL: ok(`{"workflow_runs":[]}`)},
		"run 404":        {},
		"job missing":    {runsURL: ok(`{"workflow_runs":[{"id":903}]}`), jobsURL: ok(`[{"id":40}]`)},
		"no jobs":        {runsURL: ok(`{"workflow_runs":[{"id":903}]}`), jobsURL: ok(`[]`)},
		"log 404":        {runsURL: ok(`{"workflow_runs":[{"id":903}]}`), jobsURL: ok(`[{"id":40},{"id":41}]`)},
		"log 410":        {runsURL: ok(`{"workflow_runs":[{"id":903}]}`), jobsURL: ok(`[{"id":40},{"id":41}]`), repoAPI + "/actions/jobs/41/logs": status(410)},
		"bad runs":       {runsURL: ok(`not json`)},
		"runs read 404s": {logURL: ok("x")},
	} {
		stub(t, resp)
		if _, err := jobLog(ftgt, "5/1"); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestJobLogBadIDRefusedWithoutRequest(t *testing.T) {
	got := stub(t, nil)
	for _, id := range []string{"", "5", "5/", "/1", "5/1/2", "x/1", "5/x", "-5/1", "5/-1", "0x5/1", "5/1?x=y", " 5/1", "5/1\n", "0/0", "0/1"} {
		if _, err := jobLog(ftgt, id); err == nil {
			t.Errorf("jobLog(%q): want error", id)
		}
	}
	if _, err := jobLog(forge.Target{API: ftgt.API, Repo: "norepo"}, "5/1"); err == nil {
		t.Error("bad repo: want error")
	}
	if len(*got) != 0 {
		t.Fatalf("requests sent: %#v", *got)
	}
}
