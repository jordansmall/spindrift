package github

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"spindrift.dev/launcher/internal/backend/backendrows"
	"spindrift.dev/launcher/internal/backendkit"
	"spindrift.dev/launcher/internal/connectorforge"
	"spindrift.dev/launcher/internal/connectorhost"
	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/ledger"
)

// primaryGitClient is the production execClient for a primary row, with the
// base branch and committer identity core's own git needs.
func primaryGitClient(opts ...ExecOption) *execClient {
	return NewExecClient(connectorRepo, testLabels, append([]ExecOption{
		WithBaseBranch("main"), WithCommitIdentity("Test Bot", "bot@example.com"),
	}, opts...)...)
}

func primaryReadOnlyForge(opts ...ExecOption) *readOnlyCodeForge {
	return &readOnlyCodeForge{execClient: primaryGitClient(opts...)}
}

// A git step presents the row env's token as core's credential, and no gh
// credential helper ran.
func requireCoreCredential(t *testing.T, seen string, calls func() int) {
	t.Helper()
	log, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("core's git never ran with a credential: %v", err)
	}
	if !strings.Contains(string(log), "x-access-token:"+connectorToken) {
		t.Errorf("git ran without the row's token: %q", log)
	}
	if n := calls(); n != 0 {
		t.Errorf("gh ran %d times under a primary row", n)
	}
}

// Under a primary row the read-only Box's bundle relay and commit subjects run
// in core's git with the row's credential.
func TestPrimaryRow_BundleRelayRunsInCoreGit(t *testing.T) {
	h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
	seen := gitCredentialLog(t)
	f := primaryReadOnlyForge()
	const ref = "agent/issue-9002"
	outbox := t.TempDir()
	want := forgetest.SeedRelayBundle(t, h.backend.repo.Bare, "main", outbox, ref)

	subjects, err := f.CommitSubjects(outbox, "main", ref)
	if err != nil || len(subjects) != 1 || subjects[0] != "feature" {
		t.Errorf("CommitSubjects = %v, %v; want [feature]", subjects, err)
	}
	if err := f.RelayBundle(outbox, ref); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}

	if got := forgetest.RevParse(t, h.backend.repo.Bare, "refs/heads/"+ref); got != want {
		t.Errorf("refs/heads/%s = %s, want %s", ref, got, want)
	}
	requireCoreCredential(t, seen, calls)
}

// A Box that wrote no bundle is the benign case under primary too.
func TestPrimaryRow_RelayOfNoBundleIsBenign(t *testing.T) {
	primaryHarness(t, useGithubRowPrimary)
	f := primaryReadOnlyForge()

	if err := f.RelayBundle(t.TempDir(), "agent/issue-1"); !errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("RelayBundle(empty outbox) = %v, want forge.ErrBundleNotFound", err)
	}
	if _, err := f.CommitSubjects(t.TempDir(), "main", "agent/issue-1"); !errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("CommitSubjects(empty outbox) = %v, want forge.ErrBundleNotFound", err)
	}
}

// PushBranch creates the branch through core's git, create-only.
func TestPrimaryRow_PushBranchRunsInCoreGit(t *testing.T) {
	h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
	seen := gitCredentialLog(t)
	src := t.TempDir()
	forgetest.Run(t, "", "clone", h.backend.repo.Bare, src)
	forgetest.Run(t, src, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src, "feature.txt"), "feature\n")
	forgetest.Run(t, src, "add", "feature.txt")
	forgetest.Run(t, src, "commit", "-m", "feature")
	c := primaryGitClient()

	if err := c.PushBranch(src, "work", landingBranch, "main"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}

	if got, want := forgetest.RevParse(t, h.backend.repo.Bare, "refs/heads/"+landingBranch), forgetest.RevParse(t, src, "work"); got != want {
		t.Errorf("refs/heads/%s = %s, want %s", landingBranch, got, want)
	}
	requireCoreCredential(t, seen, calls)
	other := t.TempDir()
	forgetest.Run(t, "", "clone", h.backend.repo.Bare, other)
	forgetest.WriteFile(t, filepath.Join(other, "other.txt"), "other\n")
	forgetest.Run(t, other, "add", "other.txt")
	forgetest.Run(t, other, "commit", "-m", "other")
	if err := c.PushBranch(other, "HEAD", landingBranch, "main"); err == nil {
		t.Error("PushBranch onto an existing branch succeeded, want a refusal")
	}
}

