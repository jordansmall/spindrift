package forgejo_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/forgejo"
	"spindrift.dev/launcher/internal/forge/forgetest"
	"spindrift.dev/launcher/internal/gitexec/gitexectest"
	"spindrift.dev/launcher/internal/seambundle"
)

const credToken = "s3cr3t-forgejo-token-6860"

// urlUserinfo matches URL userinfo (scheme://user@host), not a bare "@"
// elsewhere in a git config.
var urlUserinfo = regexp.MustCompile(`://[^/\s]*@`)

// exitTail matches the ": <git output>" cloneOutputTail appends after the exit
// status.
var exitTail = regexp.MustCompile(`exit status \d+: \S`)

// credHarness serves an authenticated smart-HTTP remote (user oauth2, password
// = the Forgejo token) with git tracing on, and builds both adapters over it
// with the credential-free URL, as the production fallback remote now is.
type credHarness struct {
	bare  string
	rw    forge.CodeForge
	ro    forge.CodeForge
	trace func() string
}

func newCredHarness(t *testing.T) *credHarness {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "Test Bot")
	t.Setenv("GIT_AUTHOR_EMAIL", "bot@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Test Bot")
	t.Setenv("GIT_COMMITTER_EMAIL", "bot@example.com")
	traceFile := filepath.Join(t.TempDir(), "trace.log")
	t.Setenv("GIT_TRACE", traceFile)

	url, bare := gitexectest.AuthRemote(t, "oauth2", credToken)
	cfg := forgejo.ForgejoCodeForgeConfig{
		BaseURL:    newFakeForgejo(t).URL(),
		Repo:       "owner/repo",
		Token:      credToken,
		BaseBranch: "main",
		UserName:   "Test Bot",
		UserEmail:  "bot@example.com",
	}
	return &credHarness{
		bare: bare,
		rw:   forgejo.NewForgejoCodeForgeForTest(cfg, nil, url),
		ro:   forgejo.NewReadOnlyForgejoCodeForgeForTest(cfg, nil, url),
		trace: func() string {
			b, err := os.ReadFile(traceFile)
			if err != nil {
				t.Fatalf("read GIT_TRACE: %v", err)
			}
			return string(b)
		},
	}
}

func (h *credHarness) assertTokenOutOfBand(t *testing.T) {
	t.Helper()
	trace := h.trace()
	if trace == "" {
		t.Fatal("GIT_TRACE is empty: the tracer saw no git invocation")
	}
	if strings.Contains(trace, credToken) {
		t.Errorf("git trace carries the token:\n%s", trace)
	}
	for _, line := range strings.Split(trace, "\n") {
		if strings.Contains(line, "http://") && strings.Contains(line, "@") {
			t.Errorf("traced git argv carries URL userinfo: %s", line)
		}
	}
}

// assertCloneConfigCredentialFree clones through relayClone and asserts the
// resulting repository config holds neither the token nor any URL userinfo.
func assertCloneConfigCredentialFree(t *testing.T, cf forge.CodeForge) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "clone")
	if err := forgejo.RelayClone(cf, "x")(dir); err != nil {
		t.Fatalf("relayClone: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := string(b)
	if !strings.Contains(cfg, "url = http://") {
		t.Fatalf("clone config has no http remote.origin.url:\n%s", cfg)
	}
	if strings.Contains(cfg, credToken) || urlUserinfo.MatchString(cfg) {
		t.Errorf("clone config carries the token or URL userinfo:\n%s", cfg)
	}
}

func TestForgejoCodeForge_RelayCloneConfigCarriesNoToken(t *testing.T) {
	h := newCredHarness(t)
	assertCloneConfigCredentialFree(t, h.rw)
	h.assertTokenOutOfBand(t)
}

// A test override remote carrying userinfo is split like the production
// remote, so the token never reaches relayClone's argv or the clone's config.
func TestForgejoCodeForge_RemoteOverrideUserinfoIsSplitOff(t *testing.T) {
	h := newCredHarness(t)
	url, _ := gitexectest.AuthRemote(t, "oauth2", credToken)
	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL: newFakeForgejo(t).URL(), Repo: "owner/repo", Token: credToken,
		BaseBranch: "main", UserName: "Test Bot", UserEmail: "bot@example.com",
	}, nil, strings.Replace(url, "://", "://oauth2:"+credToken+"@", 1))

	assertCloneConfigCredentialFree(t, cf)
	h.assertTokenOutOfBand(t)
}

func TestForgejoCodeForge_CredentialAttachesOverHTTP(t *testing.T) {
	h := newCredHarness(t)

	if ok, err := h.rw.BranchExists("main"); err != nil || !ok {
		t.Fatalf("BranchExists(main) = %v, %v; want true, nil", ok, err)
	}

	src := t.TempDir()
	forgetest.Run(t, "", "clone", h.bare, src)
	forgetest.Run(t, src, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src, "feature.txt"), "feature\n")
	forgetest.Run(t, src, "add", "feature.txt")
	forgetest.Run(t, src, "commit", "-m", "feature")
	wantSHA := forgetest.RevParse(t, src, "work")

	bp := h.rw.(forge.BranchPusher)
	if err := bp.PushBranch(src, "work", "agent/issue-9001", "main"); err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if got := forgetest.RevParse(t, h.bare, "refs/heads/agent/issue-9001"); got != wantSHA {
		t.Errorf("refs/heads/agent/issue-9001 = %s, want %s", got, wantSHA)
	}
	if ok, err := h.rw.BranchExists("agent/issue-9001"); err != nil || !ok {
		t.Fatalf("BranchExists(pushed branch) = %v, %v; want true, nil", ok, err)
	}

	h.assertTokenOutOfBand(t)
}

