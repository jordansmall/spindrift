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
