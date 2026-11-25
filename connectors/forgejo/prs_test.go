package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"spindrift.dev/sdk/connector"
	"spindrift.dev/sdk/forge"
)

func TestOpenAddsWIPPrefix(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		repoAPI + "/pulls": {Status: 201, Body: []byte(`{"number":8,"html_url":"https://forge.test/o/r/pulls/8","state":"open","title":"WIP: t","head":{"ref":"agent/issue-1","sha":"s"}}`)},
	})
	p, err := open(ftgt, forge.NewPR{Title: "t", Body: "b", Base: "main", Head: "agent/issue-1", Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	want := forge.PR{Number: "8", URL: "https://forge.test/o/r/pulls/8", State: "open", Draft: true, Head: "agent/issue-1", HeadSHA: "s", Mergeable: "CONFLICTING"}
	if p != want {
		t.Fatalf("pr = %#v", p)
	}
	r := (*got)[0]
	if r.Method != "POST" || r.Body != `{"base":"main","body":"b","head":"agent/issue-1","title":"WIP: t"}` {
		t.Fatalf("request = %#v", r)
	}
}

func TestOpenNotDraftKeepsTitle(t *testing.T) {
	got := stub(t, map[string]connector.Response{repoAPI + "/pulls": ok(`{"number":8,"head":{}}`)})
	if _, err := open(ftgt, forge.NewPR{Title: "t", Base: "main", Head: "h"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*got)[0].Body, `"title":"t"`) {
		t.Fatalf("body = %s", (*got)[0].Body)
	}
}

func TestOpenConflictIsClassed(t *testing.T) {
	stub(t, map[string]connector.Response{repoAPI + "/pulls": status(409)})
	if _, err := open(ftgt, forge.NewPR{Title: "t", Draft: true}); err == nil {
		t.Fatal("a 409 must be an error: core adopts the existing PR")
	}
}

func TestBehind(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		repoAPI + "/pulls/5":                      ok(`{"number":5,"head":{"ref":"agent/issue-1"},"base":{"ref":"main"}}`),
		repoAPI + "/compare/agent/issue-1...main": ok(`{"total_commits":3}`),
	})
	n, err := behind(ftgt, "5")
	if err != nil || n != 3 {
		t.Fatalf("behind = %d, %v", n, err)
	}
	if r := (*got)[1]; r.Method != "GET" {
		t.Fatalf("request = %#v", r)
	}
}

func TestFiles(t *testing.T) {
	stub(t, map[string]connector.Response{
		repoAPI + "/pulls/5/files?limit=100&page=1": ok(`[{"filename":"a.go"},{"filename":""},{"filename":"b/c.go"}]`),
		repoAPI + "/pulls/5/files?limit=100&page=2": ok(`[]`),
	})
	files, err := prFiles(ftgt, "5")
	if err != nil || !reflect.DeepEqual(files, []string{"a.go", "b/c.go"}) {
		t.Fatalf("files = %#v, %v", files, err)
	}
}

func TestSetDraft(t *testing.T) {
	for _, tc := range []struct {
		name, pr string
		draft    bool
		body     string // "" means no PATCH
	}{
		{"mark ready", `"title":"WIP: [wip] x"`, false, `{"title":"x"}`},
		{"mark ready by draft field", `"draft":true,"title":"x"`, false, `{"title":"x"}`},
		{"ready already", `"title":"x"`, false, ""},
		{"mark draft", `"title":"x"`, true, `{"title":"WIP: x"}`},
		{"draft already", `"title":"WIP: x"`, true, ""},
		{"draft by field already", `"draft":true,"title":"x"`, true, ""},
	} {
		got := stub(t, map[string]connector.Response{
			repoAPI + "/pulls/5": ok(`{"number":5,"head":{},` + tc.pr + `}`),
		})
		if err := setDraft(ftgt, "5", tc.draft); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if tc.body == "" {
			if len(*got) != 1 {
				t.Errorf("%s: no-op sent %d requests", tc.name, len(*got))
			}
			continue
		}
		if len(*got) != 2 || (*got)[1].Method != "PATCH" || (*got)[1].Body != tc.body {
			t.Errorf("%s: requests = %#v", tc.name, *got)
		}
	}
}

func TestSetDraftRefusesEmptyTitle(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		repoAPI + "/pulls/5": ok(`{"number":5,"title":"WIP:","head":{}}`),
	})
	if err := setDraft(ftgt, "5", false); err == nil {
		t.Fatal("an empty stripped title must be refused")
	}
	if len(*got) != 1 {
		t.Fatalf("requests = %#v", *got)
	}
}

func TestComment(t *testing.T) {
	got := stub(t, map[string]connector.Response{repoAPI + "/issues/5/comments": {Status: 201, Body: []byte(`{}`)}})
	if err := comment(ftgt, "5", "hi"); err != nil {
		t.Fatal(err)
	}
	if r := (*got)[0]; r.Method != "POST" || r.Body != `{"body":"hi"}` {
		t.Fatalf("request = %#v", r)
	}
}

