package github

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// prependFakeGit writes a counting-wrapper git script to a temp dir,
// prepends that dir to PATH, and returns the dir. Each invocation of the
// fake git records its argv to git-call-NN.txt (zero-indexed) inside the
// dir and exits 0 unconditionally, so every git subcommand Rebase and
// gitplumbing.GitForcePush issue (checkout, rebase/merge, push
// --force-with-lease) succeeds. This mirrors prependFakeGH (exec_test.go)
// but for the git binary, which Rebase also shells out to.
func prependFakeGit(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
n=$(ls "%s"/git-call-*.txt 2>/dev/null | wc -l)
printf '%%s\n' "$@" > "%s/git-call-$(printf '%%02d' $n).txt"
exit 0
`, dir, dir)
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("PATH")
	t.Cleanup(func() { os.Setenv("PATH", old) })
	os.Setenv("PATH", dir+":"+old)
	return dir
}

// readGitCallArgs reads every recorded fake-git invocation and returns each
// as a space-joined string, in call order.
func readGitCallArgs(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "git-call-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, m := range matches {
		raw, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		calls = append(calls, strings.Join(strings.Split(strings.TrimSpace(string(raw)), "\n"), " "))
	}
	return calls
}

// TestRebase_SyncMethod verifies Rebase maps the sync method knob (via
// WithSyncMethod) onto the git verb it uses to bring the PR branch up to
// date with its base, and that leaving it unset keeps today's rebase
// behavior byte-identical (mirroring TestMerge_MergeMethod, issue #2176).
func TestRebase_SyncMethod(t *testing.T) {
	cases := []struct {
		name       string
		opts       []ExecOption
		wantVerb   string
		forbidVerb string
	}{
		{name: "unset defaults to rebase", opts: nil, wantVerb: "rebase", forbidVerb: "merge"},
		{name: "rebase", opts: []ExecOption{WithSyncMethod("rebase")}, wantVerb: "rebase", forbidVerb: "merge"},
		{name: "merge", opts: []ExecOption{WithSyncMethod("merge")}, wantVerb: "merge", forbidVerb: "rebase"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gitDir := prependFakeGit(t)
			prependFakeGH(t, `if [ "$1" = "pr" ]; then
  printf 'feature\tmain\n'
fi
exit 0
`)

			c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-", tc.opts...)
			if err := c.Rebase("https://github.com/owner/repo/pull/42"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			calls := readGitCallArgs(t, gitDir)

			found := false
			for _, argv := range calls {
				fields := strings.Fields(argv)
				for i, f := range fields {
					if f == tc.wantVerb && i+1 < len(fields) && fields[i+1] == "origin/main" {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("git calls = %v, want a %q origin/main call", calls, tc.wantVerb)
			}

			for _, argv := range calls {
				fields := strings.Fields(argv)
				for i, f := range fields {
					if f == tc.forbidVerb && i+1 < len(fields) && fields[i+1] == "origin/main" {
						t.Fatalf("git calls = %v, want no %q origin/main call", calls, tc.forbidVerb)
					}
				}
			}
		})
	}
}
