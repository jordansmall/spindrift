package github

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/forge"
)

const readPRURL = "https://github.com/owner/repo/pull/1"

// readsNeedingARow is every tracker read a no-route state must fail by name.
var readsNeedingARow = []struct {
	name string
	read func(c *execClient) error
}{
	{"ListIssues", func(c *execClient) error { _, err := c.ListIssues(forge.Dispatchable); return err }},
	{"ListOpenIssues", func(c *execClient) error { _, err := c.ListOpenIssues(); return err }},
	{"ListIssuesWithLabels", func(c *execClient) error {
		_, err := c.ListIssuesWithLabels(forge.IssueClosed, []string{"agent-review-finding"})
		return err
	}},
	{"Issue", func(c *execClient) error { _, err := c.Issue("1"); return err }},
	{"Comments", func(c *execClient) error { _, err := c.Comments("1"); return err }},
	{"NativeDepsOf", func(c *execClient) error { _, err := c.NativeDepsOf("1"); return err }},
	{"BlocksOf", func(c *execClient) error { _, err := c.BlocksOf("1"); return err }},
	{"PriorClaimState", func(c *execClient) error { _, _, err := c.PriorClaimState("1"); return err }},
	{"SearchIssues", func(c *execClient) error {
		_, err := c.SearchIssues("one", forge.IssueOpen, "", 5)
		return err
	}},
	{"CountReady", func(c *execClient) error { _, err := c.CountReady(false); return err }},
	{"OpenPRForBranch", func(c *execClient) error { _, _, err := c.OpenPRForBranch("b"); return err }},
	{"BranchExists", func(c *execClient) error { _, err := c.BranchExists("b"); return err }},
	{"BranchProtected", func(c *execClient) error { _, err := c.BranchProtected("b"); return err }},
	{"PRForBranch", func(c *execClient) error { _, _, err := c.PRForBranch("b"); return err }},
	{"PRState", func(c *execClient) error { _, err := c.PRState(readPRURL); return err }},
	{"MergeCommit", func(c *execClient) error { _, err := c.MergeCommit(readPRURL); return err }},
	{"CheckState", func(c *execClient) error { _, err := c.CheckState(readPRURL); return err }},
	{"CheckRun", func(c *execClient) error { _, _, err := c.CheckRun(readPRURL); return err }},
	{"HeadCommitSHA", func(c *execClient) error { _, err := c.HeadCommitSHA(readPRURL); return err }},
	{"Mergeable", func(c *execClient) error { _, err := c.Mergeable(readPRURL); return err }},
	{"NeedsUpdate", func(c *execClient) error { _, err := c.NeedsUpdate(readPRURL); return err }},
	{"ListPRFiles", func(c *execClient) error { _, err := c.ListPRFiles(readPRURL); return err }},
	{"CanAutoMerge", func(c *execClient) error { _, err := c.CanAutoMerge(); return err }},
	{"Probe", func(c *execClient) error { _, err := c.Probe(); return err }},
	{"ListLabels", func(c *execClient) error { _, err := c.ListLabels(); return err }},
	{"FailureDetail", func(c *execClient) error { _, err := c.FailureDetail(readPRURL); return err }},
	{"IntrospectToken", func(*execClient) error { _, err := introspectToken("ghp_abc", connectorRepo); return err }},
}

// With no github row, under a GitHub Enterprise GH_HOST, or under a shadow row
// there is no Connector to read through, and gh is no fallback: each read fails
// with ErrNoReadRoute naming the cause and never runs gh.
func TestReads_WithoutARoutedRowFailNamedAndNeverRunGh(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) (c *execClient, calls func() int)
		want  string
	}{
		{"NoRow", func(t *testing.T) (*execClient, func() int) {
			t.Cleanup(backendrows.Snapshot())
			if err := backendrows.Use([]byte("[]"), func(string) string { return "" }); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GH_HOST", "")
			return NewExecClient(connectorRepo, testLabels), countGH(t)
		}, "no github backend row"},
		{"GHEHost", func(t *testing.T) (*execClient, func() int) {
			useGithubRowUnderHost(t, "ghe.example.com")
			t.Setenv("GH_HOST", "ghe.example.com")
			return NewExecClient(connectorRepo, testLabels), countGH(t)
		}, "GH_HOST"},
		{"ShadowRow", func(t *testing.T) (*execClient, func() int) {
			_, c, calls := harnessWithRow(t, useGithubRowShadow)
			return c, calls
		}, "shadow"},
	} {
		for _, rd := range readsNeedingARow {
			t.Run(tc.name+"/"+rd.name, func(t *testing.T) {
				c, calls := tc.setup(t)

				err := rd.read(c)

				if !errors.Is(err, ErrNoReadRoute) {
					t.Fatalf("%s error = %v, want ErrNoReadRoute", rd.name, err)
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Errorf("%s error = %q, want it to name %q", rd.name, err, tc.want)
				}
				if n := calls(); n != 0 {
					t.Errorf("%s ran gh %d times", rd.name, n)
				}
			})
		}
	}
}
