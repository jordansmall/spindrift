package seamtest

import (
	"bytes"
	"path/filepath"
	"reflect"
	"testing"
)

func runGh(t *testing.T, cfg GhConfig, args ...string) (code int, stdout string) {
	t.Helper()
	for k, v := range WriteFakeConfig(t, "gh", cfg) {
		t.Setenv(k, v)
	}
	var out, errb bytes.Buffer
	code = ghFake(args, &out, &errb)
	return code, out.String()
}

func TestGhFirstMatchingReplyAnswers(t *testing.T) {
	rec := filepath.Join(t.TempDir(), "rec")
	cfg := GhConfig{Record: rec, Replies: []GhReply{
		{Args: []string{"issue", "view", "7"}, Stdout: `{"number":7}`},
		{Args: []string{"issue", "view"}, Stdout: "other", Exit: 1},
	}}
	code, out := runGh(t, cfg, "issue", "view", "7", "--repo", "o/r", "--json", "number")
	if code != 0 || out != `{"number":7}` {
		t.Fatalf("got exit %d %q", code, out)
	}
	code, out = runGh(t, cfg, "issue", "view", "8")
	if code != 1 || out != "other" {
		t.Fatalf("got exit %d %q", code, out)
	}
	want := [][]string{
		{"issue", "view", "7", "--repo", "o/r", "--json", "number"},
		{"issue", "view", "8"},
	}
	if got := ReadRecord(t, rec); !reflect.DeepEqual(got, want) {
		t.Fatalf("record = %v; want %v", got, want)
	}
}

func TestGhUnmatchedSucceedsSilently(t *testing.T) {
	cfg := GhConfig{Record: filepath.Join(t.TempDir(), "rec"), Replies: []GhReply{{Args: []string{"pr", "view"}, Stdout: "x"}}}
	if code, out := runGh(t, cfg, "view", "pr"); code != 0 || out != "" {
		t.Fatalf("got exit %d %q; order matters, want silent success", code, out)
	}
}

func TestGhMissingConfig(t *testing.T) {
	t.Setenv(configEnv("gh"), "")
	var out, errb bytes.Buffer
	if code := ghFake([]string{"issue"}, &out, &errb); code != fakeConfigExit {
		t.Fatalf("exit %d; want %d", code, fakeConfigExit)
	}
}

func typedCfg(t *testing.T) (GhConfig, string) {
	rec := filepath.Join(t.TempDir(), "rec")
	return GhConfig{
		Record: rec,
		Issues: []GhIssue{
			{Number: 7, Title: "T", Body: "B", Labels: []string{"ready-for-agent"}, Comments: []GhComment{{Author: "bob", CreatedAt: "2026-01-01T00:00:00Z", Body: "hi"}}},
			{Number: 8, Title: "U", Labels: []string{"other"}},
			{Number: 9, Title: "V", State: "CLOSED", Labels: []string{"ready-for-agent"}},
		},
		PRs: []GhPR{{Number: 9, URL: "https://github.com/o/r/pull/9", HeadRefName: "agent/issue-7", BaseRefName: "main", HeadRefOid: "abc", Checks: "SUCCESS", Mergeable: "MERGEABLE"}},
	}, rec
}

func TestGhIssueListFiltersByLabelAndState(t *testing.T) {
	cfg, rec := typedCfg(t)
	args := []string{"issue", "list", "--repo", "o/r", "--state", "open", "--label", "ready-for-agent", "--json", "number,title,labels"}
	code, out := runGh(t, cfg, args...)
	want := `[{"labels":[{"name":"ready-for-agent"}],"number":7,"title":"T"}]` + "\n"
	if code != 0 || out != want {
		t.Fatalf("got exit %d %q; want %q", code, out, want)
	}
	if got := ReadRecord(t, rec); !reflect.DeepEqual(got, [][]string{args}) {
		t.Fatalf("record = %v", got)
	}
	if _, out := runGh(t, cfg, "issue", "list", "--state", "closed", "--label", "ready-for-agent", "--json", "number"); out != `[{"number":9}]`+"\n" {
		t.Fatalf("closed list = %q", out)
	}
	if _, out := runGh(t, cfg, "issue", "list", "--json", "number"); out != `[{"number":7},{"number":8}]`+"\n" {
		t.Fatalf("unlabelled list = %q", out)
	}
}

func TestGhIssueViewEmitsRequestedFields(t *testing.T) {
	cfg, _ := typedCfg(t)
	_, out := runGh(t, cfg, "issue", "view", "7", "--repo", "o/r", "--json", "number,title,body,state,labels")
	want := `{"body":"B","labels":[{"name":"ready-for-agent"}],"number":7,"state":"OPEN","title":"T"}` + "\n"
	if out != want {
		t.Fatalf("got %q; want %q", out, want)
	}
	_, out = runGh(t, cfg, "issue", "view", "7", "--json", "comments")
	want = `{"comments":[{"author":{"login":"bob"},"body":"hi","createdAt":"2026-01-01T00:00:00Z"}]}` + "\n"
	if out != want {
		t.Fatalf("got %q; want %q", out, want)
	}
	if code, _ := runGh(t, cfg, "issue", "view", "404", "--json", "number"); code == 0 {
		t.Fatal("unknown issue should fail")
	}
}

