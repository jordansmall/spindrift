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
	"spindrift.dev/launcher/internal/gitexec/gitexectest"
	"spindrift.dev/launcher/internal/ledger"
)

// routedGitClient is the production execClient for a routed row, with the
// base branch and committer identity core's own git needs.
func routedGitClient(opts ...ExecOption) *execClient {
	return NewExecClient(connectorRepo, testLabels, append([]ExecOption{
		WithBaseBranch("main"), WithCommitIdentity("Test Bot", "bot@example.com"),
	}, opts...)...)
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
		t.Errorf("gh ran %d times under a routed row", n)
	}
}

// requireNoCoreGit fails when core's credentialed git ran. The log also holds
// credential-less lines from the scripted backend serving the PR read, which
// shells out to git itself, so the row's token is what marks core's git.
func requireNoCoreGit(t *testing.T, seen string) {
	t.Helper()
	log, err := os.ReadFile(seen)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(log), connectorToken) {
		t.Errorf("core's git ran for a refused head: %q", log)
	}
}

// Under a routed row the read-only Box's bundle relay and commit subjects run
// in core's git with the row's credential.
func TestConnectorRow_BundleRelayRunsInCoreGit(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	seen := gitCredentialLog(t)
	f := routedGitClient()
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

// A Box that wrote no bundle is the benign case under a routed row too.
func TestConnectorRow_RelayOfNoBundleIsBenign(t *testing.T) {
	routedLogHarness(t, useGithubRowAt)
	f := routedGitClient()

	if err := f.RelayBundle(t.TempDir(), "agent/issue-1"); !errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("RelayBundle(empty outbox) = %v, want forge.ErrBundleNotFound", err)
	}
	if _, err := f.CommitSubjects(t.TempDir(), "main", "agent/issue-1"); !errors.Is(err, forge.ErrBundleNotFound) {
		t.Errorf("CommitSubjects(empty outbox) = %v, want forge.ErrBundleNotFound", err)
	}
}

