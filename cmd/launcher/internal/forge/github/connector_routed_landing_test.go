package github

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
)

// landingBranch is the agent branch the landing writes act on.
const landingBranch = "agent/issue-9"

// routedLandingWrites is every PR write and branch delete a routed row
// routes, each driven through the production execClient. seed prepares the PR
// and branch the write acts on and returns the PR's URL; landed reports what
// the scripted backend then holds.
var routedLandingWrites = []struct {
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
	{"Merge",
		func(h *connectorHarness) string { return h.SeedLandablePR("9") },
		func(c *execClient, u string) error { return c.Merge(u) },
		// gh pr merge --delete-branch removes the head with the merge, so the
		// Connector path must too.
		func(h *connectorHarness, c *execClient, u string) bool {
			exists, err := c.BranchExists(landingBranch)
			return h.Merged(u) && h.backend.repo.Landed("9") && err == nil && !exists
		}},
	{"EnqueueAutoMerge",
		func(h *connectorHarness) string { return h.SeedOpenPR("9") },
		func(c *execClient, u string) error { return c.EnqueueAutoMerge(u) },
		func(h *connectorHarness, _ *execClient, u string) bool { return h.prView(u).enqueued }},
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

// Under a routed row each PR write and the branch delete reaches the
// Connector's backend and runs no gh.
func TestRoutedRow_LandingWritesGoThroughTheConnector(t *testing.T) {
	for _, tc := range routedLandingWrites {
		t.Run(tc.name, func(t *testing.T) {
			h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
			url := tc.seed(h)
			c := routedWriteClient()

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
func TestRoutedRow_CreateDraftPRAdoptsAnOpenPR(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	h.backend.repo.SeedBranch(landingBranch, "9")
	c := routedWriteClient()
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
func TestRoutedRow_DeleteBranchRefusesTheBaseBranch(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	before := h.backend.hitCount()

	err := routedWriteClient().DeleteBranch("main", "main")

	if err == nil {
		t.Fatal("DeleteBranch(base) succeeded, want a refusal")
	}
	if after := h.backend.hitCount(); after != before || calls() != 0 {
		t.Errorf("the refused delete reached the backend (%d requests) or gh (%d)", after-before, calls())
	}
}

// A plain row routes the landing writes through the Connector.
func TestLandingWrites_PlainRowRoutesThroughTheConnector(t *testing.T) {
	for _, tc := range routedLandingWrites {
		t.Run(tc.name, func(t *testing.T) {
			h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
			url := tc.seed(h)

			_ = tc.write(routedWriteClient(), url)

			if n := calls(); n != 0 {
				t.Errorf("%s under a plain row ran gh %d times", tc.name, n)
			}
		})
	}
}

// A Connector that fails to load fails each routed landing write with the
// load error, never reaching gh.
func TestPlainRow_LoadFailureFailsTheLandingWrite(t *testing.T) {
	for _, tc := range routedLandingWrites {
		t.Run(tc.name, func(t *testing.T) {
			h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
			url := tc.seed(h)
			boom := errors.New("github connector failed to load")
			old := loadRowConnector
			t.Cleanup(func() { loadRowConnector = old })
			loadRowConnector = func(context.Context, backendrows.Row, backendrows.Options) (*connectorhost.Connector, error) {
				return nil, boom
			}

			if err := tc.write(routedWriteClient(), url); !errors.Is(err, boom) {
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
func TestPlainRow_RefusedRouteFailsTheLandingWrite(t *testing.T) {
	for _, tc := range routedLandingWrites {
		t.Run(tc.name, func(t *testing.T) {
			h, _, calls, _ := routedLogHarness(t, func(t *testing.T, host string) {
				applyGithubRow(t, host, func(r *backendrows.Row) {
					r.Routes = r.Routes[:0:0]
					for _, rt := range allRoutes(t, host) {
						if !strings.Contains(rt.Path, "/pulls") && !strings.Contains(rt.Path, "/git/") && !strings.Contains(rt.Path, "/graphql") {
							r.Routes = append(r.Routes, rt)
						}
					}
				})
			})
			url := tc.seed(h)

			if err := tc.write(routedWriteClient(), url); !errors.Is(err, connectorhost.ErrRouteRefused) {
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
func TestRoutedRow_MissingCapabilityFailsTheLandingWrite(t *testing.T) {
	for _, tc := range []struct {
		write, capName string
		want           error
	}{
		{"CreateDraftPR", "forge.draftPRs", errNoCapability},
		{"DeleteBranch", "forge.branches", backendkit.ErrUnsupported},
		{"MarkReady", "forge.branches", backendkit.ErrUnsupported},
		{"Merge", "forge.prs", connectorforge.ErrPRForgeUnsupported},
	} {
		t.Run(tc.write+"/"+tc.capName, func(t *testing.T) {
			h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
			withoutCapability(t, tc.capName)
			for _, w := range routedLandingWrites {
				if w.name != tc.write {
					continue
				}
				if err := w.write(routedWriteClient(), w.seed(h)); !errors.Is(err, tc.want) {
					t.Errorf("err = %v, want %v", err, tc.want)
				}
			}
			if n := calls(); n != 0 {
				t.Errorf("the unsupported write ran gh %d times", n)
			}
		})
	}
}

// A Connector without forge.autoMerge fails the enqueue by name instead of
// reaching gh.
func TestRoutedRow_EnqueueAutoMergeWithoutTheCapabilityFails(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	withoutCapability(t, "forge.autoMerge")
	url := h.SeedOpenPR("9")

	err := routedWriteClient().EnqueueAutoMerge(url)

	if err == nil || !strings.Contains(err.Error(), "auto-merge is unavailable") {
		t.Errorf("err = %v, want the auto-merge capability refusal", err)
	}
	if n := calls(); n != 0 {
		t.Errorf("the unsupported enqueue ran gh %d times", n)
	}
}

// A transient merge failure surfaces as the class core's merge loop retries,
// and nothing is deleted for a merge that did not land.
func TestRoutedRow_MergeTransientFailureIsRetryable(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	url := h.SeedLandablePR("9")
	h.scriptMerge(http.StatusBadGateway)
	c := routedWriteClient()

	err := c.Merge(url)

	if !errors.Is(err, forge.ErrMergeTransient) {
		t.Fatalf("Merge on a 502 = %v, want forge.ErrMergeTransient", err)
	}
	if ok, berr := c.BranchExists(landingBranch); berr != nil || !ok {
		t.Errorf("BranchExists after a failed merge = (%v, %v), want the branch kept", ok, berr)
	}
	if n := calls(); n != 0 {
		t.Errorf("ran gh %d times", n)
	}
	if err := c.Merge(url); err != nil || !h.Merged(url) {
		t.Errorf("core's retry = %v (merged %v), want a landed merge", err, h.Merged(url))
	}
}

// The branch delete after a landed merge is best effort: a Connector that
// refuses it must not turn a merged PR into a failed settle.
func TestRoutedRow_MergeSurvivesAFailedBranchDelete(t *testing.T) {
	h, _, calls, logDir := routedLogHarness(t, func(t *testing.T, host string) {
		applyGithubRow(t, host, func(r *backendrows.Row) {
			r.Routes = r.Routes[:0:0]
			for _, rt := range allRoutes(t, host) {
				if rt.Method != "DELETE" {
					r.Routes = append(r.Routes, rt)
				}
			}
		})
	})
	url := h.SeedLandablePR("9")

	if err := routedWriteClient().Merge(url); err != nil {
		t.Fatalf("Merge = %v, want nil: the PR landed", err)
	}
	if !h.Merged(url) {
		t.Error("the PR did not merge")
	}
	if n := calls(); n != 0 {
		t.Errorf("ran gh %d times", n)
	}
	// stderr carries the same failure as a warning; only the log is asserted.
	logs := routedFiles(t, logDir, ".log")
	if len(logs) != 1 {
		t.Fatalf("primary logs = %v, want one recording the failed delete", logs)
	}
	raw, err := os.ReadFile(logs[0])
	if err != nil || !strings.Contains(string(raw), "delete a merged PR's head branch") {
		t.Errorf("primary log = %q (%v), want the failed head-branch delete", raw, err)
	}
}

// A merged same-repository PR has its head branch deleted.
func TestRoutedRow_MergeDeletesASameRepoHead(t *testing.T) {
	h, _, _, _ := routedLogHarness(t, useGithubRowAt)
	url := h.SeedLandablePR("9")

	if err := routedWriteClient().Merge(url); err != nil {
		t.Fatalf("Merge = %v, want nil", err)
	}
	if got, want := h.deletedRefs(), []string{"refs/heads/" + landingBranch}; !slices.Equal(got, want) {
		t.Errorf("deleted refs = %v, want %v", got, want)
	}
}

// A cross-repository PR's head is not in the base repository, so the merge
// deletes nothing: a same-named base-repo branch would be someone else's.
func TestRoutedRow_MergeLeavesACrossRepoHeadAlone(t *testing.T) {
	h, _, _, logDir := routedLogHarness(t, useGithubRowAt)
	url := h.SeedLandableForkPR("9")

	if err := routedWriteClient().Merge(url); err != nil {
		t.Fatalf("Merge = %v, want nil", err)
	}
	if !h.Merged(url) {
		t.Error("the PR did not merge")
	}
	if got := h.deletedRefs(); len(got) != 0 {
		t.Errorf("deleted refs = %v, want none for a fork PR", got)
	}
	if ok, err := routedWriteClient().BranchExists(landingBranch); err != nil || !ok {
		t.Errorf("BranchExists(%s) = (%v, %v), want the base repo's same-named branch kept", landingBranch, ok, err)
	}
	if logs := routedFiles(t, logDir, ".log"); len(logs) != 0 {
		t.Errorf("primary logs = %v, want none", logs)
	}
}

// A head the repository already deleted is done, not a fault: nothing is
// recorded for a clean primary run.
func TestRoutedRow_MergeWithTheHeadAlreadyGoneRecordsNothing(t *testing.T) {
	h, _, calls, logDir := routedLogHarness(t, useGithubRowAt)
	url := h.SeedOpenPR("9")

	if err := routedWriteClient().Merge(url); err != nil {
		t.Fatalf("Merge = %v, want nil", err)
	}
	if !h.Merged(url) {
		t.Error("the PR did not merge")
	}
	if n := calls(); n != 0 {
		t.Errorf("ran gh %d times", n)
	}
	if logs := routedFiles(t, logDir, ".log"); len(logs) != 0 {
		t.Errorf("primary logs = %v, want none for an already-deleted head", logs)
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

// The PR-body Closes gate is core policy; the Connector carries the body to the
// backend byte for byte, so the keyword survives the host-side open.
func TestRoutedRow_CreateDraftPRCarriesTheClosesBodyIntact(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	h.backend.repo.SeedBranch(landingBranch, "9")
	const body = "Fixes the thing.\n\nCloses #9\n"

	url, created, err := routedWriteClient().CreateDraftPR("title", body, "main", landingBranch)

	if err != nil || !created {
		t.Fatalf("CreateDraftPR = (%q, %v, %v), want a created PR", url, created, err)
	}
	if got := h.prView(url); got.body != body || got.title != "title" || !got.draft {
		t.Errorf("backend PR = title %q body %q draft %v, want the supplied body on a draft", got.title, got.body, got.draft)
	}
	if n := calls(); n != 0 {
		t.Errorf("ran gh %d times", n)
	}
}

// The butler patch PR path: push the branch, open the draft PR for it, then
// delete the branch again when the patch is abandoned, all through the
// Connector and core's git with no gh call.
func TestRoutedRow_ButlerPatchPathRunsThroughTheConnector(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	src := t.TempDir()
	forgetest.Run(t, "", "clone", h.backend.repo.Bare, src)
	forgetest.Run(t, src, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src, "patch.txt"), "patch\n")
	forgetest.Run(t, src, "add", "patch.txt")
	forgetest.Run(t, src, "commit", "-m", "patch")
	c := routedGitClient()

	if err := c.PushBranch(src, "work", landingBranch, "main"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	url, created, err := c.CreateDraftPR("patch", "Closes #9", "main", landingBranch)
	if err != nil || !created {
		t.Fatalf("CreateDraftPR = (%q, %v, %v), want a created PR", url, created, err)
	}
	if pr, ok, err := c.OpenPRForBranch(landingBranch); err != nil || !ok || pr.URL != url {
		t.Errorf("OpenPRForBranch = (%+v, %v, %v), want the draft PR %q", pr, ok, err, url)
	}
	if err := c.DeleteBranch(landingBranch, "main"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}

	if ok, err := c.BranchExists(landingBranch); err != nil || ok {
		t.Errorf("BranchExists after DeleteBranch = (%v, %v), want (false, nil)", ok, err)
	}
	if n := calls(); n != 0 {
		t.Errorf("the patch path ran gh %d times under a primary row", n)
	}
}

// A push-only Connector composes without a PR forge: Merge fails by the named
// capability instead of reaching the push-only forge's Merge.
func TestRoutedRow_MergeOnAPushOnlyConnectorNamesTheMissingCapability(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	withoutCapability(t, "forge.prs")
	old := loadRowConnector
	loadRowConnector = func(ctx context.Context, row backendrows.Row, o backendrows.Options) (*connectorhost.Connector, error) {
		conn, err := old(ctx, row, o)
		if conn != nil {
			conn.Capabilities = append(conn.Capabilities, "forge.pushOnly")
		}
		return conn, err
	}
	url := h.SeedLandablePR("9")

	err := routedWriteClient().Merge(url)

	if !errors.Is(err, backendkit.ErrUnsupported) || !strings.Contains(err.Error(), "forge.prs") {
		t.Errorf("Merge = %v, want unsupported naming forge.prs", err)
	}
	if h.Merged(url) || calls() != 0 {
		t.Errorf("merged = %v, gh calls = %d, want neither", h.Merged(url), calls())
	}
}