func TestReadOnlyForgejoCodeForge_RelayBundleCredentialAttachesOverHTTP(t *testing.T) {
	h := newCredHarness(t)
	outbox := t.TempDir()
	branch := "agent/issue-1964"
	wantSHA := forgetest.SeedRelayBundle(t, h.bare, "main", outbox, branch)
	if _, err := os.Stat(filepath.Join(outbox, seambundle.FileName)); err != nil {
		t.Fatal(err)
	}

	br := h.ro.(forge.BundleRelay)
	if _, err := h.ro.(forge.BundleCommitSubjects).CommitSubjects(outbox, "main", branch); err != nil {
		t.Fatalf("CommitSubjects: %v", err)
	}
	if err := br.RelayBundle(outbox, branch); err != nil {
		t.Fatalf("RelayBundle: %v", err)
	}
	if got := forgetest.RevParse(t, h.bare, "refs/heads/"+branch); got != wantSHA {
		t.Errorf("refs/heads/%s = %s, want %s", branch, got, wantSHA)
	}

	h.assertTokenOutOfBand(t)
}

func TestForgejoCodeForge_WrongTokenErrorsDoNotLeakIt(t *testing.T) {
	h := newCredHarness(t)
	url, _ := gitexectest.AuthRemote(t, "oauth2", credToken)
	const wrong = "wrong-forgejo-token-6860"
	cf := forgejo.NewForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL: newFakeForgejo(t).URL(), Repo: "owner/repo", Token: wrong,
		BaseBranch: "main", UserName: "Test Bot", UserEmail: "bot@example.com",
	}, nil, url)

	src := t.TempDir()
	forgetest.Run(t, "", "clone", h.bare, src)
	forgetest.Run(t, src, "checkout", "-b", "work")
	forgetest.WriteFile(t, filepath.Join(src, "f.txt"), "f\n")
	forgetest.Run(t, src, "add", "f.txt")
	forgetest.Run(t, src, "commit", "-m", "f")

	pushErr := cf.(forge.BranchPusher).PushBranch(src, "work", "agent/issue-1", "main")
	_, existsErr := cf.BranchExists("main")
	for name, err := range map[string]error{"PushBranch": pushErr, "BranchExists": existsErr} {
		if err == nil {
			t.Errorf("%s with a wrong token: want error, got nil", name)
			continue
		}
		if strings.Contains(err.Error(), wrong) {
			t.Errorf("%s error leaks the token: %v", name, err)
		}
	}
}

// SeedMirror's host fetch authenticates over HTTP with the token on the
// credential path, and the bare mirror the Box clones keeps neither the token
// nor URL userinfo in its config.
func TestForgejoCodeForge_SeedMirrorAuthenticatesAndPersistsNoToken(t *testing.T) {
	h := newCredHarness(t)
	ms, ok := h.ro.(forge.MirrorSeeder)
	if !ok {
		t.Fatal("read-only forgejo CodeForge does not implement forge.MirrorSeeder")
	}
	dir := filepath.Join(t.TempDir(), "repo.git")
	if err := ms.SeedMirror(dir); err != nil {
		t.Fatalf("SeedMirror over authenticated HTTP: %v", err)
	}
	if got, want := forgetest.RevParse(t, dir, "refs/heads/main"), forgetest.RevParse(t, h.bare, "refs/heads/main"); got != want {
		t.Errorf("mirror main = %s, want %s", got, want)
	}
	b, err := os.ReadFile(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg := string(b); strings.Contains(cfg, credToken) || urlUserinfo.MatchString(cfg) {
		t.Errorf("mirror config carries the token or URL userinfo:\n%s", cfg)
	}
	h.assertTokenOutOfBand(t)
}

// A failed seed names git's cause, redacted of any URL userinfo and the token.
func TestForgejoCodeForge_SeedMirrorFailureNamesCauseWithoutToken(t *testing.T) {
	h := newCredHarness(t)
	cf := forgejo.NewReadOnlyForgejoCodeForgeForTest(forgejo.ForgejoCodeForgeConfig{
		BaseURL: newFakeForgejo(t).URL(), Repo: "owner/repo", Token: credToken, BaseBranch: "main",
	}, nil, h.bare+"-missing")
	err := cf.(forge.MirrorSeeder).SeedMirror(filepath.Join(t.TempDir(), "repo.git"))
	if err == nil {
		t.Fatal("SeedMirror of a missing remote returned nil")
	}
	// git's own wording is locale- and version-dependent, so assert on what the
	// code adds: the op, the remote, and a non-empty tail of git's output.
	if want := "seed mirror: git clone " + h.bare + "-missing"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not carry %q", err, want)
	}
	if !exitTail.MatchString(err.Error()) {
		t.Errorf("error %q carries no tail of git's output", err)
	}
	if strings.Contains(err.Error(), credToken) {
		t.Errorf("error leaks the token: %v", err)
	}
}