func TestMerge(t *testing.T) {
	got := stub(t, map[string]connector.Response{repoAPI + "/pulls/5/merge": status(200)})
	if err := merge(ftgt, "5"); err != nil {
		t.Fatal(err)
	}
	if r := (*got)[0]; r.Method != "POST" || r.Body != `{"Do":"rebase","delete_branch_after_merge":true}` {
		t.Fatalf("request = %#v", r)
	}
}

func TestMergeFailureClasses(t *testing.T) {
	for code, want := range map[int]connector.Class{
		502: connector.Transient, 405: connector.NotMergeable, 409: connector.NotMergeable,
		403: connector.ClassForStatus(403), 404: connector.ClassForStatus(404),
	} {
		stub(t, map[string]connector.Response{repoAPI + "/pulls/5/merge": status(code)})
		err := merge(ftgt, "5")
		var ce *connector.Error
		if !errors.As(err, &ce) || ce.Class != want {
			t.Errorf("status %d: err = %#v, want class %v", code, err, want)
		}
	}
	if connector.ClassForStatus(405) == connector.Transient {
		t.Error("a refused merge must not be transient")
	}
}

func TestMergeRefusalKeepsMessage(t *testing.T) {
	stub(t, map[string]connector.Response{repoAPI + "/pulls/5/merge": {Status: 405, Body: []byte(`{"message":"Please try again later"}`)}})
	if err := merge(ftgt, "5"); err == nil || !strings.Contains(err.Error(), ": Please try again later") {
		t.Errorf("err = %v, want the forge message", err)
	}
	stub(t, map[string]connector.Response{repoAPI + "/pulls/5/merge": {Status: 409, Body: []byte("<html>")}})
	if err := merge(ftgt, "5"); err == nil || !strings.HasSuffix(err.Error(), "status 409") {
		t.Errorf("err = %v, want the bare form", err)
	}
}

func TestEnqueueAutoMerge(t *testing.T) {
	got := stub(t, map[string]connector.Response{repoAPI + "/pulls/5/merge": status(200)})
	if err := enqueueAutoMerge(ftgt, "5"); err != nil {
		t.Fatal(err)
	}
	want := `{"Do":"rebase","delete_branch_after_merge":true,"merge_when_checks_succeed":true}`
	if r := (*got)[0]; r.Method != "POST" || r.Body != want {
		t.Fatalf("request = %#v", r)
	}
}

func TestAutoMergeAllowed(t *testing.T) {
	for body, want := range map[string]bool{`{"allow_rebase":true}`: true, `{"allow_rebase":false,"allow_squash_merge":true}`: false, `{}`: false} {
		stub(t, map[string]connector.Response{repoAPI: ok(body)})
		if got, err := autoMergeAllowed(ftgt); err != nil || got != want {
			t.Errorf("%s: %v, %v", body, got, err)
		}
	}
}

func TestBranchProtected(t *testing.T) {
	got := stub(t, map[string]connector.Response{
		repoAPI + "/branches/main":          ok(`{"name":"main","protected":true}`),
		repoAPI + "/branches/agent/issue-1": ok(`{"name":"agent/issue-1","protected":false}`),
	})
	for branch, want := range map[string]bool{"main": true, "agent/issue-1": false} {
		if p, err := branchProtected(ftgt, branch); err != nil || p != want {
			t.Errorf("branchProtected(%q) = %v, %v", branch, p, err)
		}
	}
	if _, err := branchProtected(ftgt, "gone"); err == nil {
		t.Error("an unresolvable branch is a failed probe, not unprotected")
	}
	if _, err := branchProtected(ftgt, "../x"); err == nil {
		t.Error("traversal accepted")
	}
	for _, r := range (*got)[:2] {
		if r.Method != "GET" {
			t.Fatalf("request = %#v", r)
		}
	}
}

func TestProbe(t *testing.T) {
	stub(t, map[string]connector.Response{repoAPI: ok(`{"full_name":"o/r"}`)})
	if name, err := probe(ftgt); err != nil || name != "o/r" {
		t.Fatalf("probe = %q, %v", name, err)
	}
}

// A write whose answer is ignored succeeds on any 2xx, body or none: failing
// after the forge accepted it would make core repeat the write.
func TestWritesAcceptABodilessAnswer(t *testing.T) {
	stub(t, map[string]connector.Response{
		repoAPI + "/issues/5/comments": status(201),
		repoAPI + "/pulls/5":           status(200),
	})
	if err := comment(ftgt, "5", "hi"); err != nil {
		t.Errorf("comment: %v", err)
	}
	// setDraft reads the PR first, so drive its PATCH itself.
	if err := send("PATCH", ftgt.API, forgePath(ftgt, "/pulls/5"), nil, nil); err != nil {
		t.Errorf("PATCH: %v", err)
	}
}
