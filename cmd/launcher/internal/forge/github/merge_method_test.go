package github

import (
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
)

// TestMerge_MergeMethod verifies Merge maps the MERGE_METHOD knob (via
// WithMergeMethod) onto gh pr merge's native flag, and that leaving it unset
// keeps today's --rebase default byte-identical (issue #2176).
func TestMerge_MergeMethod(t *testing.T) {
	cases := []struct {
		name       string
		opts       []ExecOption
		wantFlag   string
		forbidFlag []string
	}{
		{name: "unset defaults to rebase", opts: nil, wantFlag: "--rebase", forbidFlag: []string{"--merge", "--squash"}},
		{name: "merge", opts: []ExecOption{WithMergeMethod("merge")}, wantFlag: "--merge", forbidFlag: []string{"--rebase", "--squash"}},
		{name: "squash", opts: []ExecOption{WithMergeMethod("squash")}, wantFlag: "--squash", forbidFlag: []string{"--rebase", "--merge"}},
		{name: "rebase", opts: []ExecOption{WithMergeMethod("rebase")}, wantFlag: "--rebase", forbidFlag: []string{"--merge", "--squash"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := prependFakeGH(t, "exit 0")

			c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-", tc.opts...)
			if err := c.Merge("https://github.com/owner/repo/pull/42"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			argv := readCallArgs(t, dir, 0)
			fields := strings.Fields(argv)
			if !contains(fields, tc.wantFlag) {
				t.Fatalf("argv = %q, want %q present", argv, tc.wantFlag)
			}
			for _, forbid := range tc.forbidFlag {
				if contains(fields, forbid) {
					t.Fatalf("argv = %q, want %q absent", argv, forbid)
				}
			}
			if !contains(fields, "--delete-branch") {
				t.Fatalf("argv = %q, want --delete-branch present", argv)
			}
		})
	}
}

// TestEnqueueAutoMerge_MergeMethod verifies EnqueueAutoMerge maps the same
// MERGE_METHOD knob onto gh pr merge --auto's native flag, unset defaulting
// to --rebase.
func TestEnqueueAutoMerge_MergeMethod(t *testing.T) {
	cases := []struct {
		name       string
		opts       []ExecOption
		wantFlag   string
		forbidFlag []string
	}{
		{name: "unset defaults to rebase", opts: nil, wantFlag: "--rebase", forbidFlag: []string{"--merge", "--squash"}},
		{name: "merge", opts: []ExecOption{WithMergeMethod("merge")}, wantFlag: "--merge", forbidFlag: []string{"--rebase", "--squash"}},
		{name: "squash", opts: []ExecOption{WithMergeMethod("squash")}, wantFlag: "--squash", forbidFlag: []string{"--rebase", "--merge"}},
		{name: "rebase", opts: []ExecOption{WithMergeMethod("rebase")}, wantFlag: "--rebase", forbidFlag: []string{"--merge", "--squash"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := prependFakeGH(t, "exit 0")

			c := NewExecClient("owner/repo", forge.DispatchLabels{}, "agent/issue-", tc.opts...)
			if err := c.EnqueueAutoMerge("https://github.com/owner/repo/pull/42"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			argv := readCallArgs(t, dir, 0)
			fields := strings.Fields(argv)
			if !contains(fields, tc.wantFlag) {
				t.Fatalf("argv = %q, want %q present", argv, tc.wantFlag)
			}
			for _, forbid := range tc.forbidFlag {
				if contains(fields, forbid) {
					t.Fatalf("argv = %q, want %q absent", argv, forbid)
				}
			}
			if !contains(fields, "--auto") {
				t.Fatalf("argv = %q, want --auto present", argv)
			}
			if !contains(fields, "--delete-branch") {
				t.Fatalf("argv = %q, want --delete-branch present", argv)
			}
		})
	}
}

func contains(fields []string, s string) bool {
	for _, f := range fields {
		if f == s {
			return true
		}
	}
	return false
}
