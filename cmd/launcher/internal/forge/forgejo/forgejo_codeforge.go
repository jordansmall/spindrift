package forgejo

import (
	"net/http"
	"net/url"
	"strings"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/git"
)

// forgejoGitRemoteURL builds a token-authenticated git clone URL for repo
// (an owner/repo slug) on the Forgejo instance at baseURL, e.g.
// ("https://codeberg.org", "owner/repo", "tok") ->
// "https://tok@codeberg.org/owner/repo.git" — the shape `git clone`/`git
// push` expect for HTTP(S) token auth (the token rides as the URL's
// userinfo, with no password half). Falls back to plain string
// concatenation if baseURL fails to parse, so a malformed
// FORGEJO_BASE_URL still yields a best-effort remote rather than an empty
// string.
func forgejoGitRemoteURL(baseURL, repo, token string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return strings.TrimSuffix(baseURL, "/") + "/" + strings.Trim(repo, "/") + ".git"
	}
	u.User = url.User(token)
	u.Path = "/" + strings.Trim(repo, "/") + ".git"
	return u.String()
}

// ForgejoCodeForgeConfig configures the Forgejo CodeForge adapter.
type ForgejoCodeForgeConfig struct {
	BaseURL string // Forgejo instance base URL, e.g. https://codeberg.org
	Repo    string // owner/repo slug
	Token   string

	BaseBranch   string // target branch Merge pushes onto for MERGE_MODE=immediate
	UserName     string // commit identity for Merge's throwaway clone
	UserEmail    string
	BranchPrefix string // baked into AgentBranch's output

	// HTTPClient overrides the HTTP client used for the REST Probe call; nil
	// uses http.DefaultClient. Tests inject a client pointed at a fake
	// server.
	HTTPClient *http.Client

	// GitRemoteURL overrides the derived token-authenticated git remote
	// (forgejoGitRemoteURL) with a verbatim remote URL. Test-only: it lets a
	// test point the git plumbing at a local bare repo fixture while Probe
	// still exercises the real Forgejo REST call against BaseURL. Production
	// callers leave this empty.
	GitRemoteURL string
}

// forgejoCodeForge is the Forgejo CodeForge adapter. Like the plain git
// adapter it wraps, it is push-only — Forgejo pull requests are not driven
// through this seam — so it implements forge.CodeForge only, never
// forge.PRForge. AgentBranch/BranchExists/Merge/Rebase delegate to a plain
// git.CodeForge against a token-authenticated remote; Probe delegates to the
// Forgejo REST client instead of git ls-remote, so it also validates the
// token and instance reachability, not just the repo's git-level presence.
type forgejoCodeForge struct {
	rest *forgejoClient
	git  forge.CodeForge
}

// NewForgejoCodeForge returns a forge.CodeForge backed by a Forgejo repo:
// git plumbing (via the push-only git adapter) for AgentBranch/BranchExists/
// Merge/Rebase, and the Forgejo REST API for Probe.
func NewForgejoCodeForge(cfg ForgejoCodeForgeConfig) forge.CodeForge {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultForgejoBaseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	remote := cfg.GitRemoteURL
	if remote == "" {
		remote = forgejoGitRemoteURL(baseURL, cfg.Repo, cfg.Token)
	}

	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}

	rest := &forgejoClient{cfg: ForgejoConfig{BaseURL: baseURL, Repo: cfg.Repo, Token: cfg.Token}, hc: hc}
	gitCF := git.NewGitClient(remote, cfg.BaseBranch, cfg.UserName, cfg.UserEmail, cfg.BranchPrefix)
	return &forgejoCodeForge{rest: rest, git: gitCF}
}

// AgentBranch delegates to the underlying git adapter.
func (f *forgejoCodeForge) AgentBranch(num string) string { return f.git.AgentBranch(num) }

// BranchExists delegates to the underlying git adapter.
func (f *forgejoCodeForge) BranchExists(branch string) (bool, error) {
	return f.git.BranchExists(branch)
}

// Merge delegates to the underlying git adapter.
func (f *forgejoCodeForge) Merge(ref string) error { return f.git.Merge(ref) }

// Rebase delegates to the underlying git adapter.
func (f *forgejoCodeForge) Rebase(ref string) error { return f.git.Rebase(ref) }

// Probe delegates to the Forgejo REST client, validating token and instance
// reachability (not just the remote's git-level presence, unlike the plain
// git adapter's ls-remote-based Probe).
func (f *forgejoCodeForge) Probe() (string, error) { return f.rest.Probe() }