// The mirror is seeded by core's git with the row's credential, which persists
// nowhere in the repo the Box mounts.
func TestPrimaryRow_SeedMirrorRunsInCoreGit(t *testing.T) {
	h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
	seen := gitCredentialLog(t)
	dir := filepath.Join(t.TempDir(), "mirror")

	if err := primaryGitClient().SeedMirror(dir); err != nil {
		t.Fatalf("SeedMirror: %v", err)
	}

	if got, want := forgetest.RevParse(t, dir, "main"), forgetest.RevParse(t, h.backend.repo.Bare, "main"); got != want {
		t.Errorf("mirror main = %s, want %s", got, want)
	}
	requireCoreCredential(t, seen, calls)
	cfg, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cfg), connectorToken) || strings.Contains(string(cfg), "credential") {
		t.Errorf("mirror config carries the credential:\n%s", cfg)
	}
}

// splitWebHost moves the repository's web URL, and so its clone URL, onto
// GH_HOST while the API stays on the backend's host, as github.com and
// api.github.com split on GitHub. The row's Routes declare only the API host.
func splitWebHost(t *testing.T, h *connectorHarness) {
	t.Helper()
	const web = "github.example"
	h.backend.mu.Lock()
	h.backend.web = "https://" + web + "/" + connectorRepo
	h.backend.mu.Unlock()
	t.Setenv("GH_HOST", web)
	t.Setenv("GIT_CONFIG_VALUE_0", h.backend.web+".git")
}

// The mirror seeds when git's host is GH_HOST and no declared route names it.
func TestPrimaryRow_SeedMirrorWhenTheWebHostIsNotAnAPIRoute(t *testing.T) {
	h, _, _, _ := primaryHarness(t, useGithubRowPrimary)
	splitWebHost(t, h)
	dir := filepath.Join(t.TempDir(), "mirror")

	if err := primaryGitClient().SeedMirror(dir); err != nil {
		t.Fatalf("SeedMirror: %v", err)
	}
	if got, want := forgetest.RevParse(t, dir, "main"), forgetest.RevParse(t, h.backend.repo.Bare, "main"); got != want {
		t.Errorf("mirror main = %s, want %s", got, want)
	}
}

// No git operation a primary row composes refuses a remote on GH_HOST.
func TestPrimaryRow_GitOpsAcceptARemoteOnGHHost(t *testing.T) {
	for _, op := range primaryGitOps {
		t.Run(op.name, func(t *testing.T) {
			h, _, _, _ := primaryHarness(t, useGithubRowPrimary)
			splitWebHost(t, h)
			if err := op.run(h, t); errors.Is(err, connectorforge.ErrRemoteHost) {
				t.Errorf("err = %v, want the GH_HOST remote accepted", err)
			}
		})
	}
}

