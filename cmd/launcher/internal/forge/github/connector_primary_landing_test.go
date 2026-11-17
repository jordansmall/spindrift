package github

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorhost"
)

// landingBranch is the agent branch the landing writes act on.
const landingBranch = "agent/issue-9"

// primaryLandingWrites is every PR write and branch delete a primary row
// routes, each driven through the production execClient. seed prepares the PR
// and branch the write acts on and returns the PR's URL; landed reports what
// the scripted backend then holds.
var primaryLandingWrites = []struct {
	name   string
	seed   func(h *connectorHarness) string
	write  func(c *execClient, prURL string) error
	landed func(h *connectorHarness, c *execClient, prURL string) bool
}{
	{"MarkReady",
		func(h *connectorHarness) string { return h.SeedDraftPR("9") },
		func(c *execClient, u string) error { return c.MarkReady(u) },
		func(h *connectorHarness, _ *execClient, u string) bool { return !h.IsDraft(u) }},
	{"MarkDraft",
		func(h *connectorHarness) string { return h.SeedOpenPR("9") },
		func(c *execClient, u string) error { return c.MarkDraft(u) },
		func(h *connectorHarness, _ *execClient, u string) bool { return h.IsDraft(u) }},
	{"CommentPR",
		func(h *connectorHarness) string { return h.SeedOpenPR("9") },
		func(c *execClient, u string) error { return c.CommentPR(u, "needs a human") },
		func(h *connectorHarness, _ *execClient, u string) bool {
			return slices.Equal(h.comments(u), []string{"needs a human"})
		}},
	{"CreateDraftPR",
		func(h *connectorHarness) string { h.backend.repo.SeedBranch(landingBranch, "9"); return "" },
		func(c *execClient, _ string) error {
			_, created, err := c.CreateDraftPR("title", "body", "main", landingBranch)
			if err == nil && !created {
				err = errors.New("CreateDraftPR adopted a PR on a branch with none")
			}
			return err
		},
		func(_ *connectorHarness, c *execClient, _ string) bool {
			u, ok, err := c.OpenPRForBranch(landingBranch)
			return err == nil && ok && u.URL != ""
		}},
	{"DeleteBranch",
		func(h *connectorHarness) string { h.backend.repo.SeedBranch(landingBranch, "9"); return "" },
		func(c *execClient, _ string) error { return c.DeleteBranch(landingBranch, "main") },
		func(_ *connectorHarness, c *execClient, _ string) bool {
			ok, err := c.BranchExists(landingBranch)
			return err == nil && !ok
		}},
}

// Under a primary row each PR write and the branch delete reaches the
// Connector's backend and runs no gh.
func TestPrimaryRow_LandingWritesGoThroughTheConnector(t *testing.T) {
	for _, tc := range primaryLandingWrites {
		t.Run(tc.name, func(t *testing.T) {
			h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
			url := tc.seed(h)
			c := primaryWriteClient()

			if err := tc.write(c, url); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}

			if n := calls(); n != 0 {
				t.Errorf("%s ran gh %d times under a primary row", tc.name, n)
			}
			if !tc.landed(h, c, url) {
				t.Errorf("%s did not reach the backend", tc.name)
			}
		})
	}
}

// A hand-off the Box or an earlier attempt already opened is adopted, not
// reported as a failure, and created=false tells settle the title and body are
// not the ones supplied.
func TestPrimaryRow_CreateDraftPRAdoptsAnOpenPR(t *testing.T) {
	h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
	h.backend.repo.SeedBranch(landingBranch, "9")
	c := primaryWriteClient()
	first, created, err := c.CreateDraftPR("title", "body", "main", landingBranch)
	if err != nil || !created {
		t.Fatalf("CreateDraftPR = (%q, %v, %v), want a created PR", first, created, err)
	}

	again, created, err := c.CreateDraftPR("other", "other", "main", landingBranch)

	if err != nil || created || again != first {
		t.Errorf("second CreateDraftPR = (%q, %v, %v), want (%q, false, nil)", again, created, err, first)
	}
	if n := calls(); n != 0 {
		t.Errorf("CreateDraftPR ran gh %d times under a primary row", n)
	}
}

// DeleteBranch refuses the base branch before any call is made.
func TestPrimaryRow_DeleteBranchRefusesTheBaseBranch(t *testing.T) {
	h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
	h.backend.mu.Lock()
	before := h.backend.hits
	h.backend.mu.Unlock()

	err := primaryWriteClient().DeleteBranch("main", "main")

	if err == nil {
		t.Fatal("DeleteBranch(base) succeeded, want a refusal")
	}
	h.backend.mu.Lock()
	defer h.backend.mu.Unlock()
	if h.backend.hits != before || calls() != 0 {
		t.Errorf("the refused delete reached the backend (%d requests) or gh (%d)", h.backend.hits-before, calls())
	}
}

