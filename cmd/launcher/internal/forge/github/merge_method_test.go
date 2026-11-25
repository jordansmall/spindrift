package github

import (
	"errors"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/forge"
)

// Under a routed row the Connector merges by rebase only, so another
// MERGE_METHOD is refused by name before any Connector or gh call rather than
// silently ignored.
func TestConnectorRow_MergeMethodOtherThanRebaseIsRefused(t *testing.T) {
	for _, method := range []string{"merge", "squash"} {
		for _, op := range []struct {
			name string
			run  func(c *execClient, url string) error
		}{
			{"Merge", func(c *execClient, u string) error { return c.Merge(u) }},
			{"EnqueueAutoMerge", func(c *execClient, u string) error { return c.EnqueueAutoMerge(u) }},
		} {
			t.Run(op.name+"/"+method, func(t *testing.T) {
				h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
				url := h.SeedLandablePR("9")
				before := h.backend.hitCount()
				c := newClient(t, connectorRepo, testLabels, WithMergeMethod(method))

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

// A routed row refuses MERGE_METHOD at forge build time, as the forgejo row
// does, so a misconfigured launch fails before any Box starts.
func TestStrategy_RoutedRowRefusesMergeMethodAtBuild(t *testing.T) {
	for _, tc := range []struct {
		method  string
		refused bool
	}{
		{"squash", true},
		{"merge", true},
		{"rebase", false},
		{"", false},
	} {
		t.Run(tc.method, func(t *testing.T) {
			useGithubRowAt(t, "127.0.0.1:1")

			_, err := Strategy.NewCodeForge(backendkit.Shared{RepoSlug: connectorRepo, MergeMethod: tc.method, Connectors: testRuntime(t)}, nil, forge.SanitizedParent{}, nil)

			if tc.refused != errors.Is(err, ErrRoutedMergeMethod) || (!tc.refused && err != nil) {
				t.Errorf("err = %v, want refused=%v", err, tc.refused)
			}
		})
	}
}

// An explicit rebase is the Connector's own method and passes.
func TestConnectorRow_MergeMethodRebasePasses(t *testing.T) {
	h, _, _, _ := routedLogHarness(t, useGithubRowAt)
	url := h.SeedLandablePR("9")
	c := newClient(t, connectorRepo, testLabels, WithMergeMethod("rebase"))

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
