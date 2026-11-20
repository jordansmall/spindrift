package github

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/forge"
)

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

// servedByReadRow reports whether the read named name has no native path left.
func servedByReadRow(name string) bool {
	return slices.ContainsFunc(readsNeedingARow, func(r struct {
		name string
		read func(c *execClient) error
	}) bool {
		return r.name == name
	})
}
