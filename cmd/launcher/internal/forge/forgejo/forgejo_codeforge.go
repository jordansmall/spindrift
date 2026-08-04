package forgejo

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"spindrift.dev/launcher/internal/forge"
	"spindrift.dev/launcher/internal/forge/git"
	"spindrift.dev/launcher/internal/forge/rest"
)

// defaultForgejoHTTPTimeout bounds the default HTTP client used for all
// Forgejo REST calls -- both the IssueTracker adapter (NewForgejoClient) and
// the CodeForge adapter's Probe/Merge (newForgejoCodeForge) -- so a hung
// Forgejo instance can't block any of them forever. This also matters for
// the shared-client seam (issue #2256): when CODE_FORGE=forgejo and
// ISSUE_TRACKER=forgejo agree on the same repo, newForgejoCodeForge reuses
// the tracker's own *rest.Client instead of building a second one, so the
// tracker's default must be timeout-bound too, not just the CodeForge's own
// locally-computed default.
const defaultForgejoHTTPTimeout = 30 * time.Second

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
}

// errMergeRefused is forgejo's internal signal that the merge endpoint
// refused the merge as "not mergeable" (405 or 409 -- Forgejo uses both for
// the same refusal) -- classifyMergeFailure disambiguates a genuine
// conflict from checks-still-pending by then querying Mergeable. It never
// escapes this file: callers only ever see forge.ErrMergeConflict,
// forge.ErrMergeBlockedByChecks, or a raw wrapped error.
var errMergeRefused = errors.New("forgejo: merge refused")

// forgejoStatusMap is the HTTP-status -> sentinel-error table shared by
// every *rest.Client this package builds against the Forgejo REST API
// (NewForgejoClient's tracker and newForgejoCodeForge's fallback
// CodeForge client) -- kept in one place so the tracker and CodeForge
// seams can't drift out of sync on which status maps to which sentinel.
func forgejoStatusMap() rest.StatusMap {
	return rest.StatusMap{
		http.StatusUnauthorized:     forge.ErrAuthFailure,
		http.StatusForbidden:        forge.ErrAuthFailure,
		http.StatusNotFound:         forge.ErrNotFound,
		http.StatusMethodNotAllowed: errMergeRefused,
		http.StatusConflict:         errMergeRefused,
	}
}

// forgejoCodeForge is the Forgejo CodeForge adapter. AgentBranch/BranchExists
// delegate to a plain git.CodeForge against a token-authenticated remote;
// Probe drives the Forgejo REST client directly instead of git ls-remote, so
// it also validates the token and instance reachability, not just the
// repo's git-level presence. Merge drives Forgejo's REST merge endpoint
// directly (a PR URL, not a branch); Rebase resolves the PR's head branch
// via REST and then delegates to the underlying git.CodeForge's Rebase,
// which clones the token remote, rebases the branch onto baseBranch, and
// force-pushes the result back to the remote.
type forgejoCodeForge struct {
	rest        *rest.Client
	repo        string // owner/repo slug, for repoPath
	git         forge.CodeForge
	mergeMethod string
	// remote is the token-authenticated git clone/push URL (forgejoGitRemoteURL,
	// or an explicit override in tests via NewForgejoCodeForgeForTest): the
	// same remote the underlying git adapter clones/pushes against, kept
	// here too so the read-only wrapper (forgejo_readonly.go) can clone it
	// directly for RelayBundle without threading a second config surface
	// through NewForgejoCodeForge.
	remote string
}

// repoPath returns the API base path for the configured repo,
// /api/v1/repos/{owner}/{repo}.
func (f *forgejoCodeForge) repoPath() string {
	return "/api/v1/repos/" + f.repo
}

// NewForgejoCodeForge returns a forge.CodeForge backed by a Forgejo repo:
// the Forgejo REST API for Probe/Merge, git plumbing (via the push-only git
// adapter) for AgentBranch/BranchExists/Rebase. tracker, when non-nil and
// itself a Forgejo IssueTracker built by NewForgejoClient, lets this
// CodeForge reuse that tracker's underlying *rest.Client instead of building
// a second one -- one shared REST client instance backing both seams when
// CODE_FORGE=forgejo and ISSUE_TRACKER=forgejo agree on the same repo. Any
// other tracker (nil, or a different backend's IssueTracker) falls back to
// constructing a fresh client of its own.
func NewForgejoCodeForge(cfg ForgejoCodeForgeConfig, tracker forge.IssueTracker) forge.CodeForge {
	return newForgejoCodeForge(cfg, tracker, "")
}

// NewForgejoCodeForgeForTest is NewForgejoCodeForge with an explicit git
// remote override -- test-only. It lets a test point the git plumbing at a
// local bare repo fixture while Probe/REST calls still exercise the real (or
// fake) Forgejo REST server at cfg.BaseURL. Production wiring (main.go) has
// no such override and always uses NewForgejoCodeForge.
func NewForgejoCodeForgeForTest(cfg ForgejoCodeForgeConfig, tracker forge.IssueTracker, gitRemoteURL string) forge.CodeForge {
	return newForgejoCodeForge(cfg, tracker, gitRemoteURL)
}