// PushBranch creates the branch through core's git, create-only.
func TestConnectorRow_PushBranchRunsInCoreGit(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	seen := gitCredentialLog(t)
	src := t.TempDir()
	forgetest.Run(t, "", "clone", h.backend.repo.Bare, src)
	forgetest.Run(t, src, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src, "feature.txt"), "feature\n")
	forgetest.Run(t, src, "add", "feature.txt")
	forgetest.Run(t, src, "commit", "-m", "feature")
	c := routedGitClient()

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
func TestConnectorRow_SeedMirrorRunsInCoreGit(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	seen := gitCredentialLog(t)
	dir := filepath.Join(t.TempDir(), "mirror")

	if err := routedGitClient().SeedMirror(dir); err != nil {
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
func TestConnectorRow_SeedMirrorWhenTheWebHostIsNotAnAPIRoute(t *testing.T) {
	h, _, _, _ := routedLogHarness(t, useGithubRowAt)
	splitWebHost(t, h)
	dir := filepath.Join(t.TempDir(), "mirror")

	if err := routedGitClient().SeedMirror(dir); err != nil {
		t.Fatalf("SeedMirror: %v", err)
	}
	if got, want := forgetest.RevParse(t, dir, "main"), forgetest.RevParse(t, h.backend.repo.Bare, "main"); got != want {
		t.Errorf("mirror main = %s, want %s", got, want)
	}
}

// No git operation a routed row composes refuses a remote on GH_HOST.
func TestConnectorRow_GitOpsAcceptARemoteOnGHHost(t *testing.T) {
	for _, op := range routedGitOps {
		t.Run(op.name, func(t *testing.T) {
			h, _, _, _ := routedLogHarness(t, useGithubRowAt)
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

// RebasePRHead under a routed row syncs the PR's head onto core's base branch in
// core's git, with SYNC_METHOD choosing between rebase and merge, and a real
// conflict is forge.ErrMergeConflict.
func TestConnectorRow_RebasePRHeadRunsInCoreGit(t *testing.T) {
	for _, tc := range []struct {
		method     string
		wantMerges string
	}{{"rebase", "0"}, {"", "0"}, {"merge", "1"}} {
		t.Run("sync="+tc.method, func(t *testing.T) {
			h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
			seen := gitCredentialLog(t)
			url := h.SeedLandablePR("11")
			h.backend.repo.AdvanceBase()
			branch := forge.AgentBranchName(h.BranchPrefix(), "11")

			if err := routedGitClient(WithSyncMethod(tc.method)).RebasePRHead(url, branch); err != nil {
				t.Fatalf("RebasePRHead: %v", err)
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

// A conflicting sync under a routed row surfaces forge.ErrMergeConflict and
// never reaches gh.
func TestConnectorRow_RebasePRHeadConflictIsErrMergeConflict(t *testing.T) {
	for _, method := range []string{"rebase", "merge"} {
		t.Run(method, func(t *testing.T) {
			h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
			url := h.SeedLandablePR("12")
			h.backend.repo.ConflictBase("12")

			if err := routedGitClient(WithSyncMethod(method)).RebasePRHead(url, forge.AgentBranchName(h.BranchPrefix(), "12")); !errors.Is(err, forge.ErrMergeConflict) {
				t.Errorf("RebasePRHead = %v, want forge.ErrMergeConflict", err)
			}
			if n := calls(); n != 0 {
				t.Errorf("gh ran %d times", n)
			}
		})
	}
}

// routedGitOps is each git operation a routed row routes, with what it needs
// seeded.
var routedGitOps = []struct {
	name string
	run  func(h *connectorHarness, t *testing.T) error
}{
	{"RelayBundle", func(h *connectorHarness, t *testing.T) error {
		outbox := t.TempDir()
		forgetest.SeedRelayBundle(t, h.backend.repo.Bare, "main", outbox, landingBranch)
		return routedGitClient().RelayBundle(outbox, landingBranch)
	}},
	{"CommitSubjects", func(h *connectorHarness, t *testing.T) error {
		outbox := t.TempDir()
		forgetest.SeedRelayBundle(t, h.backend.repo.Bare, "main", outbox, landingBranch)
		_, err := routedGitClient().CommitSubjects(outbox, "main", landingBranch)
		return err
	}},
	{"PushBranch", func(h *connectorHarness, t *testing.T) error {
		return routedGitClient().PushBranch(t.TempDir(), "work", landingBranch, "main")
	}},
	{"RebasePRHead", func(h *connectorHarness, t *testing.T) error {
		return routedGitClient().RebasePRHead(h.SeedLandablePR("13"), forge.AgentBranchName(h.BranchPrefix(), "13"))
	}},
	{"SeedMirror", func(h *connectorHarness, t *testing.T) error {
		return routedGitClient().SeedMirror(filepath.Join(t.TempDir(), "mirror"))
	}},
}

// A Connector that fails to load, or a route the host refuses, fails each git
// operation with that error; none falls back to gh.
func TestConnectorRow_GitOpsFailByName(t *testing.T) {
	boom := errors.New("github connector failed to load")
	for _, op := range routedGitOps {
		t.Run(op.name+"/load", func(t *testing.T) {
			h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
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

			if err := op.run(h, t); !errors.Is(err, connectorhost.ErrRouteRefused) {
				t.Errorf("err = %v, want the route refusal", err)
			}
			if n := calls(); n != 0 {
				t.Errorf("ran gh %d times", n)
			}
		})
	}
}

// A routed row whose credential env is unset names it rather than sending git
// an empty password.
func TestConnectorRow_GitOpsNameAnUnsetCredential(t *testing.T) {
	_, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	t.Setenv(tokenCredentialEnv, "")

	err := routedGitClient().SeedMirror(filepath.Join(t.TempDir(), "mirror"))

	if err == nil || !strings.Contains(err.Error(), tokenCredentialEnv+" is not set") {
		t.Errorf("SeedMirror = %v, want the unset credential named", err)
	}
	if n := calls(); n != 0 {
		t.Errorf("ran gh %d times", n)
	}
}

// routedHostMediationHarness is the host-mediation contract's harness with the
// Strategy's own tracker and read-only Code Forge, under a plain row. Comments
// land on the scripted backend, so the tracker half is under test too.
type routedHostMediationHarness struct {
	*composedHostMediationHarness
	cf forge.CodeForge
	tr forge.IssueTracker
}

func (h *routedHostMediationHarness) CodeForge() forge.CodeForge  { return h.cf }
func (h *routedHostMediationHarness) Tracker() forge.IssueTracker { return h.tr }

func (h *routedHostMediationHarness) SeedCommentTarget(failing bool) string {
	return trackerWriteHarness{h.connectorHarness}.SeedCommentTarget(failing)
}

func (h *routedHostMediationHarness) CommentPosted(num, body string) bool {
	return trackerWriteHarness{h.connectorHarness}.CommentPosted(num, body)
}

// The host-mediation contract holds for the production forge under a plain
// row, with core's git doing the relay and no gh call.
func TestPlainRow_HostMediationContract(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	base := &composedHostMediationHarness{
		connectorHarness: h,
		relayedSHA:       map[string]string{}, seeded: map[string]bool{}, prSeq: 1500,
	}
	s := backendkit.Shared{
		RepoSlug: connectorRepo, Labels: testLabels, BaseBranch: "main", GitUserName: "Test Bot", GitUserEmail: "bot@example.com",
	}
	tr, err := Strategy.NewIssueTracker(s, nil)
	if err != nil {
		t.Fatal(err)
	}
	cf, err := Strategy.NewCodeForge(s, nil, forge.SanitizedParent{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	forgetest.RunHostMediationContract(t, &routedHostMediationHarness{base, cf, tr})

	if n := calls(); n != 0 {
		t.Errorf("the contract ran gh %d times under a plain row", n)
	}
}

// The butler Ledger's site under a routed row is the Connector's clone URL
// and the row's credential, with no gh credential helper: the Ledger's own git
// pushes a Ledger entry to the repository with them.
func TestConnectorRow_LedgerSiteIsConnectorCloneURLAndCoreCredential(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	seen := gitCredentialLog(t)

	site, err := Strategy.NewLedger(backendkit.Shared{RepoSlug: connectorRepo, Labels: testLabels}, nil)
	if err != nil {
		t.Fatalf("NewLedger: %v", err)
	}

	if len(site.GitArgs) != 0 {
		t.Errorf("GitArgs = %v, want none under a routed row", site.GitArgs)
	}
	if cred, err := site.Credential.Get(); err != nil || cred.Username != "x-access-token" || cred.Password != connectorToken {
		t.Errorf("Credential = %+v, %v, want the row's token", cred, err)
	}
	r, err := ledger.NewRemoteFrom(t.TempDir(), site.URL, site.Credential, site.GitArgs...)
	if err != nil {
		t.Fatalf("a Ledger over the site cannot reach the repository: %v", err)
	}
	const chore = "chore-routed"
	commit, err := r.Append(chore, "", ledger.State{Phase: ledger.Claimed}, time.Now())
	if err != nil {
		t.Fatalf("pushing a Ledger entry over the site: %v", err)
	}
	if got := forgetest.RevParse(t, h.backend.repo.Bare, ledger.RefPrefix+chore); got != commit {
		t.Errorf("Ledger ref = %s, want the pushed %s", got, commit)
	}
	requireCoreCredential(t, seen, calls)
}

// The site is built once per chore run but the token refreshes under it: each
// Ledger push presents the row env's token as it stands then.
func TestConnectorRow_LedgerPushReadsTheRowTokenAtEachPush(t *testing.T) {
	routedLogHarness(t, useGithubRowAt)
	requireLedgerPushReadsRotatedToken(t, func() { t.Setenv(tokenCredentialEnv, "rotated-token") })
}

func TestConnectorRow_LedgerSiteNamesAnUnsetCredential(t *testing.T) {
	routedLogHarness(t, useGithubRowAt)
	t.Setenv(tokenCredentialEnv, "")

	_, err := Strategy.NewLedger(backendkit.Shared{RepoSlug: connectorRepo, Labels: testLabels}, nil)

	if err == nil || !strings.Contains(err.Error(), tokenCredentialEnv+" is not set") {
		t.Errorf("NewLedger = %v, want the unset credential named", err)
	}
}

// RebasePRHead under a routed row rebases when the head the Connector names
// is the one the caller expects, in core's git.
func TestConnectorRow_RebasePRHeadMatchRebases(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	seen := gitCredentialLog(t)
	url := h.SeedLandablePR("14")
	h.backend.repo.AdvanceBase()
	branch := forge.AgentBranchName(h.BranchPrefix(), "14")

	if err := routedGitClient().RebasePRHead(url, branch); err != nil {
		t.Fatalf("RebasePRHead: %v", err)
	}
	if !h.backend.repo.Rebased(branch) {
		t.Error("the PR branch does not contain the advanced base")
	}
	requireCoreCredential(t, seen, calls)
}

// A head the Connector names that differs from the expected one is refused
// before any git runs.
func TestConnectorRow_RebasePRHeadMismatchIsRefusedBeforeGit(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	url := h.SeedLandablePR("15")
	h.backend.repo.AdvanceBase()
	seen := gitCredentialLog(t)
	branch := forge.AgentBranchName(h.BranchPrefix(), "15")

	err := routedGitClient().RebasePRHead(url, forge.AgentBranchName(h.BranchPrefix(), "999"))
	if !errors.Is(err, connectorforge.ErrAnswer) {
		t.Fatalf("RebasePRHead = %v, want connectorforge.ErrAnswer", err)
	}
	requireNoCoreGit(t, seen)
	if h.backend.repo.Rebased(branch) {
		t.Error("the PR branch was rebased")
	}
	if n := calls(); n != 0 {
		t.Errorf("gh ran %d times", n)
	}
}

// The production github Code Forge exposes the head-pinned rebase, so the
// Lander finds it through forge.ResolveCodeForge.
func TestExecClient_ResolvesPRHeadRebaser(t *testing.T) {
	if forge.ResolveCodeForge(routedGitClient()).PRHeadRebaser == nil {
		t.Error("ResolveCodeForge found no PRHeadRebaser on the github Code Forge")
	}
}

// A plain Rebase under a routed row has no expected head to pin against the
// guest's, so it is refused with nothing pushed (issue #6961).
func TestConnectorRow_PlainRebaseIsRefused(t *testing.T) {
	h, _, calls, _ := routedLogHarness(t, useGithubRowAt)
	url := h.SeedLandablePR("16")
	h.backend.repo.AdvanceBase()
	seen := gitCredentialLog(t)
	branch := forge.AgentBranchName(h.BranchPrefix(), "16")

	if err := routedGitClient().Rebase(url); !errors.Is(err, connectorforge.ErrAnswer) || !strings.Contains(err.Error(), "unpinned") {
		t.Fatalf("Rebase = %v, want connectorforge.ErrAnswer naming the unpinned refusal", err)
	}
	if _, serr := os.Stat(seen); serr == nil {
		t.Error("git ran for an unpinned rebase")
	}
	if h.backend.repo.Rebased(branch) {
		t.Error("the PR branch was rebased")
	}
	if n := calls(); n != 0 {
		t.Errorf("gh ran %d times", n)
	}
}

// Core's git reaches the repository at the exact clone URL over HTTPS with no
// insteadOf rewrite, so the credential must be scoped to that URL to be sent.
func TestConnectorRow_SeedMirrorAuthenticatesAtTheCloneURL(t *testing.T) {
	h, _, _, _ := routedLogHarness(t, useGithubRowAt)
	git := h.backend.serveGitOverHTTPS(t)
	dir := filepath.Join(t.TempDir(), "mirror")

	if err := routedGitClient().SeedMirror(dir); err != nil {
		t.Fatalf("SeedMirror: %v", err)
	}

	if got, want := forgetest.RevParse(t, dir, "main"), forgetest.RevParse(t, h.backend.repo.Bare, "main"); got != want {
		t.Errorf("mirror main = %s, want %s", got, want)
	}
	gitexectest.RequireAuthOnlyUnder(t, git.Requests(), "/"+connectorRepo+".git/")
}

func TestConnectorRow_RebasePRHeadAuthenticatesAtTheCloneURL(t *testing.T) {
	h, _, _, _ := routedLogHarness(t, useGithubRowAt)
	git := h.backend.serveGitOverHTTPS(t)
	url := h.SeedLandablePR("21")
	h.backend.repo.AdvanceBase()
	branch := forge.AgentBranchName(h.BranchPrefix(), "21")

	if err := routedGitClient().RebasePRHead(url, branch); err != nil {
		t.Fatalf("RebasePRHead: %v", err)
	}

	if !h.backend.repo.Rebased(branch) {
		t.Error("the PR branch does not contain the advanced base")
	}
	gitexectest.RequireAuthOnlyUnder(t, git.Requests(), "/"+connectorRepo+".git/")
}

// requireLedgerPushReadsRotatedToken pushes a Ledger entry through a routed
// row's Ledger site, calls rotate to move the row's credential to
// "rotated-token", and pushes again: git must present the original token on
// the first push and the rotated one on the second, though the site was built
// once before either.
func requireLedgerPushReadsRotatedToken(t *testing.T, rotate func()) {
	t.Helper()
	const chore, rotated = "chore-rotated", "rotated-token"
	seen := gitCredentialLog(t)
	site, err := Strategy.NewLedger(backendkit.Shared{RepoSlug: connectorRepo, Labels: testLabels}, nil)
	if err != nil {
		t.Fatalf("NewLedger: %v", err)
	}
	r, err := ledger.NewRemoteFrom(t.TempDir(), site.URL, site.Credential, site.GitArgs...)
	if err != nil {
		t.Fatalf("NewRemoteFrom: %v", err)
	}
	commit, err := r.Append(chore, "", ledger.State{Phase: ledger.Claimed}, time.Now())
	if err != nil {
		t.Fatalf("Append with the live token: %v", err)
	}
	first, err := os.ReadFile(seen)
	if err != nil {
		t.Fatalf("git never ran with a credential: %v", err)
	}
	if !strings.Contains(string(first), "x-access-token:"+connectorToken) || strings.Contains(string(first), rotated) {
		t.Fatalf("first push credentials = %q, want the original token only", first)
	}

	rotate()
	if _, err := r.Append(chore, commit, ledger.State{Phase: ledger.Done}, time.Now()); err != nil {
		t.Fatalf("Append after the token rotated: %v", err)
	}
	all, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(all[len(first):]), "x-access-token:"+rotated) {
		t.Errorf("second push credentials = %q, want the rotated token: the push reused the token captured at build", all[len(first):])
	}
}