func mergeCommits(t *testing.T, bare, branch string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", bare, "rev-list", "--merges", "--count", "refs/heads/"+branch).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// Rebase under a primary row syncs the PR's head onto core's base branch in
// core's git, with SYNC_METHOD choosing between rebase and merge, and a real
// conflict is forge.ErrMergeConflict.
func TestPrimaryRow_RebaseRunsInCoreGit(t *testing.T) {
	for _, tc := range []struct {
		method     string
		wantMerges string
	}{{"rebase", "0"}, {"", "0"}, {"merge", "1"}} {
		t.Run("sync="+tc.method, func(t *testing.T) {
			h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
			seen := gitCredentialLog(t)
			url := h.SeedLandablePR("11")
			h.backend.repo.AdvanceBase()
			branch := forge.AgentBranchName(h.BranchPrefix(), "11")

			if err := primaryGitClient(WithSyncMethod(tc.method)).Rebase(url); err != nil {
				t.Fatalf("Rebase: %v", err)
			}

			if !h.backend.repo.Rebased(branch) {
				t.Error("the PR branch does not contain the advanced base")
			}
			if got := mergeCommits(t, h.backend.repo.Bare, branch); got != tc.wantMerges {
				t.Errorf("merge commits on %s = %s, want %s", branch, got, tc.wantMerges)
			}
			requireCoreCredential(t, seen, calls)
		})
	}
}

// A conflicting sync under a primary row surfaces forge.ErrMergeConflict, as
// the native path does, and never reaches gh.
func TestPrimaryRow_RebaseConflictIsErrMergeConflict(t *testing.T) {
	for _, method := range []string{"rebase", "merge"} {
		t.Run(method, func(t *testing.T) {
			h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
			url := h.SeedLandablePR("12")
			h.backend.repo.ConflictBase("12")

			if err := primaryGitClient(WithSyncMethod(method)).Rebase(url); !errors.Is(err, forge.ErrMergeConflict) {
				t.Errorf("Rebase = %v, want forge.ErrMergeConflict", err)
			}
			if n := calls(); n != 0 {
				t.Errorf("gh ran %d times", n)
			}
		})
	}
}

// primaryGitOps is each git operation a primary row routes, with what it needs
// seeded.
var primaryGitOps = []struct {
	name string
	run  func(h *connectorHarness, t *testing.T) error
}{
	{"RelayBundle", func(h *connectorHarness, t *testing.T) error {
		outbox := t.TempDir()
		forgetest.SeedRelayBundle(t, h.backend.repo.Bare, "main", outbox, landingBranch)
		return primaryReadOnlyForge().RelayBundle(outbox, landingBranch)
	}},
	{"CommitSubjects", func(h *connectorHarness, t *testing.T) error {
		outbox := t.TempDir()
		forgetest.SeedRelayBundle(t, h.backend.repo.Bare, "main", outbox, landingBranch)
		_, err := primaryReadOnlyForge().CommitSubjects(outbox, "main", landingBranch)
		return err
	}},
	{"PushBranch", func(h *connectorHarness, t *testing.T) error {
		return primaryGitClient().PushBranch(t.TempDir(), "work", landingBranch, "main")
	}},
	{"Rebase", func(h *connectorHarness, t *testing.T) error {
		return primaryGitClient().Rebase(h.SeedLandablePR("13"))
	}},
	{"SeedMirror", func(h *connectorHarness, t *testing.T) error {
		return primaryGitClient().SeedMirror(filepath.Join(t.TempDir(), "mirror"))
	}},
}

// A Connector that fails to load, or a route the host refuses, fails each git
// operation with that error; none falls back to gh.
func TestPrimaryRow_GitOpsFailByName(t *testing.T) {
	boom := errors.New("github connector failed to load")
	for _, op := range primaryGitOps {
		t.Run(op.name+"/load", func(t *testing.T) {
			h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
			old := loadRowConnector
			t.Cleanup(func() { loadRowConnector = old })
			loadRowConnector = func(context.Context, backendrows.Row, backendrows.Options) (*connectorhost.Connector, error) {
				return nil, boom
			}

			if err := op.run(h, t); !errors.Is(err, boom) {
				t.Errorf("err = %v, want the load error", err)
			}
			if n := calls(); n != 0 {
				t.Errorf("ran gh %d times", n)
			}
		})
		t.Run(op.name+"/refused", func(t *testing.T) {
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

			if err := op.run(h, t); !errors.Is(err, connectorhost.ErrRouteRefused) {
				t.Errorf("err = %v, want the route refusal", err)
			}
			if n := calls(); n != 0 {
				t.Errorf("ran gh %d times", n)
			}
		})
	}
}

// A primary row whose credential env is unset names it rather than sending git
// an empty password.
func TestPrimaryRow_GitOpsNameAnUnsetCredential(t *testing.T) {
	_, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
	t.Setenv(tokenCredentialEnv, "")

	err := primaryGitClient().SeedMirror(filepath.Join(t.TempDir(), "mirror"))

	if err == nil || !strings.Contains(err.Error(), tokenCredentialEnv+" is not set") {
		t.Errorf("SeedMirror = %v, want the unset credential named", err)
	}
	if n := calls(); n != 0 {
		t.Errorf("ran gh %d times", n)
	}
}

// primaryHostMediationHarness is the host-mediation contract's harness with the
// production read-only forge, under a primary row, as the code forge. The
// tracker half is a forge.Fake built before primaryHarness changes directory,
// which the fake's first use depends on.
type primaryHostMediationHarness struct {
	*composedHostMediationHarness
	cf forge.CodeForge
	tr forge.IssueTracker
}

func (h *primaryHostMediationHarness) CodeForge() forge.CodeForge  { return h.cf }
func (h *primaryHostMediationHarness) Tracker() forge.IssueTracker { return h.tr }

// The host-mediation contract holds for the production forge under a primary
// row, with core's git doing the relay and no gh call.
func TestPrimaryRow_HostMediationContract(t *testing.T) {
	fake := forge.NewFake()
	tr := fake.AsIssueFiler()
	_, _ = fake.ListOpenIssues() // loads the shared fake Connector from the package directory
	h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
	base := &composedHostMediationHarness{
		connectorHarness: h, fake: fake,
		relayedSHA: map[string]string{}, seeded: map[string]bool{}, prSeq: 1500,
	}

	forgetest.RunHostMediationContract(t, &primaryHostMediationHarness{base, primaryReadOnlyForge(), tr})

	if n := calls(); n != 0 {
		t.Errorf("the contract ran gh %d times under a primary row", n)
	}
}

// The butler Ledger's site under a primary row is the Connector's clone URL
// and the row's credential, with no gh credential helper: the Ledger's own git
// pushes a Ledger entry to the repository with them.
func TestPrimaryRow_LedgerSiteIsConnectorCloneURLAndCoreCredential(t *testing.T) {
	h, _, calls, _ := primaryHarness(t, useGithubRowPrimary)
	seen := gitCredentialLog(t)

	site, err := Strategy.NewLedger(backendkit.Shared{RepoSlug: connectorRepo, Labels: testLabels}, nil)
	if err != nil {
		t.Fatalf("NewLedger: %v", err)
	}

	if len(site.GitArgs) != 0 {
		t.Errorf("GitArgs = %v, want none under a primary row", site.GitArgs)
	}
	if site.Credential.Username != "x-access-token" || site.Credential.Password != connectorToken {
		t.Errorf("Credential = %+v, want the row's token", site.Credential)
	}
	r, err := ledger.NewRemote(t.TempDir(), site.URL, site.Credential, site.GitArgs...)
	if err != nil {
		t.Fatalf("a Ledger over the site cannot reach the repository: %v", err)
	}
	const chore = "chore-primary"
	commit, err := r.Append(chore, "", ledger.State{Phase: ledger.Claimed}, time.Now())
	if err != nil {
		t.Fatalf("pushing a Ledger entry over the site: %v", err)
	}
	if got := forgetest.RevParse(t, h.backend.repo.Bare, ledger.RefPrefix+chore); got != commit {
		t.Errorf("Ledger ref = %s, want the pushed %s", got, commit)
	}
	requireCoreCredential(t, seen, calls)
}

func TestPrimaryRow_LedgerSiteNamesAnUnsetCredential(t *testing.T) {
	primaryHarness(t, useGithubRowPrimary)
	t.Setenv(tokenCredentialEnv, "")

	_, err := Strategy.NewLedger(backendkit.Shared{RepoSlug: connectorRepo, Labels: testLabels}, nil)

	if err == nil || !strings.Contains(err.Error(), tokenCredentialEnv+" is not set") {
		t.Errorf("NewLedger = %v, want the unset credential named", err)
	}
}
