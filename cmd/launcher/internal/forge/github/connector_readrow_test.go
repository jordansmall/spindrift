package github

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/forge"
)

const readPRURL = "https://github.com/owner/repo/pull/1"

// callsNeedingARow is every read and write a no-route state must fail by name.
var callsNeedingARow = []struct {
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
	{"TransitionState", func(c *execClient) error { return c.TransitionState("1", forge.Dispatchable, forge.InProgress) }},
	{"AddLabels", func(c *execClient) error { return c.AddLabels("1", []string{"x"}) }},
	{"CompleteVerdict", func(c *execClient) error { return c.CompleteVerdict("1", forge.Recommend) }},
	{"CloseMergedIssue", func(c *execClient) error { return c.CloseMergedIssue("1") }},
	{"Comment", func(c *execClient) error { return c.Comment("1", "b") }},
	{"PostIssue", func(c *execClient) error { _, err := c.PostIssue("t", "b", nil); return err }},
	{"Merge", func(c *execClient) error { return c.Merge(readPRURL) }},
	{"EnqueueAutoMerge", func(c *execClient) error { return c.EnqueueAutoMerge(readPRURL) }},
	{"MarkReady", func(c *execClient) error { return c.MarkReady(readPRURL) }},
	{"MarkDraft", func(c *execClient) error { return c.MarkDraft(readPRURL) }},
	{"CommentPR", func(c *execClient) error { return c.CommentPR(readPRURL, "b") }},
	{"CreateLabel", func(c *execClient) error { return c.CreateLabel("n", "d", "ffffff") }},
	{"Rebase", func(c *execClient) error { return c.Rebase(readPRURL) }},
	{"RebasePRHead", func(c *execClient) error { return c.RebasePRHead(readPRURL, "b") }},
	{"SeedMirror", func(c *execClient) error { return c.SeedMirror("/nonexistent") }},
	{"PushBranch", func(c *execClient) error { return c.PushBranch("/nonexistent", "ref", "b", "main") }},
	{"DeleteBranch", func(c *execClient) error { return c.DeleteBranch("b", "main") }},
	{"CreateDraftPR", func(c *execClient) error { _, _, err := c.CreateDraftPR("t", "b", "main", "b"); return err }},
	{"RelayBundle", func(c *execClient) error { return c.RelayBundle("/nonexistent", "ref") }},
	{"CommitSubjects", func(c *execClient) error { _, err := c.CommitSubjects("/nonexistent", "main", "ref"); return err }},
	{"IntrospectToken", func(c *execClient) error {
		_, err := introspectToken(c.connectors, "ghp_abc", connectorRepo)
		return err
	}},
}

// With no github row, or under a GitHub Enterprise GH_HOST
// there is no Connector to go through, and gh is no fallback: each call fails
// with ErrNoRoute naming the cause and never runs gh.
func TestCalls_WithoutARoutedRowFailNamedAndNeverRunGh(t *testing.T) {
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
			return newClient(t, connectorRepo, testLabels), countGH(t)
		}, "no github backend row"},
		{"GHEHost", func(t *testing.T) (*execClient, func() int) {
			useGithubRowUnderHost(t, "ghe.example.com")
			t.Setenv("GH_HOST", "ghe.example.com")
			return newClient(t, connectorRepo, testLabels), countGH(t)
		}, "GH_HOST"},
	} {
		for _, rd := range callsNeedingARow {
			t.Run(tc.name+"/"+rd.name, func(t *testing.T) {
				c, calls := tc.setup(t)

				err := rd.read(c)

				if !errors.Is(err, ErrNoRoute) {
					t.Fatalf("%s error = %v, want ErrNoRoute", rd.name, err)
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