// Without a primary row the same writes still run gh.
func TestLandingWrites_StayOnGhWithoutAPrimaryRow(t *testing.T) {
	for rowName, useRow := range map[string]func(*testing.T, string){"plain": useGithubRowAt, "shadow": useGithubRowShadow} {
		for _, tc := range primaryLandingWrites {
			t.Run(rowName+"/"+tc.name, func(t *testing.T) {
				h, _, calls, _ := primaryHarness(t, useRow)
				url := tc.seed(h)

				_ = tc.write(primaryWriteClient(), url)

				if calls() == 0 {
					t.Errorf("%s under a %s row never reached gh", tc.name, rowName)
				}
			})
		}
	}
}

// A Connector that fails to load fails each primary landing write with the
// load error, never reaching gh.
func TestPrimaryRow_LoadFailureFailsTheLandingWrite(t *testing.T) {
	for _, tc := range primaryLandingWrites {
		t.Run(tc.name, func(t *testing.T) {
			h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
			url := tc.seed(h)
			boom := errors.New("github connector failed to load")
			old := loadRowConnector
			t.Cleanup(func() { loadRowConnector = old })
			loadRowConnector = func(context.Context, backendrows.Row, backendrows.Options) (*connectorhost.Connector, error) {
				return nil, boom
			}

			if err := tc.write(primaryWriteClient(), url); !errors.Is(err, boom) {
				t.Errorf("err = %v, want the load error", err)
			}
			if n := calls(); n != 0 {
				t.Errorf("the failed write ran gh %d times", n)
			}
		})
	}
}

// A landing write outside the row's Routes is refused by the host and fails
// with that refusal, never falling back to gh.
func TestPrimaryRow_RefusedRouteFailsTheLandingWrite(t *testing.T) {
	for _, tc := range primaryLandingWrites {
		t.Run(tc.name, func(t *testing.T) {
			h, _, calls, _ := primaryHarness(t, func(t *testing.T, host string) {
				applyGithubRow(t, host, func(r *backendrows.Row) {
					r.Primary = true
					r.Routes = r.Routes[:0:0]
					for _, rt := range allRoutes(t, host) {
						if !strings.Contains(rt.Path, "/pulls") && !strings.Contains(rt.Path, "/git/") && !strings.Contains(rt.Path, "/graphql") {
							r.Routes = append(r.Routes, rt)
						}
					}
				})
			})
			url := tc.seed(h)

			if err := tc.write(primaryWriteClient(), url); !errors.Is(err, connectorhost.ErrRouteRefused) {
				t.Errorf("err = %v, want the route refusal", err)
			}
			if n := calls(); n != 0 {
				t.Errorf("the refused write ran gh %d times", n)
			}
		})
	}
}

// A Connector without the draft-PR capability fails CreateDraftPR with the
// named capability error instead of reaching gh; one that is not a forge at
// all is unsupported.
func TestPrimaryRow_MissingCapabilityFailsTheLandingWrite(t *testing.T) {
	for _, tc := range []struct {
		write, capName string
		want           error
	}{
		{"CreateDraftPR", "forge.draftPRs", errNoCapability},
		{"DeleteBranch", "forge.branches", backendkit.ErrUnsupported},
		{"MarkReady", "forge.branches", backendkit.ErrUnsupported},
	} {
		t.Run(tc.write+"/"+tc.capName, func(t *testing.T) {
			h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
			withoutCapability(t, tc.capName)
			for _, w := range primaryLandingWrites {
				if w.name != tc.write {
					continue
				}
				if err := w.write(primaryWriteClient(), w.seed(h)); !errors.Is(err, tc.want) {
					t.Errorf("err = %v, want %v", err, tc.want)
				}
			}
			if n := calls(); n != 0 {
				t.Errorf("the unsupported write ran gh %d times", n)
			}
		})
	}
}

// The git credential is read from the row's env at each operation, and an
// unset one is named instead of sending git an empty password.
func TestGitCredential_ReadsTheRowEnvAtUse(t *testing.T) {
	row := backendrows.Row{Name: "github", CredentialEnv: tokenCredentialEnv}

	t.Setenv(tokenCredentialEnv, "ghp_one")
	cred, err := gitCredential(row)
	if err != nil || cred.Username != "x-access-token" || cred.Password != "ghp_one" {
		t.Fatalf("gitCredential = (%+v, %v), want x-access-token / ghp_one", cred, err)
	}

	t.Setenv(tokenCredentialEnv, "ghp_rotated")
	if cred, _ := gitCredential(row); cred.Password != "ghp_rotated" {
		t.Errorf("gitCredential after rotation = %q, want ghp_rotated", cred.Password)
	}

	t.Setenv(tokenCredentialEnv, "")
	if _, err := gitCredential(row); err == nil || !strings.Contains(err.Error(), tokenCredentialEnv+" is not set") {
		t.Errorf("gitCredential with the env unset = %v, want %q", err, tokenCredentialEnv+" is not set")
	}
}