// newForgejoCodeForge is the shared constructor behind NewForgejoCodeForge
// and NewForgejoCodeForgeForTest.
func newForgejoCodeForge(cfg ForgejoCodeForgeConfig, tracker forge.IssueTracker, gitRemoteURL string) *forgejoCodeForge {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultForgejoBaseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	remote := gitRemoteURL
	if remote == "" {
		remote = forgejoGitRemoteURL(baseURL, cfg.Repo, cfg.Token)
	}

	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: defaultForgejoHTTPTimeout}
	}

	var restCli *rest.Client
	if fc, ok := tracker.(*forgejoClient); ok {
		// Reuse the tracker's own *rest.Client so the two seams share one
		// underlying client instance (issue #2256) instead of each building
		// its own against the same repo -- cfg.BaseURL/Token/HTTPClient are
		// silently ignored on this branch. Safe only because production
		// wiring (main.go) always constructs the tracker and this CodeForge
		// from the same c.forgejoBaseURL/c.forgejoToken/c.repoSlug, so the
		// reused client's config can never diverge from cfg's; nothing here
		// enforces that invariant, so a future caller feeding a tracker and
		// cfg for different repos/instances would merge silently wrong.
		restCli = fc.rest
	} else {
		restCli = rest.New(baseURL, rest.TokenAuth{Scheme: "token", Token: cfg.Token}, "forgejo", forgejoStatusMap(), hc)
	}

	gitCF := git.NewGitClient(remote, cfg.BaseBranch, cfg.UserName, cfg.UserEmail, cfg.BranchPrefix)
	return &forgejoCodeForge{rest: restCli, repo: cfg.Repo, git: gitCF, mergeMethod: cfg.MergeMethod, remote: remote}
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

// postMerge POSTs a merge request for the PR at index with the base merge
// fields — f.mergeMethod's style (forgejoMergeDo) and deletion of the head
// branch after merge — plus any extra fields. A non-2xx response is
// translated to an error by the underlying rest.Client: a 405 or 409 (both
// used by Forgejo's merge endpoint to mean "not mergeable") wraps
// errMergeRefused, which Merge disambiguates via classifyMergeFailure;
// EnqueueAutoMerge instead propagates any error from this call raw.
func (f *forgejoCodeForge) postMerge(index string, extra map[string]any) error {
	body := map[string]any{
		"Do":                        forgejoMergeDo(f.mergeMethod),
		"delete_branch_after_merge": true,
	}
	for k, v := range extra {
		body[k] = v
	}
	return f.rest.Do(http.MethodPost, f.repoPath()+"/pulls/"+index+"/merge", body, nil)
}

// Merge merges the pull request at prURL via Forgejo's REST merge endpoint,
// requesting f.mergeMethod's style (forgejoMergeDo) and deletion of the head
// branch after merge. A merge-refusal (errMergeRefused, from a 405 or 409
// response) is classified by f.classifyMergeFailure; any other error is
// returned as-is.
func (f *forgejoCodeForge) Merge(prURL string) error {
	index, err := parsePRIndex(prURL)
	if err != nil {
		return err
	}
	err = f.postMerge(index, nil)
	if err == nil {
		return nil
	}
	if errors.Is(err, errMergeRefused) {
		return f.classifyMergeFailure(prURL, err)
	}
	return err
}

// classifyMergeFailure distinguishes a genuine merge conflict from a PR
// that's merely blocked by pending or failing required checks, given cause —
// the errMergeRefused-wrapping error postMerge returned for Forgejo's "not
// mergeable" refusal (405 Method Not Allowed or 409 Conflict; Forgejo uses
// both for the same refusal). Those two — and only those two — are
// disambiguated by querying the PR's mergeable state and handing it to the
// shared forge.ClassifyMergeFailure, which owns the actual state-to-sentinel
// mapping. Any other non-2xx status (403 token lacks merge scope, 429 rate
// limit, 500 server error) is a genuine failure that never reaches this
// function — Merge returns it as-is instead of masking it behind
// ErrMergeConflict or ErrMergeBlockedByChecks. A refusal cause whose
// mergeable state forge.ClassifyMergeFailure cannot map to either outcome is
// likewise surfaced as its own error.
func (f *forgejoCodeForge) classifyMergeFailure(prURL string, cause error) error {
	state, err := f.Mergeable(prURL)
	if err != nil {
		return fmt.Errorf("forgejo: merge %s: %w (mergeable state unavailable: %w)", prURL, cause, err)
	}
	if sentinel, ok := forge.ClassifyMergeFailure(state); ok {
		return sentinel
	}
	return fmt.Errorf("forgejo: merge %s: %w (mergeable state %q undetermined)", prURL, cause, state)
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

// Probe checks Forgejo connectivity/auth and returns the repository's full
// name (owner/repo), driving the REST client directly (not git ls-remote),
// so it also validates the token and instance reachability, not just the
// remote's git-level presence.
func (f *forgejoCodeForge) Probe() (string, error) {
	var payload forgejoRepoPayload
	if err := f.rest.Do(http.MethodGet, f.repoPath(), nil, &payload); err != nil {
		if errors.Is(err, forge.ErrAuthFailure) {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", forge.ErrRepoNotFound, err)
	}
	return payload.FullName, nil
}
