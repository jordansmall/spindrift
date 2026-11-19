package github

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backendkit"
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

			c := NewExecClient("owner/repo", forge.DispatchLabels{}, tc.opts...)
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

			c := NewExecClient("owner/repo", forge.DispatchLabels{}, tc.opts...)
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

// Under a routed row the Connector merges by rebase only, so another
// MERGE_METHOD is refused by name before any Connector or gh call rather than
// silently ignored.
func TestRoutedRow_MergeMethodOtherThanRebaseIsRefused(t *testing.T) {
	for _, method := range []string{"merge", "squash"} {
		for _, op := range []struct {
			name string
			run  func(c *execClient, url string) error
		}{
			{"Merge", func(c *execClient, u string) error { return c.Merge(u) }},
			{"EnqueueAutoMerge", func(c *execClient, u string) error { return c.EnqueueAutoMerge(u) }},
		} {
			t.Run(op.name+"/"+method, func(t *testing.T) {
				h, _, calls, _ := routedLogHarness(t, useGithubRowRouted)
				url := h.SeedLandablePR("9")
				before := h.backend.hitCount()
				c := NewExecClient(connectorRepo, testLabels, WithMergeMethod(method))

				err := op.run(c, url)

				if !errors.Is(err, ErrRoutedMergeMethod) || !strings.Contains(err.Error(), method) {
					t.Errorf("err = %v, want ErrRoutedMergeMethod naming %q", err, method)
				}
				if after := h.backend.hitCount(); after != before || calls() != 0 {
					t.Errorf("the refused call reached the backend (%d requests) or gh (%d)", after-before, calls())
				}
			})
		}
	}
}

// A routed (non-shadow) row refuses MERGE_METHOD at forge build time, as the
// forgejo row does, so a misconfigured launch fails before any Box starts;
// `primary` changes nothing. A shadow row merges natively, so it refuses
// nothing.
func TestStrategy_RoutedRowRefusesMergeMethodAtBuild(t *testing.T) {
	builds := map[string]func(backendkit.Shared) (forge.CodeForge, error){
		"NewCodeForge": func(s backendkit.Shared) (forge.CodeForge, error) {
			return Strategy.NewCodeForge(s, nil, forge.SanitizedParent{}, nil)
		},
	}
	rows := map[string]func(*testing.T, string){
		"plain":   useGithubRowAt,
		"primary": useGithubRowRouted,
		"shadow":  useGithubRowShadow,
	}
	for name, build := range builds {
		for _, tc := range []struct {
			method  string
			row     string
			refused bool
		}{
			{"squash", "plain", true},
			{"merge", "plain", true},
			{"rebase", "plain", false},
			{"", "plain", false},
			{"squash", "primary", true},
			{"rebase", "primary", false},
			{"squash", "shadow", false},
		} {
			t.Run(name+"/"+tc.method+"/"+tc.row, func(t *testing.T) {
				rows[tc.row](t, "127.0.0.1:1")

				_, err := build(backendkit.Shared{RepoSlug: connectorRepo, MergeMethod: tc.method})

				if tc.refused != errors.Is(err, ErrRoutedMergeMethod) || (!tc.refused && err != nil) {
					t.Errorf("err = %v, want refused=%v", err, tc.refused)
				}
			})
		}
	}
}

// An explicit rebase is the Connector's own method and passes.
func TestRoutedRow_MergeMethodRebasePasses(t *testing.T) {
	h, _, _, _ := routedLogHarness(t, useGithubRowRouted)
	url := h.SeedLandablePR("9")
	c := NewExecClient(connectorRepo, testLabels, WithMergeMethod("rebase"))

	if err := c.Merge(url); err != nil || !h.Merged(url) {
		t.Errorf("Merge = %v (merged %v), want a landed merge", err, h.Merged(url))
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
