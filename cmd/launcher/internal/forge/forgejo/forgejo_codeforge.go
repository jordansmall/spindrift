package forgejo

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/git"
)

// defaultForgejoProbeTimeout bounds the default HTTP client used for the
// Probe REST call so a hung Forgejo instance can't block Probe forever.
const defaultForgejoProbeTimeout = 30 * time.Second

// forgejoGitRemoteURL builds a token-authenticated git clone URL for repo
// (an owner/repo slug) on the Forgejo instance at baseURL, e.g.
// ("https://codeberg.org", "owner/repo", "tok") ->
// "https://tok@codeberg.org/owner/repo.git" — the shape `git clone`/`git
// push` expect for HTTP(S) token auth (the token rides as the URL's
// userinfo, with no password half). Falls back to string concatenation
// with the token spliced in as userinfo if baseURL fails to parse, so a
// malformed FORGEJO_BASE_URL still yields a best-effort, push-authenticated
// remote rather than an anonymous one that would fail to push.
func forgejoGitRemoteURL(baseURL, repo, token string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		// Best-effort even when baseURL fails to parse: keep the token as the
		// remote's userinfo (inserted right after the scheme when one is
		// present) so the fallback is still a push-authenticated remote, not
		// an anonymous one that would fail to push.
		base := strings.TrimSuffix(baseURL, "/")
		slug := strings.Trim(repo, "/")
		if i := strings.Index(base, "://"); i >= 0 {
			return base[:i+3] + token + "@" + base[i+3:] + "/" + slug + ".git"
		}
		return token + "@" + base + "/" + slug + ".git"
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

	BaseBranch   string // target branch Merge merges onto / Rebase rebases onto for MERGE_MODE=immediate
	UserName     string // commit identity for Rebase's throwaway clone
	UserEmail    string
	BranchPrefix string // baked into AgentBranch's output

	// MergeMethod selects the merge style Merge and EnqueueAutoMerge request
	// via Forgejo's merge endpoint's "Do" field: "merge", "squash", or
	// "rebase". Empty (unset) resolves to "rebase" (forgejoMergeDo),
	// mirroring the github adapter's MERGE_METHOD knob default.
	MergeMethod string

	// HTTPClient overrides the HTTP client used for the REST Probe call; nil
	// uses a client with a default 30s timeout. Tests inject a client
	// pointed at a fake server.
	HTTPClient *http.Client

	// GitRemoteURL overrides the derived token-authenticated git remote
	// (forgejoGitRemoteURL) with a verbatim remote URL. Test-only: it lets a
	// test point the git plumbing at a local bare repo fixture while Probe
	// still exercises the real Forgejo REST call against BaseURL. Production
	// callers leave this empty.
	GitRemoteURL string
}

// forgejoCodeForge is the Forgejo CodeForge adapter. AgentBranch/BranchExists
// delegate to a plain git.CodeForge against a token-authenticated remote;
// Probe delegates to the Forgejo REST client instead of git ls-remote, so it
// also validates the token and instance reachability, not just the repo's
// git-level presence. Merge drives Forgejo's REST merge endpoint directly
// (a PR URL, not a branch); Rebase resolves the PR's head branch via REST
// and then delegates to the underlying git.CodeForge's Rebase, which clones
// the token remote, rebases the branch onto baseBranch, and force-pushes.
type forgejoCodeForge struct {
	rest        *forgejoClient
	git         forge.CodeForge
	mergeMethod string
}

// NewForgejoCodeForge returns a forge.CodeForge backed by a Forgejo repo:
// the Forgejo REST API for Probe/Merge, git plumbing (via the push-only git
// adapter) for AgentBranch/BranchExists/Rebase.
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
		hc = &http.Client{Timeout: defaultForgejoProbeTimeout}
	}

	rest := &forgejoClient{cfg: ForgejoConfig{BaseURL: baseURL, Repo: cfg.Repo, Token: cfg.Token}, hc: hc}
	gitCF := git.NewGitClient(remote, cfg.BaseBranch, cfg.UserName, cfg.UserEmail, cfg.BranchPrefix)
	return &forgejoCodeForge{rest: rest, git: gitCF, mergeMethod: cfg.MergeMethod}
}