func TestGhIssueEditAndCloseAreReplayed(t *testing.T) {
	cfg, _ := typedCfg(t)
	runGh(t, cfg, "issue", "edit", "7", "--add-label", "agent-in-progress", "--remove-label", "ready-for-agent")
	_, out := runGh(t, cfg, "issue", "view", "7", "--json", "labels")
	if want := `{"labels":[{"name":"agent-in-progress"}]}` + "\n"; out != want {
		t.Fatalf("after edit got %q; want %q", out, want)
	}
	runGh(t, cfg, "issue", "close", "7", "--reason", "completed")
	if _, out := runGh(t, cfg, "issue", "view", "7", "--json", "state"); out != `{"state":"CLOSED"}`+"\n" {
		t.Fatalf("after close got %q", out)
	}
	if _, out := runGh(t, cfg, "issue", "view", "8", "--json", "labels"); out != `{"labels":[{"name":"other"}]}`+"\n" {
		t.Fatalf("edit of #7 leaked into #8: %q", out)
	}
}

func TestGhPRList(t *testing.T) {
	cfg, _ := typedCfg(t)
	base := []string{"pr", "list", "--repo", "o/r", "--head", "agent/issue-7", "--json", "url", "--jq", `.[0].url // ""`}
	_, out := runGh(t, cfg, append(base, "--state", "open")...)
	if out != "https://github.com/o/r/pull/9\n" {
		t.Fatalf("open list = %q", out)
	}
	if _, out := runGh(t, cfg, "pr", "list", "--head", "agent/issue-8", "--state", "all", "--json", "url", "--jq", `.[0].url // ""`); out != "\n" {
		t.Fatalf("no PR = %q", out)
	}
	runGh(t, cfg, "pr", "merge", "https://github.com/o/r/pull/9", "--rebase", "--delete-branch")
	if _, out := runGh(t, cfg, append(base, "--state", "open")...); out != "\n" {
		t.Fatalf("merged PR still listed open: %q", out)
	}
	if _, out := runGh(t, cfg, append(base, "--state", "all")...); out != "https://github.com/o/r/pull/9\n" {
		t.Fatalf("merged PR missing from --state all: %q", out)
	}
}

func TestGhPRViewJq(t *testing.T) {
	cfg, _ := typedCfg(t)
	url := "https://github.com/o/r/pull/9"
	for _, tc := range []struct{ fields, jq, want string }{
		{"state", ".state", "OPEN\n"},
		{"headRefOid", ".headRefOid", "abc\n"},
		{"headRefName,baseRefName", "[.headRefName,.baseRefName]|@tsv", "agent/issue-7\tmain\n"},
	} {
		if _, out := runGh(t, cfg, "pr", "view", url, "--json", tc.fields, "--jq", tc.jq); out != tc.want {
			t.Errorf("--jq %s: got %q; want %q", tc.jq, out, tc.want)
		}
	}
	runGh(t, cfg, "pr", "merge", url, "--rebase", "--delete-branch")
	if _, out := runGh(t, cfg, "pr", "view", url, "--json", "state", "--jq", ".state"); out != "MERGED\n" {
		t.Errorf("after merge got %q", out)
	}
	if code, _ := runGh(t, cfg, "pr", "view", "https://nope/pull/1", "--json", "state", "--jq", ".state"); code == 0 {
		t.Error("unknown PR should fail")
	}
}

func TestGhAutoMergeLeavesPROpen(t *testing.T) {
	cfg, _ := typedCfg(t)
	url := "https://github.com/o/r/pull/9"
	runGh(t, cfg, "pr", "merge", url, "--auto", "--rebase", "--delete-branch")
	if _, out := runGh(t, cfg, "pr", "view", url, "--json", "state", "--jq", ".state"); out != "OPEN\n" {
		t.Fatalf("got %q", out)
	}
}

func TestGhGraphQLProbes(t *testing.T) {
	cfg, _ := typedCfg(t)
	gql := func(query string) string {
		_, out := runGh(t, cfg, "api", "graphql", "-f", "query="+query, "-f", "owner=o", "-f", "repo=r", "-F", "number=9", "--jq", "ignored")
		return out
	}
	if got := gql("{pullRequest{commits{nodes{commit{statusCheckRollup{state}}}}}}"); got != "SUCCESS\n" {
		t.Errorf("checks = %q", got)
	}
	if got := gql("{pullRequest{mergeable}}"); got != "MERGEABLE\n" {
		t.Errorf("mergeable = %q", got)
	}
	if _, out := runGh(t, cfg, "api", "graphql", "-f", "query={repository{autoMergeAllowed}}", "-f", "owner=o"); out != "false\n" {
		t.Errorf("autoMergeAllowed = %q", out)
	}
}

func TestGhRepliesBeatTypedState(t *testing.T) {
	cfg, _ := typedCfg(t)
	cfg.Replies = []GhReply{{Args: []string{"issue", "view", "7"}, Stdout: "scripted"}}
	if _, out := runGh(t, cfg, "issue", "view", "7", "--json", "number"); out != "scripted" {
		t.Fatalf("got %q", out)
	}
}

func TestGhUnknownCallsSucceedSilently(t *testing.T) {
	cfg, _ := typedCfg(t)
	for _, args := range [][]string{{"issue", "comment", "7", "--body", "x"}, {"pr", "ready", "u"}, {"api", "repos/o/r/issues/7/dependencies/blocked_by"}} {
		if code, out := runGh(t, cfg, args...); code != 0 || out != "" {
			t.Errorf("%v: got exit %d %q", args, code, out)
		}
	}
}
