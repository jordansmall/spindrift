package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
)

var ftgt = forge.Target{API: "https://api.github.com", Repo: "o/r"}

const prsURL = "https://api.github.com/repos/o/r/pulls?head=o%3Afeat%2Fx+y&state=all&sort=created&direction=desc&per_page=100"

func TestPRsForBranch(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		prsURL: ok(`[
			{"number":12,"html_url":"https://github.com/o/r/pull/12","state":"closed","merged_at":"2026-01-01T00:00:00Z"},
			{"number":11,"html_url":"https://github.com/o/r/pull/11","state":"closed","merged_at":null},
			{"number":10,"html_url":"https://github.com/o/r/pull/10","state":"open","merged_at":null}]`),
	})
	prs, err := prsForBranch(ftgt, "feat/x y")
	if err != nil {
		t.Fatal(err)
	}
	want := []forge.PR{
		{Number: "12", URL: "https://github.com/o/r/pull/12", State: "closed", Merged: true},
		{Number: "11", URL: "https://github.com/o/r/pull/11", State: "closed"},
		{Number: "10", URL: "https://github.com/o/r/pull/10", State: "open"},
	}
	if !reflect.DeepEqual(prs, want) {
		t.Fatalf("prs = %#v", prs)
	}
	r := (*got)[0]
	if r.Method != "GET" || r.Headers["Accept"] != "application/vnd.github+json" ||
		r.Headers["X-GitHub-Api-Version"] != "2022-11-28" {
		t.Fatalf("request = %#v", r)
	}
}

func TestPRsForBranchEmpty(t *testing.T) {
	stub(t, map[string]connector.Response{
		"https://api.github.com/repos/o/r/pulls?head=o%3Ab&state=all&sort=created&direction=desc&per_page=100": ok(`[]`),
	})
	prs, err := prsForBranch(ftgt, "b")
	if err != nil || len(prs) != 0 {
		t.Fatalf("prs = %#v, err = %v", prs, err)
	}
}

func TestPR(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		"https://api.github.com/repos/o/r/pulls/5": ok(
			`{"number":5,"html_url":"https://github.com/o/r/pull/5","state":"closed","merged":true,"merged_at":"2026-01-01T00:00:00Z"}`),
	})
	p, err := pr(ftgt, "5")
	if err != nil {
		t.Fatal(err)
	}
	want := forge.PR{Number: "5", URL: "https://github.com/o/r/pull/5", State: "closed", Merged: true}
	if p != want {
		t.Fatalf("pr = %#v", p)
	}
	if r := (*got)[0]; r.Method != "GET" {
		t.Fatalf("request = %#v", r)
	}
}

const graphqlURL = "https://api.github.com/graphql"

func TestChecks(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		graphqlURL: ok(`{"data":{"repository":{"pullRequest":{"commits":{"nodes":[{"commit":{"statusCheckRollup":{
			"state":"FAILURE","contexts":{"nodes":[
				{"__typename":"CheckRun","name":"build","conclusion":"FAILURE","summary":"boom"},
				{"__typename":"CheckRun","name":"lint","conclusion":null,"summary":null},
				{"__typename":"StatusContext","context":"ci/legacy","state":"ERROR","description":"timed out"},
				{"__typename":"StatusContext","context":"ci/none","state":"PENDING","description":null},
				{"__typename":"Other"}]}}}}]}}}}}`),
	})
	ro, err := checks(ftgt, "7")
	if err != nil {
		t.Fatal(err)
	}
	want := forge.Rollup{State: "FAILURE", Checks: []forge.Check{
		{Kind: "CheckRun", Name: "build", Conclusion: "FAILURE", Summary: "boom"},
		{Kind: "CheckRun", Name: "lint"},
		{Kind: "StatusContext", Name: "ci/legacy", Conclusion: "ERROR", Summary: "timed out"},
		{Kind: "StatusContext", Name: "ci/none", Conclusion: "PENDING"},
		{Kind: "Other"},
	}}
	if !reflect.DeepEqual(ro, want) {
		t.Fatalf("rollup = %#v", ro)
	}

	r := (*got)[0]
	if r.Method != "POST" || r.URL != graphqlURL ||
		r.Headers["Content-Type"] != "application/json" ||
		r.Headers["Accept"] != "application/vnd.github+json" {
		t.Fatalf("request = %#v", r)
	}
	var body struct {
		Query     string
		Variables map[string]any
	}
	if err := json.Unmarshal([]byte(r.Body), &body); err != nil {
		t.Fatal(err)
	}
	wantVars := map[string]any{"owner": "o", "repo": "r", "number": float64(7)}
	if !reflect.DeepEqual(body.Variables, wantVars) {
		t.Fatalf("variables = %#v", body.Variables)
	}
	if strings.Contains(body.Query, "mutation") || !strings.Contains(body.Query, "statusCheckRollup") {
		t.Fatalf("query = %q", body.Query)
	}
}

func TestChecksNoRollup(t *testing.T) {
	for name, body := range map[string]string{
		"null rollup": `{"data":{"repository":{"pullRequest":{"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}}}}}`,
		"no commits":  `{"data":{"repository":{"pullRequest":{"commits":{"nodes":[]}}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			stub(t, map[string]connector.Response{graphqlURL: ok(body)})
			ro, err := checks(ftgt, "7")
			if err != nil {
				t.Fatal(err)
			}
			if ro.State != "" || ro.Checks == nil || len(ro.Checks) != 0 {
				t.Fatalf("rollup = %#v", ro)
			}
		})
	}
}

func TestForgeErrors(t *testing.T) {
	cases := map[string]struct {
		resp connector.Response
		call func() error
	}{
		"prs status": {connector.Response{Status: 500, Body: []byte("{}")}, func() error { _, err := prsForBranch(ftgt, "b"); return err }},
		"prs decode": {ok(`{"not":"a list"}`), func() error { _, err := prsForBranch(ftgt, "b"); return err }},
		"pr status":  {connector.Response{Status: 404, Body: []byte("{}")}, func() error { _, err := pr(ftgt, "5"); return err }},
		"checks status": {connector.Response{Status: 403, Body: []byte("{}")}, func() error {
			_, err := checks(ftgt, "5")
			return err
		}},
		"graphql errors": {ok(`{"data":null,"errors":[{"message":"rate limited"}]}`), func() error {
			_, err := checks(ftgt, "5")
			return err
		}},
		"null pull request": {ok(`{"data":{"repository":{"pullRequest":null}}}`), func() error {
			_, err := checks(ftgt, "5")
			return err
		}},
		"null repository": {ok(`{"data":{"repository":null}}`), func() error {
			_, err := checks(ftgt, "5")
			return err
		}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			old := send
			send = func(connector.Request) (connector.Response, error) { return c.resp, nil }
			t.Cleanup(func() { send = old })
			if c.call() == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestForgeBadNumberRefusedWithoutRequest(t *testing.T) {
	got := stub(t, nil)
	for _, n := range []string{"", "0", "-1", "1/../x", "01", "abc"} {
		if _, err := pr(ftgt, n); err == nil {
			t.Errorf("pr(%q): want error", n)
		}
		if _, err := checks(ftgt, n); err == nil {
			t.Errorf("checks(%q): want error", n)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("requests sent: %#v", *got)
	}
}

func TestForgeBadRepoRefusedWithoutRequest(t *testing.T) {
	got := stub(t, nil)
	if _, err := checks(forge.Target{API: ftgt.API, Repo: "norepo"}, "5"); err == nil {
		t.Error("want error")
	}
	if len(*got) != 0 {
		t.Fatalf("requests sent: %#v", *got)
	}
}