// AgentBranch delegates to the underlying git adapter.
func (f *forgejoCodeForge) AgentBranch(num string) string { return f.git.AgentBranch(num) }

// BranchExists delegates to the underlying git adapter.
func (f *forgejoCodeForge) BranchExists(branch string) (bool, error) {
	return f.git.BranchExists(branch)
}

// forgejoMergeDo maps the MergeMethod knob's value onto the value Forgejo's
// merge endpoint's "Do" field expects. An empty method (unset) resolves to
// "rebase", mirroring the github adapter's mergeMethodFlag default so an
// unset MergeMethod behaves the same across both forges.
func forgejoMergeDo(method string) string {
	switch method {
	case "merge":
		return "merge"
	case "squash":
		return "squash"
	default:
		return "rebase"
	}
}

// Merge merges the pull request at prURL via Forgejo's REST merge endpoint,
// requesting f.mergeMethod's style (forgejoMergeDo) and deletion of the head
// branch after merge. A non-2xx response is classified like the github
// adapter's classifyMergeFailure (exec_pr.go, issue #566).
func (f *forgejoCodeForge) Merge(prURL string) error {
	index, err := parsePRIndex(prURL)
	if err != nil {
		return err
	}
	body := map[string]any{
		"Do":                        forgejoMergeDo(f.mergeMethod),
		"delete_branch_after_merge": true,
	}
	status, err := f.rest.do(http.MethodPost, f.rest.repoPath()+"/pulls/"+index+"/merge", body, nil)
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		return nil
	}
	return f.classifyMergeFailure(prURL, status)
}

// classifyMergeFailure distinguishes a genuine merge conflict from a PR
// that's merely blocked by pending or failing required checks. Forgejo's
// merge endpoint returns the same "not mergeable" refusal status (405 Method
// Not Allowed or 409 Conflict) for both cases, so those two — and only those
// two — are disambiguated by querying the PR's mergeable state, the same
// disambiguation the github adapter's classifyMergeFailure performs after it
// gates on IsMergeConflict(stderr) (issue #566). Any other non-2xx status
// (403 token lacks merge scope, 429 rate limit, 500 server error) is a
// genuine failure and is surfaced as a raw error naming the status rather
// than masked as ErrMergeConflict or ErrMergeBlockedByChecks. A refusal-status
// mergeable state this function cannot map to either outcome is likewise
// surfaced as its own error.
func (f *forgejoCodeForge) classifyMergeFailure(prURL string, status int) error {
	if status != http.StatusMethodNotAllowed && status != http.StatusConflict {
		return fmt.Errorf("forgejo: merge %s: unexpected status %d", prURL, status)
	}
	state, err := f.Mergeable(prURL)
	if err != nil {
		return fmt.Errorf("forgejo: merge %s: unexpected status %d (mergeable state unavailable: %w)", prURL, status, err)
	}
	switch state {
	case forge.MergeableConflicting:
		return forge.ErrMergeConflict
	case forge.MergeableMergeable:
		return forge.ErrMergeBlockedByChecks
	default:
		return fmt.Errorf("forgejo: merge %s: unexpected status %d (mergeable state %q undetermined)", prURL, status, state)
	}
}

// Rebase resolves prURL's PR to its head branch via REST, then delegates to
// the underlying git adapter's Rebase, which clones the token remote,
// rebases the branch onto the configured base branch, and force-pushes the
// result back to the remote.
func (f *forgejoCodeForge) Rebase(prURL string) error {
	p, err := f.getPull(prURL)
	if err != nil {
		return err
	}
	return f.git.Rebase(p.Head.Ref)
}

// Probe delegates to the Forgejo REST client, validating token and instance
// reachability (not just the remote's git-level presence, unlike the plain
// git adapter's ls-remote-based Probe).
func (f *forgejoCodeForge) Probe() (string, error) { return f.rest.Probe() }
